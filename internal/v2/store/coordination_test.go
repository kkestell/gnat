package store

import (
	"database/sql"
	"github.com/kkestell/gnat/internal/v2/workspace"
	"path/filepath"
	"testing"
)

func TestMigrationFromMilestoneTwo(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "gnat.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(schema + migration2 + `INSERT INTO workspaces VALUES('w','/repo','/repo/.git','repo','today'); INSERT INTO swarms VALUES('s','w','a','running','today'); INSERT INTO agents(id,swarm_id,role,name,harness,model,effort,lifecycle,working_dir,conversation_id) VALUES('a','s','orchestrator','orchestrator','claude','model','high','ready','/repo','c'); INSERT INTO conversations VALUES('c','a','claude','saved-session','today',NULL); INSERT INTO messages VALUES('m','s','a','saved input','queued','today',1);`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	s := openTest(t, dir)
	v, err := s.Snapshot("s")
	if err != nil {
		t.Fatal(err)
	}
	if v.Orchestrator.Membership != "active" || v.WorkerDefaults.Model != "model" || v.Messages[0].Sender != "user" || v.Messages[0].AgentID != "a" || v.Conversations[0].SessionRef != "saved-session" {
		t.Fatal("migration lost state", v)
	}
}
func TestCoordinationRollbackAndScopedRequestIDs(t *testing.T) {
	s := openTest(t, t.TempDir())
	v := chatWorkspace(t, s, "/repo")
	c := Command{RequestID: "same", Name: "worker", Prompt: "hello"}
	r, _, err := s.Coordinate(v.ID, v.Orchestrator.ID, "create_agent", c, "base", v.Orchestrator.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.WorkerState(v.ID, r.AgentID, "active", "worktree_created", ""); err != nil {
		t.Fatal(err)
	}
	for _, caller := range []string{v.Orchestrator.ID, r.AgentID} {
		_, _, err = s.Coordinate(v.ID, caller, "send_message", Command{RequestID: "message", AgentID: v.Orchestrator.ID, Text: "same"}, "", Settings{})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec(`CREATE TRIGGER fail_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT, 'disk failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Coordinate(v.ID, v.Orchestrator.ID, "send_message", Command{RequestID: "failed", AgentID: r.AgentID, Text: "must roll back"}, "", Settings{}); err == nil {
		t.Fatal("acknowledged failed message")
	}
	got, err := s.Snapshot(v.ID)
	if err != nil || len(got.Messages) != 3 {
		t.Fatal("partial acceptance", got.Messages, err)
	}
}

func TestSavedWorkerDefaults(t *testing.T) {
	s := openTest(t, t.TempDir())
	v, err := s.OpenWorkspace(workspace.Workspace{Root: "/defaults", Name: "defaults"}, func() (Settings, error) {
		return Settings{Harness: "claude", Model: "orchestrator-model", Effort: "high", Workers: &Settings{Model: "worker-model"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defaults := v.WorkerDefaults
	if defaults.Harness != "claude" || defaults.Model != "worker-model" || defaults.Effort != "high" {
		t.Fatal("missing worker defaults", defaults)
	}
	reopened, err := s.OpenWorkspace(v.Workspace, func() (Settings, error) { t.Fatal("reloaded saved defaults"); return Settings{}, nil })
	if err != nil || reopened.WorkerDefaults != defaults {
		t.Fatal("defaults not saved", reopened.WorkerDefaults, err)
	}
	resolved := ResolveSettings(defaults, Settings{Effort: "low"})
	if resolved.Harness != "claude" || resolved.Model != "worker-model" || resolved.Effort != "low" {
		t.Fatal("override lost defaults", resolved)
	}
}
