// Package recorded reads what the control plane keeps in control.db, which the host reads
// too: `gnat status` reports a swarm that is down from it. The control plane writes the
// database (see the control package) and uses the loaders here to read it back.
package recorded

import (
	"database/sql"
	"encoding/json"
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

// Current reports whether what the database has under the agent's name is the agent's
// own. The control plane records each agent's ID (see the state package) in the agent_ids
// table, and an agent whose ID isn't the one recorded for its name is a new agent by an
// old name, whose predecessor's history isn't its own: the control plane forgets that
// history when it starts. An agent without an ID, saved before agents had them, owns
// whatever its name has.
func Current(db *sql.DB, agent, id string) (bool, error) {
	if id == "" {
		return true, nil
	}
	// A database no control plane with IDs has opened has no table for them.
	if ok, err := hasTable(db, "agent_ids"); !ok || err != nil {
		return false, err
	}
	var recorded string
	err := db.QueryRow("SELECT id FROM agent_ids WHERE agent = ?", agent).Scan(&recorded)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return recorded == id, nil
}

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

// Queue is what is waiting in an agent's queue: how many messages, and how many each
// sender sent, the operator first and then agents by name.
type Queue struct {
	Total int
	By    []protocol.Queued
}

// LoadQueue reads what is waiting in the agent's queue.
func LoadQueue(db *sql.DB, agent string) (Queue, error) {
	var q Queue
	// The operator's messages have no sender, and NULL sorts first.
	rows, err := db.Query("SELECT sender, COUNT(*), SUM(urgent != 0) FROM messages WHERE agent = ? "+
		"GROUP BY sender ORDER BY sender", agent)
	if err != nil {
		return q, err
	}
	defer rows.Close()
	for rows.Next() {
		var by protocol.Queued
		var sender sql.NullString
		if err := rows.Scan(&sender, &by.Queued, &by.Now); err != nil {
			return q, err
		}
		if sender.Valid {
			by.Sender = &sender.String
		}
		q.Total += by.Queued
		q.By = append(q.By, by)
	}
	return q, rows.Err()
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

// hasTable is whether the database has the table, which one written by an earlier
// version of gnat, or by the Python swarm, may not.
func hasTable(db *sql.DB, name string) (bool, error) {
	var tables int
	err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&tables)
	return tables > 0, err
}

// LoadPaused reads whether the agent is paused: `gnat pause` stops its queued messages
// from starting turns until `gnat resume`.
func LoadPaused(db *sql.DB, agent string) (bool, error) {
	if ok, err := hasTable(db, "paused"); !ok || err != nil {
		return false, err
	}
	var n int
	err := db.QueryRow("SELECT COUNT(*) FROM paused WHERE agent = ?", agent).Scan(&n)
	return n > 0, err
}

// LoadContext reads how full the agent's context window last was, nil if the database
// has nothing on it.
func LoadContext(db *sql.DB, agent string) (*events.ContextWindow, error) {
	if ok, err := hasTable(db, "context"); !ok || err != nil {
		return nil, err
	}
	var c events.ContextWindow
	err := db.QueryRow("SELECT used, size FROM context WHERE agent = ?", agent).Scan(&c.Used, &c.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &c, nil
}

// LoadLimits reads the usage limits the agent's harness last reported, nil if the
// database has none.
func LoadLimits(db *sql.DB, agent string) (*protocol.Limits, error) {
	if ok, err := hasTable(db, "limits"); !ok || err != nil {
		return nil, err
	}
	var l protocol.Limits
	var windows string
	err := db.QueryRow("SELECT windows, reported FROM limits WHERE agent = ?", agent).Scan(&windows, &l.Reported)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(windows), &l.Windows); err != nil {
		return nil, err
	}
	return &l, nil
}

// Write writes each agent's status as `status` does, from what control.db in logDir last
// recorded, for `gnat status` while the swarm is down. It only reads the database. A turn
// an agent was running when the swarm went down is reported as interrupted, as it will
// be when the swarm comes back up, and an agent whose ID (from ids, by name; absent for
// none) isn't the one recorded for its name has no history, as it won't then (see Current).
func Write(logDir string, agents []string, ids map[string]string, w io.Writer) error {
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
		var queue Queue
		var context *events.ContextWindow
		var limits *protocol.Limits
		paused, current := false, false
		if db != nil {
			var err error
			if current, err = Current(db, agent, ids[agent]); err != nil {
				return err
			}
		}
		if current {
			var err error
			if queue, err = LoadQueue(db, agent); err != nil {
				return err
			}
			if turn, _, err = LoadTurn(db, agent); err != nil {
				return err
			}
			if err := LoadUsage(db, agent, &usage); err != nil {
				return err
			}
			if context, err = LoadContext(db, agent); err != nil {
				return err
			}
			if limits, err = LoadLimits(db, agent); err != nil {
				return err
			}
			if paused, err = LoadPaused(db, agent); err != nil {
				return err
			}
		}
		if turn.Busy {
			turn.Busy, turn.Exit, turn.Stopped = false, nil, false
			turn.Turns++
		}
		r := protocol.OK()
		r.State, r.Turns, r.Exit, r.Stopped = "down", turn.Turns, turn.Exit, turn.Stopped
		r.Queued, r.QueuedBy = queue.Total, queue.By
		r.Reply, r.Error = turn.Reply, turn.Error
		r.Usage, r.Context, r.Limits, r.Paused = &usage, context, limits, paused
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
