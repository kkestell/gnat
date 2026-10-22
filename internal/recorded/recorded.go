// Package recorded reads what the control plane keeps in control.db, which the host reads
// too: `gnat status` reports a swarm that is down from it. The control plane writes the
// database (see the control package) and uses the loaders here to read it back.
package recorded

import (
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/kkestell/gnat/internal/events"
	"github.com/kkestell/gnat/internal/protocol"
	_ "modernc.org/sqlite" // The database driver.
)

// DBFile holds each agent's queued messages and how its last turn went, so a swarm that
// goes down and comes back up carries on where it was.
const DBFile = "control.db"

// Turn is where an agent's work was, as the database keeps it.
type Turn struct {
	Busy  bool
	Turns int
	// Exit is the last finished turn's exit status, nil if the agent restarted mid-turn.
	Exit *int
	// Reply is the harness's final reply, nil if it never gave one; Error is why the
	// harness said the turn failed, if it did.
	Reply, Error *string
	// Stopped is whether the last turn was ended by `gnat stop`.
	Stopped bool
}

// LoadTurn reads the agent's status, returning false if the database has none.
func LoadTurn(db *sql.DB, agent string) (Turn, bool, error) {
	var t Turn
	var busy, stopped int
	var exit sql.NullInt64
	var reply, failure sql.NullString
	err := db.QueryRow("SELECT busy, turns, exit, reply, error, stopped FROM statuses WHERE agent = ?",
		agent).Scan(&busy, &t.Turns, &exit, &reply, &failure, &stopped)
	if errors.Is(err, sql.ErrNoRows) {
		return t, false, nil
	} else if err != nil {
		return t, false, err
	}
	t.Busy, t.Stopped = busy != 0, stopped != 0
	if exit.Valid {
		code := int(exit.Int64)
		t.Exit = &code
	}
	if reply.Valid {
		t.Reply = &reply.String
	}
	if failure.Valid {
		t.Error = &failure.String
	}
	return t, true, nil
}

// LoadUsage reads the agent's usage totals into total, leaving them as they are if the
// database has none.
func LoadUsage(db *sql.DB, agent string, total *events.UsageTotals) error {
	t := total
	err := db.QueryRow("SELECT input_tokens, cache_read_tokens, cache_write_tokens, output_tokens, cost FROM usage WHERE agent = ?",
		agent).Scan(&t.InputTokens, &t.CacheReadTokens, &t.CacheWriteTokens, &t.OutputTokens, &t.Cost)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

// Write writes each agent's status as `status` does, from what control.db in logDir last
// recorded, for `gnat status` while the swarm is down. It only reads the database. A turn
// an agent was running when the swarm went down is reported as interrupted, as it will
// be when the swarm comes back up.
func Write(logDir string, agents []string, w io.Writer) error {
	path := filepath.Join(logDir, DBFile)
	var db *sql.DB
	if _, err := os.Stat(path); err == nil {
		if db, err = sql.Open("sqlite", path); err != nil {
			return err
		}
		defer db.Close()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, agent := range agents {
		var turn Turn
		var usage events.UsageTotals
		var queued int
		if db != nil {
			if err := db.QueryRow("SELECT COUNT(*) FROM messages WHERE agent = ?", agent).Scan(&queued); err != nil {
				return err
			}
			var err error
			if turn, _, err = LoadTurn(db, agent); err != nil {
				return err
			}
			if err := LoadUsage(db, agent, &usage); err != nil {
				return err
			}
		}
		if turn.Busy {
			turn.Busy, turn.Exit, turn.Stopped = false, nil, false
			turn.Turns++
		}
		r := protocol.OK()
		r.State, r.Queued, r.Turns, r.Exit, r.Stopped = "down", queued, turn.Turns, turn.Exit, turn.Stopped
		r.Reply, r.Error = turn.Reply, turn.Error
		r.Usage = &usage
		line, err := protocol.Encode(protocol.StatusLine(agent, r))
		if err != nil {
			return err
		}
		if _, err := w.Write(line); err != nil {
			return err
		}
	}
	return nil
}
