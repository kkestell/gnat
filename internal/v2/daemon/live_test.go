package daemon

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/v2/core"
	"github.com/kkestell/gnat/internal/v2/harness"
	"github.com/kkestell/gnat/internal/v2/store"
	"github.com/kkestell/gnat/internal/v2/workspace"
)

// Opt in with GNAT_LIVE_TEST=1 go test ./internal/v2/daemon -run TestLiveCoordination -v -timeout 10m.
func TestLiveCoordination(t *testing.T) {
	if os.Getenv("GNAT_LIVE_TEST") != "1" {
		t.Skip("requires authenticated local Claude Code")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	git("init", "-b", "main")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.test")
	git("commit", "--allow-empty", "-m", "initial")
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
		return store.Settings{Harness: "claude", Model: "sonnet", Effort: "low"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	desc := Descriptor{URL: "http://" + listener.Addr().String(), Token: store.NewID()}
	service, err := core.New(db, harness.Claude{}, desc.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	server := &http.Server{Handler: Handler(db, service, desc, func() {})}
	go server.Serve(listener)
	defer server.Close()
	send := func(text string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"action": "send", "request_id": store.NewID(), "text": text})
		req, _ := http.NewRequest("POST", desc.URL+"/api/swarms/"+v.ID+"/actions", strings.NewReader(string(body)))
		req.Header.Set("Origin", desc.URL)
		req.Header.Set("X-Gnat-Browser", "1")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatal("send", res.Status)
		}
	}
	wait := func(label string, predicate func(store.Snapshot) bool) store.Snapshot {
		t.Helper()
		deadline := time.Now().Add(4 * time.Minute)
		last := time.Now()
		for time.Now().Before(deadline) {
			current, err := db.Snapshot(v.ID)
			if err != nil {
				t.Fatal(err)
			}
			if predicate(current) {
				t.Log(label)
				return current
			}
			if time.Since(last) > 30*time.Second {
				t.Logf("waiting for %s: orchestrator %s, workers %d", label, current.Orchestrator.Lifecycle, len(current.Workers))
				last = time.Now()
			}
			if current.Orchestrator.Lifecycle == "error" {
				t.Fatalf("orchestrator error: %s", current.Orchestrator.Error)
			}
			time.Sleep(200 * time.Millisecond)
		}
		current, _ := db.Snapshot(v.ID)
		for _, e := range current.Events {
			if e.Payload["type"] == "message" || e.Payload["type"] == "tool_result" {
				t.Log(e.Payload)
			}
		}
		t.Fatalf("timed out: %s", label)
		return current
	}
	send(`Use only the gnat MCP tools for this coordination smoke test. Create exactly two Gnat workers named one and two with initial prompts: "Use gnat list_agents to find the orchestrator, send it the message ready-one [or ready-two, respectively] using gnat send_message, then end your turn. Do not edit files. If you later receive a follow-up, send follow-up-ok to the orchestrator and end." After creating both workers, end your turn. On subsequent ready or follow-up-ok messages just acknowledge and end; do not send replies or create more workers. Do not use native subagents.`)
	current := wait("two workers replied", func(v store.Snapshot) bool {
		ready := 0
		for _, m := range v.Messages {
			if m.Sender != "user" && m.Sender != "system" && (strings.Contains(m.Text, "ready-one") || strings.Contains(m.Text, "ready-two")) {
				ready++
			}
		}
		return len(v.Workers) == 2 && ready >= 2
	})
	if current.Workers[0].WorkingDir == ws.Root || current.Workers[1].WorkingDir == ws.Root {
		t.Fatal("worker used original checkout")
	}
	send("Send an ordinary gnat message to worker one asking for the follow-up-ok reply, then end. Do not create any workers.")
	wait("follow-up delivered", func(v store.Snapshot) bool {
		for _, m := range v.Messages {
			if m.Sender == current.Workers[0].ID && strings.Contains(m.Text, "follow-up-ok") {
				return true
			}
		}
		return false
	})
	send("Remove worker two using gnat remove_agent. Do not remove worker one. End after the tool accepts the request.")
	final := wait("worker removed with history retained", func(v store.Snapshot) bool { return len(v.Workers) == 1 && len(v.RemovedWorkers) == 1 })
	if len(final.Messages) < 6 {
		t.Fatal("lost message history")
	}
	if _, err := os.Stat(filepath.Join(final.Workers[0].WorkingDir, ".git")); err != nil {
		t.Fatal("surviving worktree unavailable", err)
	}
	// TempDir cleanup must not leave linked-worktree metadata in the disposable repo.
	service.Close()
	for _, a := range final.Workers {
		_ = exec.Command("git", "-C", root, "worktree", "remove", a.WorkingDir).Run()
	}
}
