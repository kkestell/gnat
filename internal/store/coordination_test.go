package store

import (
	"github.com/kkestell/gnat/internal/workspace"
	"testing"
)

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
