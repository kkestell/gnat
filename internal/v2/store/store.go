// Package store owns v2 SQLite records. It never reads or migrates v1 state.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kkestell/gnat/internal/v2/workspace"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE workspaces (id TEXT PRIMARY KEY, root TEXT NOT NULL UNIQUE, common_dir TEXT NOT NULL, name TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE swarms (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL UNIQUE REFERENCES workspaces(id), orchestrator_id TEXT NOT NULL, scheduling TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE agents (id TEXT PRIMARY KEY, swarm_id TEXT NOT NULL REFERENCES swarms(id), role TEXT NOT NULL, name TEXT NOT NULL, harness TEXT NOT NULL, model TEXT NOT NULL, effort TEXT NOT NULL, lifecycle TEXT NOT NULL, working_dir TEXT NOT NULL UNIQUE, branch TEXT, conversation_id TEXT NOT NULL);
CREATE TABLE conversations (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), harness TEXT NOT NULL, session_ref TEXT, created_at TEXT NOT NULL, ended_at TEXT);
CREATE TABLE events (sequence INTEGER PRIMARY KEY AUTOINCREMENT, schema_version INTEGER NOT NULL, swarm_id TEXT NOT NULL REFERENCES swarms(id), agent_id TEXT, conversation_id TEXT, created_at TEXT NOT NULL, payload TEXT NOT NULL);
PRAGMA user_version = 1;
`

type Store struct{ db *sql.DB }

type Snapshot struct {
	ID            string              `json:"id"`
	WorkspaceID   string              `json:"workspace_id"`
	Workspace     workspace.Workspace `json:"workspace"`
	Orchestrator  Agent               `json:"orchestrator"`
	Scheduling    string              `json:"scheduling"`
	CreatedAt     string              `json:"created_at"`
	Cursor        int64               `json:"cursor"`
	Messages      []Message           `json:"messages"`
	Turns         []Turn              `json:"turns"`
	Events        []Event             `json:"events"`
	Conversations []Conversation      `json:"conversations"`
}

type Agent struct {
	ID             string   `json:"id"`
	Role           string   `json:"role"`
	Name           string   `json:"name"`
	Settings       Settings `json:"settings"`
	Lifecycle      string   `json:"lifecycle"`
	WorkingDir     string   `json:"working_dir"`
	Branch         *string  `json:"branch"`
	ConversationID string   `json:"conversation_id"`
	Error          string   `json:"error"`
}

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "gnat.db")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db}
	if err := s.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) initialize() error {
	if _, err := s.db.Exec(`PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000; PRAGMA journal_mode = WAL;`); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	switch version {
	case 0:
		var tables int
		if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil {
			return err
		}
		if tables != 0 {
			return fmt.Errorf("refusing unversioned nonempty v2 database")
		}
		if _, err := tx.Exec(schema); err != nil {
			return err
		}
	case 1:
	case 2:
	default:
		return fmt.Errorf("unsupported v2 database schema version %d", version)
	}
	if version < 2 {
		if _, err := tx.Exec(migration2); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }

// OpenWorkspace commits identity, configuration, conversation and core events together.
// Settings are only loaded for a new swarm; reopening uses its resolved settings.
func (s *Store) OpenWorkspace(w workspace.Workspace, load func() (Settings, error)) (Snapshot, error) {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	var id string
	// Agent paths also identify future managed worktrees, without guessing ownership.
	err = tx.QueryRow(`SELECT swarm_id FROM agents WHERE working_dir = ? UNION SELECT id FROM swarms WHERE workspace_id IN (SELECT id FROM workspaces WHERE root = ?) LIMIT 1`, w.Root, w.Root).Scan(&id)
	if err != nil && err != sql.ErrNoRows {
		return Snapshot{}, err
	}
	if err == sql.ErrNoRows {
		settings, err := load()
		if err != nil {
			return Snapshot{}, err
		}
		wid, aid, cid := NewID(), NewID(), NewID()
		id = NewID()
		now := time.Now().UTC().Format(time.RFC3339Nano)
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO workspaces VALUES (?,?,?,?,?)`, []any{wid, w.Root, w.CommonDir, w.Name, now}},
			{`INSERT INTO swarms VALUES (?,?,?,?,?)`, []any{id, wid, aid, "running", now}},
			{`INSERT INTO agents(id,swarm_id,role,name,harness,model,effort,lifecycle,working_dir,branch,conversation_id) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, []any{aid, id, "orchestrator", "orchestrator", settings.Harness, settings.Model, settings.Effort, "pending", w.Root, nil, cid}},
			{`INSERT INTO conversations VALUES (?,?,?,?,?,?)`, []any{cid, aid, settings.Harness, nil, now, nil}},
		}
		for _, stmt := range statements {
			if _, err := tx.Exec(stmt.query, stmt.args...); err != nil {
				return Snapshot{}, err
			}
		}
		for _, kind := range []string{"workspace_registered", "swarm_created", "agent_created", "conversation_created"} {
			payload, _ := json.Marshal(map[string]string{"type": kind})
			if _, err := tx.Exec(`INSERT INTO events(schema_version,swarm_id,agent_id,conversation_id,created_at,payload) VALUES (1,?,?,?,?,?)`, id, aid, cid, now, string(payload)); err != nil {
				return Snapshot{}, err
			}
		}
	}
	snapshot, err := fullSnapshot(tx, id)
	if err != nil {
		return Snapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

type queryer interface{ QueryRow(string, ...any) *sql.Row }

func readSnapshot(q queryer, id string) (Snapshot, error) {
	var v Snapshot
	err := q.QueryRow(`SELECT s.id,w.id,w.root,w.common_dir,w.name,s.scheduling,s.created_at,a.id,a.role,a.name,a.harness,a.model,a.effort,a.lifecycle,a.working_dir,a.branch,a.conversation_id,a.error,(SELECT coalesce(max(sequence),0) FROM events) FROM swarms s JOIN workspaces w ON w.id=s.workspace_id JOIN agents a ON a.id=s.orchestrator_id WHERE s.id=?`, id).Scan(&v.ID, &v.WorkspaceID, &v.Workspace.Root, &v.Workspace.CommonDir, &v.Workspace.Name, &v.Scheduling, &v.CreatedAt, &v.Orchestrator.ID, &v.Orchestrator.Role, &v.Orchestrator.Name, &v.Orchestrator.Settings.Harness, &v.Orchestrator.Settings.Model, &v.Orchestrator.Settings.Effort, &v.Orchestrator.Lifecycle, &v.Orchestrator.WorkingDir, &v.Orchestrator.Branch, &v.Orchestrator.ConversationID, &v.Orchestrator.Error, &v.Cursor)
	return v, err
}

func (s *Store) Snapshot(id string) (Snapshot, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	v, err := fullSnapshot(tx, id)
	if err != nil {
		return v, err
	}
	return v, tx.Commit()
}

func (s *Store) List() ([]Snapshot, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM swarms ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	all := make([]Snapshot, 0, len(ids))
	for _, id := range ids {
		v, err := readSnapshot(tx, id)
		if err != nil {
			return nil, err
		}
		all = append(all, v)
	}
	return all, tx.Commit()
}
