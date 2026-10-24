package harness

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/events"
	"github.com/kkestell/gnat/internal/layout"
)

func fakeCodexServer() {
	mode := os.Getenv("GNAT_FAKE_CODEX")
	write := func(v any) { fmt.Println(string(marshal(v, false))) }
	respond := func(id json.RawMessage, result any) { write(map[string]any{"id": id, "result": result}) }
	thread, model, effort, active := "", "", "", ""
	threads, turns := 0, 0
	notify := func(method string, params map[string]any) {
		params["threadId"], params["turnId"] = thread, active
		if method == "turn/completed" {
			delete(params, "turnId") // Native completion has the ID inside turn.
		}
		write(map[string]any{"method": method, "params": params})
	}
	item := func(method, id, kind string, fields map[string]any) {
		fields["id"], fields["type"] = id, kind
		notify(method, map[string]any{"item": fields})
	}
	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		var req codexRPC
		if json.Unmarshal(reader.Bytes(), &req) != nil {
			os.Exit(2)
		}
		switch req.Method {
		case "initialize":
			if mode == "startup-hang" {
				time.Sleep(time.Minute)
			}
			respond(req.ID, map[string]any{})
		case "initialized":
		case "hooks/list":
			hook := map[string]any{
				"sourcePath": layout.AgentHome + "/.codex/hooks.json", "eventName": "postToolUse", "handlerType": "command",
				"command": strings.Join(layout.Command("hook"), " "), "enabled": true, "key": "gnat-hook", "currentHash": "hash", "trustStatus": "untrusted",
			}
			if mode == "missing-hook" {
				hook["command"] = "some other hook"
			}
			respond(req.ID, map[string]any{"data": []any{map[string]any{"hooks": []any{hook}}}})
		case "config/value/write":
			value, _ := req.Params["value"].(map[string]any)
			state, _ := value["gnat-hook"].(map[string]any)
			if req.Params["keyPath"] != "hooks.state" || len(value) != 1 || state["trusted_hash"] != "hash" {
				os.Exit(6)
			}
			respond(req.ID, map[string]any{})
		case "account/login/start":
			if mode == "login-error" {
				write(map[string]any{"id": req.ID, "error": map[string]any{"message": "rejected " + str(req.Params["accessToken"])}})
				continue
			}
			if req.Params["type"] != "chatgptAuthTokens" || req.Params["chatgptAccountId"] != "account" {
				os.Exit(3)
			}
			respond(req.ID, map[string]any{"type": "chatgptAuthTokens"})
		case "thread/start", "thread/resume":
			if mode == "resume-error" && req.Method == "thread/resume" {
				write(map[string]any{"id": req.ID, "error": map[string]any{"message": "session unavailable"}})
				continue
			}
			if req.Params["sandbox"] != "danger-full-access" || req.Params["approvalPolicy"] != "never" {
				os.Exit(4)
			}
			if req.Method == "thread/start" {
				threads++
				thread = fmt.Sprint("thread", threads)
			} else {
				threads++
				thread = str(req.Params["threadId"])
				active = "historical"
				item("item/completed", "replayed", "agentMessage", map[string]any{"text": "old reply"})
			}
			model = str(req.Params["model"])
			respond(req.ID, map[string]any{"thread": map[string]any{"id": thread}})
		case "turn/start":
			turns++
			active = fmt.Sprint("turn", turns)
			effort = str(req.Params["effort"])
			input, _ := req.Params["input"].([]any)
			block, _ := input[0].(map[string]any)
			text := str(block["text"])
			if mode == "early-items" {
				item("item/completed", "early", "agentMessage", map[string]any{"text": "early commentary", "phase": "commentary"})
			}
			respond(req.ID, map[string]any{"turn": map[string]any{"id": active}})
			if mode == "wrong-turn" {
				write(map[string]any{"method": "item/completed", "params": map[string]any{
					"threadId": thread, "turnId": "old-turn", "item": map[string]any{"id": "stale", "type": "agentMessage", "text": "stale reply"},
				}})
				write(map[string]any{"method": "turn/completed", "params": map[string]any{
					"threadId": "other-thread", "turn": map[string]any{"id": active, "status": "completed"},
				}})
			}
			switch mode {
			case "exit":
				return
			case "malformed":
				fmt.Println("secret invalid JSON")
				continue
			case "refresh", "interactive":
				method := "account/chatgptAuthTokens/refresh"
				if mode == "interactive" {
					method = "item/tool/requestUserInput"
				}
				write(map[string]any{"id": "server-request", "method": method, "params": map[string]any{}})
				continue
			case "failed-turn":
				notify("turn/completed", map[string]any{"turn": map[string]any{"id": active, "status": "failed", "error": map[string]any{"message": "unsupported effort"}}})
				continue
			}
			item("item/started", "cmd", "commandExecution", map[string]any{"command": "ls", "status": "inProgress"})
			if text == "sleep" {
				continue
			}
			item("item/completed", "cmd", "commandExecution", map[string]any{"command": "ls", "status": "completed", "exitCode": 0, "aggregatedOutput": "file.go"})
			notify("item/agentMessage/delta", map[string]any{"itemId": "reply", "delta": "partial"})
			reply := strings.Join([]string{text, thread, model, effort}, "|")
			item("item/completed", "comment", "agentMessage", map[string]any{"text": "commentary", "phase": "commentary"})
			item("item/completed", "reply", "agentMessage", map[string]any{"text": reply, "phase": "final_answer"})
			item("item/completed", "reply", "agentMessage", map[string]any{"text": reply, "phase": "final_answer"})
			total := 100
			if text == "second" {
				total = 200
			}
			if text == "third" {
				total = 300
			}
			usage := map[string]any{"inputTokens": total, "cachedInputTokens": total / 2, "outputTokens": total / 10}
			notify("thread/tokenUsage/updated", map[string]any{"tokenUsage": map[string]any{"total": usage, "last": usage, "modelContextWindow": 1000}})
			notify("turn/completed", map[string]any{"turn": map[string]any{"id": active, "status": "completed"}})
		case "turn/interrupt":
			if mode == "ignore-interrupt" {
				continue
			}
			respond(req.ID, map[string]any{})
			notify("turn/completed", map[string]any{"turn": map[string]any{"id": active, "status": "interrupted"}})
		default:
			if len(req.ID) > 0 && req.Error == nil {
				os.Exit(5)
			}
		}
	}
}

func testCodexRunner(t *testing.T, mode, path string) (*codexRunner, *collector) {
	t.Helper()
	t.Setenv("GNAT_FAKE_CODEX", mode)
	credential := CodexCredential{AccessToken: "private-access-token", AccountID: "account", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	t.Setenv("GNAT_CODEX_AUTH", string(marshal(credential, false)))
	c := new(collector)
	r := newCodexRunner("model", "high", c.emit, path)
	r.command = []string{os.Args[0]}
	r.requestWait = 2 * time.Second
	r.stopWait = 100 * time.Millisecond
	t.Cleanup(r.shutdown)
	return r, c
}

func runCodex(t *testing.T, r *codexRunner, text string, fresh bool) {
	t.Helper()
	if code, err := r.Run(text, fresh, make(chan struct{})); err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
}

func TestCodexContinuesResumesAndStartsNewThreads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gnat-session.json")
	r, c := testCodexRunner(t, "normal", path)
	runCodex(t, r, "first", false)
	runCodex(t, r, "second", false)
	if got := c.ofType(events.TurnEnd); len(got) != 2 || got[1]["reply"] != "second|thread1|model|high" {
		t.Fatalf("turns: %v", got)
	}
	assertEvents(t, c.ofType(events.Usage), []events.Event{
		usageEvent(n(50), n(50), nil, n(10), nil), usageEvent(n(50), n(50), nil, n(10), nil),
	})
	if len(c.ofType(events.Message)) != 4 {
		t.Fatal("streamed or completed text duplicated")
	}
	r.shutdown()
	r2, c2 := testCodexRunner(t, "normal", path)
	runCodex(t, r2, "third", false)
	assertEvents(t, c2.ofType(events.Usage), []events.Event{usageEvent(n(50), n(50), nil, n(10), nil)})
	if len(c2.ofType(events.Message)) != 2 || c2.last()["reply"] != "third|thread1|model|high" {
		t.Fatal("resume replayed historical output or changed the thread")
	}
	runCodex(t, r2, "fresh", true)
	if r2.session.Usage.Input != 100 || r2.session.Thread == "thread1" {
		t.Fatal("new thread retained old usage")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatal("saved session is not private")
	}
}

func TestCodexReportsProtocolAndAuthenticationFailures(t *testing.T) {
	for mode, want := range map[string]string{
		"exit": "exited", "malformed": "invalid JSON", "refresh": "gnat auth login codex",
		"interactive": "interactive input", "login-error": "account/login/start failed",
		"failed-turn":  "unsupported effort",
		"missing-hook": "message-delivery hook",
	} {
		t.Run(mode, func(t *testing.T) {
			r, c := testCodexRunner(t, mode, filepath.Join(t.TempDir(), "session"))
			code, err := r.Run("first", false, make(chan struct{}))
			if code == 0 || err != nil || !strings.Contains(str(c.last()["error"]), want) {
				t.Fatalf("Run = %d, %v; event = %v", code, err, c.last())
			}
			if strings.Contains(fmt.Sprint(c.all()), "private-access-token") || strings.Contains(fmt.Sprint(c.all()), "secret invalid") {
				t.Fatal("error exposed credentials or malformed output")
			}
		})
	}
}

func TestCodexFailedResumeKeepsTheSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session")
	r, _ := testCodexRunner(t, "normal", path)
	runCodex(t, r, "first", false)
	r.shutdown()
	r2, c := testCodexRunner(t, "resume-error", path)
	if code, _ := r2.Run("second", false, make(chan struct{})); code == 0 || !strings.Contains(str(c.last()["error"]), "thread/resume") {
		t.Fatal("failed resume was not reported")
	}
	if r2.session.Thread != "thread1" {
		t.Fatal("failed resume discarded the conversation")
	}
}

func TestCodexStopsStartupAndAnActiveTurn(t *testing.T) {
	for _, mode := range []string{"startup-hang", "normal", "ignore-interrupt"} {
		t.Run(mode, func(t *testing.T) {
			r, c := testCodexRunner(t, mode, filepath.Join(t.TempDir(), "session"))
			stop := make(chan struct{})
			done := make(chan int, 1)
			go func() { code, _ := r.Run("sleep", false, stop); done <- code }()
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				r.mu.Lock()
				started := r.signal != nil
				r.mu.Unlock()
				if (mode == "startup-hang" && started) || len(c.ofType(events.ToolCall)) > 0 {
					break
				}
				time.Sleep(time.Millisecond)
			}
			close(stop)
			r.Stop()
			select {
			case code := <-done:
				if code == 0 {
					t.Fatal("stopped turn succeeded")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("stop hung")
			}
			if mode != "startup-hang" {
				r.Stop() // A late stop between turns must do nothing.
				runCodex(t, r, "next", false)
			}
		})
	}
}

func TestCodexStoppedBeforeRunDoesNotStartAProcess(t *testing.T) {
	r, _ := testCodexRunner(t, "normal", filepath.Join(t.TempDir(), "session"))
	stop := make(chan struct{})
	close(stop)
	if code, _ := r.Run("first", false, stop); code == 0 || r.server != nil {
		t.Fatal("started a stopped turn")
	}
}

func TestCodexStartupTimeoutAndMissingBinary(t *testing.T) {
	for _, mode := range []string{"startup-hang", "missing"} {
		t.Run(mode, func(t *testing.T) {
			r, c := testCodexRunner(t, mode, filepath.Join(t.TempDir(), "session"))
			r.requestWait = 100 * time.Millisecond
			if mode == "missing" {
				r.command = []string{filepath.Join(t.TempDir(), "missing")}
			}
			if code, _ := r.Run("first", false, make(chan struct{})); code == 0 || len(c.ofType(events.TurnEnd)) != 1 {
				t.Fatal("startup failure was not reported")
			}
		})
	}
}

func TestCodexExpiredCredentialDoesNotStartAProcess(t *testing.T) {
	r, c := testCodexRunner(t, "normal", filepath.Join(t.TempDir(), "session"))
	t.Setenv("GNAT_CODEX_AUTH", string(marshal(CodexCredential{"token", "account", time.Now().Add(-time.Hour).Unix()}, false)))
	if code, _ := r.Run("first", false, make(chan struct{})); code == 0 || r.server != nil || !strings.Contains(str(c.last()["error"]), "expired") {
		t.Fatal("expired credential was not rejected")
	}
}

func TestCodexAcceptsNotificationsBeforeTheTurnResponse(t *testing.T) {
	r, c := testCodexRunner(t, "early-items", filepath.Join(t.TempDir(), "session"))
	runCodex(t, r, "first", false)
	if c.ofType(events.Message)[0]["text"] != "early commentary" {
		t.Fatal("lost early item")
	}
}

func TestCodexIgnoresEventsFromOtherTurnsAndThreads(t *testing.T) {
	r, c := testCodexRunner(t, "wrong-turn", filepath.Join(t.TempDir(), "session"))
	runCodex(t, r, "first", false)
	if strings.Contains(fmt.Sprint(c.all()), "stale reply") || c.last()["reply"] != "first|thread1|model|high" {
		t.Fatal("accepted events from another turn or thread")
	}
}

func TestCodexMalformedSavedSessionNeedsAnExplicitNewThread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session")
	if err := os.WriteFile(path, []byte("invalid session"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, c := testCodexRunner(t, "normal", path)
	if code, _ := r.Run("first", false, make(chan struct{})); code == 0 || r.server != nil || !strings.Contains(str(c.last()["error"]), "--new") {
		t.Fatal("silently discarded an unreadable session")
	}
	runCodex(t, r, "fresh", true)
}

func TestCodexOmittedModelAndEffortUseTheServerDefaults(t *testing.T) {
	r, c := testCodexRunner(t, "normal", filepath.Join(t.TempDir(), "session"))
	r.model, r.effort = "", ""
	runCodex(t, r, "first", false)
	if c.last()["reply"] != "first|thread1||" {
		t.Fatal("did not leave model and effort to Codex")
	}
}

func TestCodexConfiguration(t *testing.T) {
	h := newCodex()
	files := h.ConfigFiles("Build.", "", "")
	if string(files["AGENTS.md"]) != "Build." || !strings.Contains(string(files["config.toml"]), "hooks = true") {
		t.Fatal("missing instructions or hook configuration")
	}
	assertSettings(t, files["hooks.json"], hookSettings())
	if !reflect.DeepEqual(h.StateDirs(), []string{layout.AgentHome + "/.codex"}) {
		t.Fatal("Codex session directory is not persistent")
	}
}

func TestCodexTranslatesToolsReasoningAndReplies(t *testing.T) {
	c := new(collector)
	turn := codexTurn{emit: c.emit, tools: map[string]bool{}, completed: map[string]bool{}, deltas: map[string]string{}}
	update := func(item map[string]any) { turn.update("item/completed", map[string]any{"item": item}) }
	update(map[string]any{"id": "thought", "type": "reasoning", "summary": []any{"Thinking."}})
	update(map[string]any{"id": "patch", "type": "fileChange", "changes": []any{map[string]any{"path": "x.go", "diff": "+x"}}, "status": "failed"})
	update(map[string]any{"id": "mcp", "type": "mcpToolCall", "server": "test", "tool": "read", "arguments": map[string]any{"path": "x.go"}, "result": map[string]any{"content": "x"}, "status": "completed"})
	update(map[string]any{"id": "cmd", "type": "commandExecution", "command": "false", "exitCode": float64(1), "aggregatedOutput": "failed", "status": "completed"})
	update(map[string]any{"id": "comment", "type": "agentMessage", "text": "Working.", "phase": "commentary"})
	if turn.reply != "" {
		t.Fatal("commentary became the reply")
	}
	update(map[string]any{"id": "final", "type": "agentMessage", "text": "Done.", "phase": "final_answer"})
	if turn.reply != "Done." || len(c.ofType(events.Thought)) != 1 {
		t.Fatal("lost final reply or reasoning")
	}
	results := c.ofType(events.ToolResult)
	if len(results) != 3 || results[0]["error"] != true || results[1]["error"] != false || results[2]["error"] != true {
		t.Fatalf("tool results = %v", results)
	}
	for i, call := range c.ofType(events.ToolCall) {
		if call["id"] != results[i]["id"] {
			t.Fatal("tool IDs do not match")
		}
	}
}
