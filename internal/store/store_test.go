package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/kkestell/gnat/internal/workspace"
)

func settings() (Settings, error) {
	return Settings{Harness: "claude", Model: "example-model", Effort: "high"}, nil
}
func openTest(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestWorkspacePersistenceAndConcurrency(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	ws := workspace.Workspace{Root: "/repo", CommonDir: "/repo/.git", Name: "repo"}
	const count = 12
	results := make(chan Snapshot, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() { v, err := s.OpenWorkspace(ws, settings); results <- v; errs <- err })
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var original Snapshot
	for v := range results {
		if original.ID == "" {
			original = v
		}
		if !reflect.DeepEqual(v, original) {
			t.Fatalf("duplicate identity: %+v %+v", original, v)
		}
	}
	if original.Cursor != 4 || len(original.Events) != 0 || original.Orchestrator.Lifecycle != "pending" || original.Orchestrator.WorkingDir != ws.Root {
		t.Fatalf("snapshot: %+v", original)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, dir)
	v, err := s.OpenWorkspace(ws, func() (Settings, error) { t.Fatal("existing swarm reloaded configuration"); return Settings{}, nil })
	if err != nil || !reflect.DeepEqual(v, original) {
		t.Fatalf("reopen: %+v %v", v, err)
	}
	other, err := s.OpenWorkspace(workspace.Workspace{Root: "/linked", CommonDir: ws.CommonDir, Name: "linked"}, settings)
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == v.ID || other.WorkspaceID == v.WorkspaceID || other.Orchestrator.ID == v.Orchestrator.ID {
		t.Fatal("shared repository collapsed distinct checkout identities")
	}
	// Explicit ownership redirects a future managed worktree to its owning swarm.
	_, err = s.db.Exec(`INSERT INTO agents(id,swarm_id,role,name,harness,model,effort,lifecycle,working_dir,branch,conversation_id) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, NewID(), v.ID, "worker", "worker", "claude", "model", "high", "pending", "/managed", "branch", NewID())
	if err != nil {
		t.Fatal(err)
	}
	managed, err := s.OpenWorkspace(workspace.Workspace{Root: "/managed"}, settings)
	if err != nil || managed.ID != v.ID {
		t.Fatalf("managed ownership: %+v %v", managed, err)
	}

}

func TestCreationRollback(t *testing.T) {
	s := openTest(t, t.TempDir())
	if _, err := s.db.Exec(`CREATE TRIGGER fail_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT, 'disk failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenWorkspace(workspace.Workspace{Root: "/repo", CommonDir: "/repo/.git", Name: "repo"}, settings); err == nil {
		t.Fatal("acknowledged failed transaction")
	}
	for _, table := range []string{"workspaces", "swarms", "agents", "conversations", "events"} {
		var count int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial %s: %d %v", table, count, err)
		}
	}
}

func TestUnknownSchema(t *testing.T) {
	for _, sqlText := range []string{`PRAGMA user_version=2`, `PRAGMA user_version=3`, `PRAGMA user_version=99`, `CREATE TABLE unrelated (name TEXT)`} {
		dir := t.TempDir()
		db, err := sql.Open("sqlite", filepath.Join(dir, "gnat.db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(sqlText); err != nil {
			t.Fatal(err)
		}
		db.Close()
		if s, err := Open(dir); err == nil {
			s.Close()
			t.Fatal("opened incompatible database")
		}
	}
}

func TestConfiguration(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	dir := filepath.Join(base, "gnat")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ text, want string }{
		{"orchestrator: {}", "orchestrator.harness"},
		{"orchestrator: {harness: claude}", "orchestrator.model"},
		{"orchestrator: {harness: claude, model: example}", "orchestrator.effort"},
		{"orchestrator: {harness: ox, model: example, effort: high}", "unsupported"},
		{"orchestrator: [", "configuration"},
	}
	for _, tc := range cases {
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(tc.text), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadSettings()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%q: %v", tc.text, err)
		}
	}
	for _, h := range []string{"claude", "codex", "opencode"} {
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("orchestrator: {harness: "+h+", model: example, effort: high}"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSettings(); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_STATE_HOME", base)
	path, err := StateDir()
	if err != nil || path != filepath.Join(base, "gnat") {
		t.Fatal(path, err)
	}
}
