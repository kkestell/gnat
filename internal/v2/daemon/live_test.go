package daemon

import (
	"encoding/json"
	"fmt"
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

// Opt in with GNAT_LIVE_TEST=1 go test ./internal/v2/daemon -run TestLiveCoordination -v -timeout 20m.
// GNAT_LIVE_HARNESS optionally selects one orchestrator from the matrix.
func TestLiveCoordination(t *testing.T) {
	if os.Getenv("GNAT_LIVE_TEST") != "1" {
		t.Skip("requires authenticated local Claude Code, Codex, and OpenCode")
	}
	settings := []store.Settings{
		{Harness: "claude", Model: "sonnet", Effort: "low"},
		{Harness: "codex", Model: "gpt-6.1-sol", Effort: "low"},
		{Harness: "opencode", Model: "deepseek/deepseek-flash", Effort: "default"},
	}
	for i, s := range settings {
		if selected := os.Getenv("GNAT_LIVE_HARNESS"); selected != "" && s.Harness != selected {
			continue
		}
		t.Run(s.Harness, func(t *testing.T) { liveCoordination(t, s, settings[(i+1)%3], settings[(i+2)%3]) })
	}
}

// liveTeam uses production adapters, browser mutations, and authenticated MCP.
type liveTeam struct {
	root    string
	db      *store.Store
	swarm   store.Snapshot
	send    func(string)
	control func(string)
	wait    func(string, func(store.Snapshot) bool) store.Snapshot
	restart func()
	stop    func()
}

func newLiveTeam(t *testing.T, settings store.Settings) *liveTeam {
	t.Helper()
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
	v, err := db.OpenWorkspace(ws, func() (store.Settings, error) {
		return settings, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	root = ws.Root
	var desc Descriptor
	var service *core.Service
	var server *http.Server
	start := func() {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		desc = Descriptor{URL: "http://" + listener.Addr().String(), Token: store.NewID()}
		service, err = core.New(db, harness.Registry{}, desc.URL+"/mcp")
		if err != nil {
			listener.Close()
			t.Fatal(err)
		}
		server = &http.Server{Handler: Handler(db, service, desc, func() {})}
		go server.Serve(listener)
	}
	start()
	t.Cleanup(func() { server.Close(); service.Close(); db.Close() })
	mutate := func(action, text string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"action": action, "request_id": store.NewID(), "text": text})
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
				states := []string{}
				for _, a := range current.Workers {
					states = append(states, a.Name+":"+a.Settings.Harness+":"+a.Lifecycle)
				}
				t.Logf("waiting for %s: orchestrator %s, workers %v", label, current.Orchestrator.Lifecycle, states)
				last = time.Now()
			}
			for _, a := range current.Workers {
				if a.Error != "" {
					t.Fatalf("worker %s: %s", a.Name, a.Error)
				}
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
	return &liveTeam{root: root, db: db, swarm: v, send: func(text string) { mutate("send", text) }, control: func(action string) { mutate(action, "") }, wait: wait, restart: func() { server.Close(); service.Close(); start() }, stop: func() { service.Close() }}
}

func liveCoordination(t *testing.T, settings, firstWorker, secondWorker store.Settings) {
	team := newLiveTeam(t, settings)
	root, db, v := team.root, team.db, team.swarm
	send, wait := team.send, team.wait
	send(fmt.Sprintf(`Use only the gnat MCP tools for this coordination smoke test. Create exactly two Gnat workers named one and two. Worker one must use harness %s, model %s, effort %s; worker two must use harness %s, model %s, effort %s. Their respective initial prompts are: "Use gnat list_agents to find the orchestrator, send it the message ready-one [or ready-two, respectively] using gnat send_message, then end your turn. Do not edit files. If you later receive a follow-up, send follow-up-ok to the orchestrator and end." After creating both workers, end your turn. On subsequent ready or follow-up-ok messages just acknowledge and end; do not send replies or create more workers. Do not use native subagents.`, firstWorker.Harness, firstWorker.Model, firstWorker.Effort, secondWorker.Harness, secondWorker.Model, secondWorker.Effort))
	current := wait("two workers replied", func(v store.Snapshot) bool {
		ready := map[string]bool{}
		workerIDs := map[string]bool{}
		for _, a := range v.Workers {
			workerIDs[a.ID] = true
		}
		for _, m := range v.Messages {
			if workerIDs[m.Sender] && m.AgentID == v.Orchestrator.ID && (strings.Contains(m.Text, "ready-one") || strings.Contains(m.Text, "ready-two")) {
				ready[m.Sender] = true
			}
		}
		return len(v.Workers) == 2 && len(ready) == 2 && settled(v)
	})
	if current.Workers[0].WorkingDir == root || current.Workers[1].WorkingDir == root {
		t.Fatal("worker used original checkout")
	}
	var workerOne string
	expected := map[string]store.Settings{"one": firstWorker, "two": secondWorker}
	for _, a := range current.Workers {
		if a.Settings != expected[a.Name] {
			t.Fatal("wrong worker settings", a.Settings)
		}
		if a.Name == "one" {
			workerOne = a.ID
		}
	}
	sessions := map[string]string{}
	for _, c := range current.Conversations {
		if c.SessionRef == "" {
			t.Fatal("missing saved session", c)
		}
		sessions[c.AgentID] = c.SessionRef
	}
	// Orderly restart renews MCP bindings while preserving exact provider sessions.
	team.restart()
	send("Send an ordinary gnat message to worker one asking for the follow-up-ok reply, then end. Do not create any workers.")
	wait("follow-up delivered", func(v store.Snapshot) bool {
		for _, m := range v.Messages {
			if m.Sender == workerOne && strings.Contains(m.Text, "follow-up-ok") {
				return settled(v)
			}
		}
		return false
	})
	resumed, err := db.Snapshot(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range resumed.Conversations {
		if sessions[c.AgentID] != c.SessionRef {
			t.Fatal("session changed across restart", c)
		}
	}
	send("Remove worker two using gnat remove_agent. Do not remove worker one. End after the tool accepts the request.")
	final := wait("worker removed with history retained", func(v store.Snapshot) bool { return len(v.Workers) == 1 && len(v.RemovedWorkers) == 1 })
	if len(final.Messages) < 6 {
		t.Fatal("lost message history")
	}
	if _, err := os.Stat(filepath.Join(final.Workers[0].WorkingDir, ".git")); err != nil {
		t.Fatal("surviving worktree unavailable", err)
	}
	// TempDir cleanup must not leave linked-worktree metadata in the disposable repo.
	team.stop()
	for _, a := range final.Workers {
		_ = exec.Command("git", "-C", root, "worktree", "remove", a.WorkingDir).Run()
	}
}

func settled(v store.Snapshot) bool {
	if v.Orchestrator.Lifecycle != "ready" {
		return false
	}
	for _, a := range v.Workers {
		if a.Lifecycle != "ready" {
			return false
		}
	}
	for _, m := range v.Messages {
		if m.State == "queued" {
			return false
		}
	}
	return true
}
