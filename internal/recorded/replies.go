package recorded

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kkestell/gnat/internal/events"
)

// EventsFile is the control plane's append-only history, one {agent, event} per line.
const EventsFile = "events.jsonl"

// WriteReplies prints final replies in recording order, preserving their text.
// Agent IDs exclude a deleted predecessor's history, as they do for status.
func WriteReplies(logDir string, agents []string, ids map[string]string, w io.Writer) error {
	var db *sql.DB
	path := filepath.Join(logDir, DBFile)
	if _, err := os.Stat(path); err == nil {
		db, err = sql.Open("sqlite", path)
		if err != nil {
			return err
		}
		defer db.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	selected := map[string]bool{}
	for _, agent := range agents {
		current := ids[agent] == ""
		if db != nil {
			var err error
			if current, err = Current(db, agent, ids[agent]); err != nil {
				return err
			}
		}
		selected[agent] = current
	}
	f, err := os.Open(filepath.Join(logDir, EventsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	defer f.Close()
	turns := map[string]int{}
	senders := map[string]string{}
	r := bufio.NewReader(f)
	for line := 1; ; line++ {
		data, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			// A running control plane may still be appending the last record.
			return nil
		} else if err != nil {
			return err
		}
		var record struct {
			Agent string       `json:"agent"`
			Event events.Event `json:"event"`
		}
		if err := json.Unmarshal(data, &record); err != nil {
			return fmt.Errorf("%s line %d: %w", EventsFile, line, err)
		}
		e, agent := record.Event, record.Agent
		if !selected[agent] || e["parent"] != nil && e["parent"] != "" {
			continue
		}
		switch e.Type() {
		case "swarm_message":
			if e["mid_turn"] == true {
				continue
			}
			turns[agent]++
			senders[agent], _ = e["sender"].(string)
		case events.TurnEnd:
			reply, _ := e["reply"].(string)
			if reply == "" {
				continue
			}
			sender := senders[agent]
			if sender == "" {
				sender = "operator"
			}
			if _, err := fmt.Fprintf(w, "==> %s: turn %d (reply to %s) <==\n%s", agent, turns[agent], sender, reply); err != nil {
				return err
			}
			if !strings.HasSuffix(reply, "\n") {
				if _, err := io.WriteString(w, "\n"); err != nil {
					return err
				}
			}
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
	}
}
