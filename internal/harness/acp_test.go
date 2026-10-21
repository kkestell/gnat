package harness

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/events"
)

// fakeACPServer stands in for an ACP server: it answers each request as the SCRIPT
// environment variable says, by method, and logs every message it receives to LOG.
func fakeACPServer() {
	var script map[string]any
	if err := json.Unmarshal([]byte(os.Getenv("SCRIPT")), &script); err != nil {
		panic(err)
	}
	logFile, err := os.OpenFile(os.Getenv("LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	os.Stderr.WriteString("server starting\n")
	stdin := bufio.NewReader(os.Stdin)
	send := func(message any) {
		out, _ := json.Marshal(message)
		os.Stdout.Write(append(out, '\n'))
	}
	receive := func() map[string]any {
		line, _ := stdin.ReadString('\n')
		if line == "" {
			return nil
		}
		logFile.WriteString(line)
		var message map[string]any
		_ = json.Unmarshal([]byte(line), &message)
		return message
	}
	for message := receive(); message != nil; message = receive() {
		method, _ := message["method"].(string)
		if _, ok := message["id"]; !ok || method == "" {
			continue
		}
		if updates, ok := script[method+":updates"].([]any); ok {
			for _, update := range updates {
				send(map[string]any{"jsonrpc": "2.0", "method": "session/update",
					"params": map[string]any{"sessionId": "s", "update": update}})
			}
		}
		if _, ok := script[method+":permission"]; ok {
			send(map[string]any{"jsonrpc": "2.0", "id": "p", "method": "session/request_permission",
				"params": map[string]any{"options": []any{
					map[string]any{"optionId": "deny", "kind": "reject_once"},
					map[string]any{"optionId": "approve", "kind": "allow_once"}}}})
			receive()
			send(map[string]any{"jsonrpc": "2.0", "id": "q", "method": "fs/read_text_file", "params": map[string]any{}})
			receive()
			os.Stdout.WriteString("not json\n")
		}
		if _, ok := script[method+":cancellable"]; ok {
			for cancel := receive(); cancel != nil && cancel["method"] != "session/cancel"; cancel = receive() {
			}
			send(map[string]any{"jsonrpc": "2.0", "id": message["id"], "result": map[string]any{"stopReason": "cancelled"}})
			continue
		}
		if code, ok := script[method+":exit"].(float64); ok {
			os.Exit(int(code))
		}
		if reason, ok := script[method+":error"]; ok {
			send(map[string]any{"jsonrpc": "2.0", "id": message["id"],
				"error": map[string]any{"code": -32002, "message": reason}})
		} else {
			result := script[method]
			if result == nil {
				result = map[string]any{}
			}
			send(map[string]any{"jsonrpc": "2.0", "id": message["id"], "result": result})
		}
	}
}

func text(content string, kind ...string) m {
	update := "agent_message_chunk"
	if len(kind) > 0 {
		update = kind[0]
	}
	return m{"sessionUpdate": update, "content": m{"type": "text", "text": content}}
}

func TestACPUpdatesBecomeEvents(t *testing.T) {
	c := &collector{}
	turn := newTurn(c.emit)
	for _, update := range []m{
		text("Thinking ", "agent_thought_chunk"), text("hard.", "agent_thought_chunk"),
		text("Let me "), text("look."),
		{"sessionUpdate": "tool_call", "toolCallId": "t1", "name": "shell", "title": "Run ls",
			"kind": "execute", "rawInput": m{"command": "ls"}},
		{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "in_progress"},
		{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "failed",
			"rawOutput": "no such file"},
		{"sessionUpdate": "tool_call", "toolCallId": "c1", "title": "Context compaction",
			"status": "in_progress"},
		{"sessionUpdate": "tool_call_update", "toolCallId": "c1", "status": "completed",
			"content": []any{m{"type": "content", "content": m{"type": "text", "text": "summary"}}}},
		{"sessionUpdate": "usage_update", "used": 1, "size": 2},
		text("Done"), text("."),
	} {
		turn.update(update)
	}
	turn.end("", false)
	assertEvents(t, c.all(), []events.Event{
		events.NewThought("Thinking hard."),
		events.NewMessage("Let me look."),
		events.NewToolCall("t1", "shell", m{"command": "ls"}, "Run ls"),
		events.NewToolResult("t1", "no such file", true),
		events.NewToolCall("c1", "Context compaction", m{}, "Context compaction"),
		events.NewToolResult("c1", "summary", false),
		events.NewMessage("Done."),
		events.NewTurnEnd("Done.", ""),
	})
}

func TestACPATurnThatEndsWithAToolCallHasNoReply(t *testing.T) {
	c := &collector{}
	turn := newTurn(c.emit)
	turn.update(text("Let me look."))
	turn.update(m{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "Read", "rawInput": "not json"})
	turn.end("the model refused", true)
	all := c.all()
	assertEvents(t, all[1:], []events.Event{
		events.NewToolCall("t1", "Read", m{"input": "not json"}, "Read"),
		events.NewTurnEnd("", "the model refused"),
	})
}

// acpFixture is a runner on the fake server, with the events it emits.
type acpFixture struct {
	t           *testing.T
	runner      *acpRunner
	events      *collector
	sessionPath string
	logPath     string
}

// newACPFixture makes a runner whose server answers as script says, on top of the
// defaults for a new session and a prompt that ends its turn.
func newACPFixture(t *testing.T, script map[string]any) *acpFixture {
	t.Helper()
	dir := t.TempDir()
	full := m{"session/new": m{"sessionId": "s1"}, "session/prompt": m{"stopReason": "end_turn"}}
	for k, v := range script {
		full[k] = v
	}
	encoded, _ := json.Marshal(full)
	f := &acpFixture{
		t: t, events: &collector{},
		sessionPath: filepath.Join(dir, "state", "acp-session"), logPath: filepath.Join(dir, "log"),
	}
	t.Setenv("GNAT_FAKE_ACP", "1")
	t.Setenv("SCRIPT", string(encoded))
	t.Setenv("LOG", f.logPath)
	f.runner = newACPRunner([]string{os.Args[0], "acp"}, f.events.emit, f.sessionPath)
	t.Cleanup(func() {
		if f.runner.server != nil {
			f.runner.server.kill()
		}
	})
	return f
}

// saveSession writes the session a previous daemon left behind.
func (f *acpFixture) saveSession(id string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.sessionPath), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.sessionPath, []byte(id+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.runner.session = id
}

func (f *acpFixture) run(text string, new bool) int {
	f.t.Helper()
	code, err := f.runner.Run(text, new, never())
	if err != nil {
		f.t.Fatal(err)
	}
	return code
}

func (f *acpFixture) received() []map[string]any {
	f.t.Helper()
	data, err := os.ReadFile(f.logPath)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, message)
	}
	return out
}

func (f *acpFixture) methods() []string {
	var out []string
	for _, message := range f.received() {
		method, _ := message["method"].(string)
		out = append(out, method)
	}
	return out
}

func (f *acpFixture) message(method string) map[string]any {
	f.t.Helper()
	for _, message := range f.received() {
		if message["method"] == method {
			return message
		}
	}
	f.t.Fatalf("the server got no %s", method)
	return nil
}

func equal(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		t.Fatalf("got %s, want %s", g, w)
	}
}

func TestACPATurn(t *testing.T) {
	f := newACPFixture(t, m{"session/prompt:updates": []any{text("Hi "), text("there.")}})
	equal(t, f.run("hello", false), 0)
	all := f.events.all()
	assertEvents(t, all[len(all)-2:], []events.Event{events.NewMessage("Hi there."), events.NewTurnEnd("Hi there.", "")})
	equal(t, f.message("session/prompt")["params"],
		m{"sessionId": "s1", "prompt": []any{m{"type": "text", "text": "hello"}}})
}

func TestACPServerStderrIsForwardedAsErrors(t *testing.T) {
	f := newACPFixture(t, nil)
	f.run("hello", false)
	deadline := time.Now().Add(5 * time.Second)
	for !f.events.contains(events.NewError("server starting")) {
		if time.Now().After(deadline) {
			t.Fatal("the server's stderr wasn't forwarded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestACPTurnsContinueOneSessionUntilANewOneIsAskedFor(t *testing.T) {
	f := newACPFixture(t, nil)
	for _, turn := range []struct {
		text string
		new  bool
	}{{"one", false}, {"two", false}, {"three", true}} {
		f.run(turn.text, turn.new)
	}
	equal(t, f.methods(), []string{"initialize", "session/new", "session/prompt", "session/prompt",
		"session/close", "session/new", "session/prompt"})
	equal(t, f.received()[1]["params"], m{"cwd": "/workspace", "mcpServers": []any{}})
	saved, _ := os.ReadFile(f.sessionPath)
	equal(t, string(saved), "s1\n")
}

func TestACPARestartedDaemonLoadsTheSavedSession(t *testing.T) {
	f := newACPFixture(t, m{"session/load:updates": []any{text("replayed")}})
	f.saveSession("s0")
	f.runner = newACPRunner(f.runner.command, f.events.emit, f.sessionPath)
	equal(t, f.run("again", false), 0)
	equal(t, f.methods(), []string{"initialize", "session/load", "session/prompt"})
	equal(t, f.received()[1]["params"], m{"sessionId": "s0", "cwd": "/workspace", "mcpServers": []any{}})
	if f.events.contains(events.NewMessage("replayed")) {
		t.Error("the replayed session was emitted")
	}
}

func TestACPASessionThatWontLoadIsReplaced(t *testing.T) {
	f := newACPFixture(t, m{"session/load:error": "Resource not found"})
	f.saveSession("gone")
	equal(t, f.run("again", false), 0)
	found := false
	for _, e := range f.events.ofType("error") {
		found = found || strings.Contains(e["text"].(string), "can't resume session gone")
	}
	if !found {
		t.Errorf("no error said the session couldn't be resumed: %v", f.events.all())
	}
	equal(t, f.methods(), []string{"initialize", "session/load", "session/new", "session/prompt"})
	saved, _ := os.ReadFile(f.sessionPath)
	equal(t, string(saved), "s1\n")
}

func cost(amount float64, currency ...string) m {
	unit := "USD"
	if len(currency) > 0 {
		unit = currency[0]
	}
	return m{"sessionUpdate": "usage_update", "used": 1000, "size": 200000,
		"cost": m{"amount": amount, "currency": unit}}
}

func TestACPUsageIsWhatTheSessionsCostGrewBy(t *testing.T) {
	f := newACPFixture(t, m{"session/prompt:updates": []any{
		text("Hi."), cost(0.25), cost(0.25), cost(0.75), cost(9, "EUR")}})
	f.run("one", false)
	assertEvents(t, f.events.ofType("usage"), []events.Event{
		usageEvent(nil, nil, nil, nil, ptr(0.25)), usageEvent(nil, nil, nil, nil, ptr(0.5))})
}

func TestACPAPromptsResponseReportsItsTokens(t *testing.T) {
	f := newACPFixture(t, m{"session/prompt": m{"stopReason": "end_turn", "usage": m{
		"inputTokens": 40, "cachedReadTokens": 60, "outputTokens": 20, "thoughtTokens": 5,
		"totalTokens": 120}}})
	f.run("one", false)
	all := f.events.all()
	assertEvents(t, all[len(all)-2:], []events.Event{
		usageEvent(n(40), n(60), nil, n(20), nil), events.NewTurnEnd("", "")})
}

func TestACPALoadedSessionsCostSoFarIsntUsage(t *testing.T) {
	f := newACPFixture(t, m{"session/load:updates": []any{cost(2.0)},
		"session/prompt:updates": []any{cost(2.5)}})
	f.saveSession("s0")
	f.run("again", false)
	assertEvents(t, f.events.ofType("usage"), []events.Event{usageEvent(nil, nil, nil, nil, ptr(0.5))})
}

func TestACPPermissionIsGrantedAndOtherRequestsRefused(t *testing.T) {
	f := newACPFixture(t, m{"session/prompt:permission": true})
	equal(t, f.run("hello", false), 0)
	replies := map[any]map[string]any{}
	for _, message := range f.received() {
		if id := message["id"]; id == "p" || id == "q" {
			replies[id] = message
		}
	}
	equal(t, replies["p"]["result"], m{"outcome": m{"outcome": "selected", "optionId": "approve"}})
	equal(t, replies["q"]["error"].(map[string]any)["code"], float64(methodNotFound))
	if !f.events.contains(events.NewError("not json")) {
		t.Error("output that isn't JSON wasn't reported as an error")
	}
}

func TestACPStopReasons(t *testing.T) {
	f := newACPFixture(t, m{"session/prompt": m{"stopReason": "max_tokens"}})
	equal(t, f.run("hello", false), 1)
	equal(t, f.events.last(), events.NewTurnEnd("", "the model reached its token limit"))
}

func TestACPAFailedPromptFailsTheTurn(t *testing.T) {
	f := newACPFixture(t, m{"session/prompt:error": "Internal error"})
	equal(t, f.run("hello", false), 1)
	equal(t, f.events.last(), events.NewTurnEnd("", "session/prompt: Internal error"))
}

func TestACPAServerThatExitsFailsTheTurnAndIsRestarted(t *testing.T) {
	f := newACPFixture(t, m{"session/prompt:exit": 3})
	equal(t, f.run("hello", false), 3)
	if !f.events.contains(events.NewError(os.Args[0] + " exited with status 3")) {
		t.Errorf("no error said the server exited: %v", f.events.all())
	}
	equal(t, f.run("again", false), 3)
	// The new server loads the session the old one had open.
	equal(t, f.methods(), []string{"initialize", "session/new", "session/prompt",
		"initialize", "session/load", "session/prompt"})
}

func TestACPStopCancelsThePrompt(t *testing.T) {
	f := newACPFixture(t, m{"session/prompt:cancellable": true,
		"session/prompt:updates": []any{text("partial")}})
	stop := make(chan struct{})
	time.AfterFunc(200*time.Millisecond, func() { close(stop); f.runner.Stop() })
	code, err := f.runner.Run("hello", false, stop)
	equal(t, []any{code, err}, []any{1, nil})
	received := f.received()
	equal(t, received[len(received)-1],
		m{"jsonrpc": "2.0", "method": "session/cancel", "params": m{"sessionId": "s1"}})
	if !f.events.contains(events.NewMessage("partial")) {
		t.Error("the partial message was lost")
	}
	if len(f.events.ofType("turn_end")) != 0 {
		t.Error("a cancelled turn ended")
	}
}

func TestACPAStopBeforeThePromptIsSentSkipsIt(t *testing.T) {
	f := newACPFixture(t, nil)
	stop := make(chan struct{})
	close(stop)
	code, err := f.runner.Run("hello", false, stop)
	equal(t, []any{code, err}, []any{1, nil})
	for _, method := range f.methods() {
		if method == "session/prompt" {
			t.Error("the prompt was sent")
		}
	}
}

func TestACPMissingProgram(t *testing.T) {
	f := newACPFixture(t, nil)
	f.runner.command = []string{"/nonexistent/acp"}
	equal(t, f.run("hello", false), 1)
	reason, _ := f.events.last()["error"].(string)
	if !strings.HasPrefix(reason, "can't run /nonexistent/acp") {
		t.Errorf("turn end = %v", f.events.last())
	}
}
