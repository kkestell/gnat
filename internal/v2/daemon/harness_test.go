package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/v2/core"
	"github.com/kkestell/gnat/internal/v2/harness"
	"github.com/kkestell/gnat/internal/v2/store"
	"github.com/kkestell/gnat/internal/v2/workspace"
)

func TestMixedHarnessDispatchAndRestart(t *testing.T) {
	fixture, err := os.ReadFile("../harness/testdata/rpc.py")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	for _, name := range []string{"codex", "opencode"} {
		if err = os.WriteFile(filepath.Join(bin, name), fixture, 0700); err != nil {
			t.Fatal(err)
		}
	}
	claude := `#!/usr/bin/env python3
import sys,json
args=sys.argv[1:]
if args==['--version']:print('fixture');sys.exit(0)
if args==['auth','status']:print('{"loggedIn":true}');sys.exit(0)
sys.stdin.read()
session=args[args.index('--resume')+1] if '--resume' in args else 'fixture-claude'
print(json.dumps({'type':'system','session_id':session}))
print(json.dumps({'type':'assistant','message':{'content':[{'type':'text','text':'hello'}]}}))
print(json.dumps({'type':'result','is_error':False,'session_id':session}))
`
	if err = os.WriteFile(filepath.Join(bin, "claude"), []byte(claude), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GNAT_TEST_TRACE", filepath.Join(bin, "trace"))
	t.Setenv("GNAT_TEST_CHILD", filepath.Join(bin, "child"))
	names := []string{"claude", "codex", "opencode"}
	for _, orchestrator := range names {
		t.Run(orchestrator, func(t *testing.T) {
			root := t.TempDir()
			git := func(args ...string) {
				t.Helper()
				if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("%v: %s %v", args, out, err)
				}
			}
			git("init", "-b", "main")
			git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "--allow-empty", "-m", "initial")
			ws, err := workspace.Discover(root)
			if err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			v, err := db.OpenWorkspace(ws, func() (store.Settings, error) {
				return store.Settings{Harness: orchestrator, Model: "fixture-model", Effort: "high"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			service, err := core.New(db, harness.Registry{}, "http://127.0.0.1:9999/mcp")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { service.Close() }()
			call := func(kind string, c store.Command) store.Result {
				t.Helper()
				c.RequestID = store.NewID()
				r, err := service.Coordinate(core.Binding{SwarmID: v.ID, AgentID: v.Orchestrator.ID}, kind, c)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			for _, name := range names {
				if name != orchestrator {
					call("create_agent", store.Command{Name: name, Harness: name, Model: "fixture-model", Effort: "high", Prompt: "initial"})
				}
			}
			if _, err = service.Mutate(v.ID, store.NewID(), "send", "initial", ""); err != nil {
				t.Fatal(err)
			}
			wait := func(turns int) store.Snapshot {
				t.Helper()
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					current, err := db.Snapshot(v.ID)
					if err != nil {
						t.Fatal(err)
					}
					for _, a := range append(current.Workers, current.Orchestrator) {
						if a.Error != "" {
							t.Fatal(a.Name, a.Error)
						}
					}
					if settled(current) && len(current.Turns) == turns {
						return current
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("mixed team did not settle")
				return store.Snapshot{}
			}
			current := wait(3)
			sessions := map[string]string{}
			for _, c := range current.Conversations {
				if c.SessionRef == "" {
					t.Fatal("missing session")
				}
				sessions[c.AgentID] = c.SessionRef
			}
			if len(sessions) != 3 {
				t.Fatal("missing agent sessions")
			}
			service.Close()
			service, err = core.New(db, harness.Registry{}, "http://127.0.0.1:9998/mcp")
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range current.Workers {
				call("send_message", store.Command{AgentID: a.ID, Text: "follow-up"})
			}
			if _, err = service.Mutate(v.ID, store.NewID(), "send", "follow-up", ""); err != nil {
				t.Fatal(err)
			}
			current = wait(6)
			for _, c := range current.Conversations {
				if sessions[c.AgentID] != c.SessionRef {
					t.Fatal("replaced saved session")
				}
			}
			for _, a := range current.Workers {
				if a.WorkingDir == ws.Root {
					t.Fatal("worker used original checkout")
				}
			}
			if _, err = service.Coordinate(core.Binding{SwarmID: v.ID, AgentID: current.Workers[0].ID}, "create_agent", store.Command{RequestID: store.NewID(), Name: "forbidden", Prompt: "no"}); err == nil {
				t.Fatal("worker acquired membership authority")
			}
			service.Close()
			for _, a := range current.Workers {
				git("worktree", "remove", a.WorkingDir)
			}
		})
	}
}
