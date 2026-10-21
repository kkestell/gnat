package harness

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kkestell/gnat/internal/events"
	"github.com/kkestell/gnat/internal/layout"
)

// A harness for any agent server that speaks ACP, the Agent Client Protocol.
//
// ACP is JSON-RPC 2.0, one JSON object per line on the server's stdin and stdout. The
// runner keeps one server process for the agent's lifetime, starting another if it exits,
// and continues one session across turns, saved so a restarted daemon loads it.

const (
	acpProtocolVersion = 1
	methodNotFound     = -32601
)

var (
	// acpStateDir holds the session the agent's turns continue, kept across daemon
	// restarts so they resume it.
	acpStateDir = layout.AgentHome + "/.local/state/gnat"
	// acpSessionPath is the file the session's ID is saved in.
	acpSessionPath = acpStateDir + "/acp-session"
)

// stopReasons say why a prompt ended without finishing, for stop reasons other than
// end_turn and cancelled.
var stopReasons = map[string]string{
	"max_tokens":        "the model reached its token limit",
	"max_turn_requests": "the turn reached its request limit",
	"refusal":           "the model refused",
}

// acpError is an error the server reported, or a failure to run it.
type acpError struct{ message string }

func (e *acpError) Error() string { return e.message }

// errEOF means the server has exited.
var errEOF = errors.New("the server's output ended")

// acpHarness is a harness whose agent is an ACP server.
type acpHarness struct {
	info
	// command is the server's command line.
	command []string
}

func (h *acpHarness) Runner(model, effort string, emit Emit) Runner {
	return newACPRunner(h.command, emit, acpSessionPath)
}

// acpServer is a running server process.
type acpServer struct {
	stdin  io.WriteCloser
	stdout *os.File
	reader *bufio.Reader
	// done is closed once the process has exited and its stderr has been read to the end;
	// status is then its exit status.
	done   chan struct{}
	status int
	proc   *os.Process
}

type acpRunner struct {
	command     []string
	emit        Emit
	sessionPath string

	// mu guards prompting and nextID, and makes sending a prompt and noting that it's
	// running one step, so Stop either sees it or the turn sees the stop.
	mu        sync.Mutex
	prompting bool
	nextID    int
	// writeMu guards writes to the server, which Stop makes from another goroutine.
	writeMu sync.Mutex

	server  *acpServer
	session string
	// open is whether the session is open in the running server; a new server loads it.
	open bool
	// cost is what the session has cost so far, in US dollars, as the server last reported.
	cost float64
}

func newACPRunner(command []string, emit Emit, sessionPath string) *acpRunner {
	r := &acpRunner{command: command, emit: emit, sessionPath: sessionPath}
	if saved, err := os.ReadFile(sessionPath); err == nil {
		r.session = strings.TrimSpace(string(saved))
	}
	return r
}

func (r *acpRunner) Run(text string, new bool, stop <-chan struct{}) (int, error) {
	turn := newTurn(r.emit)
	result, err := r.prompt(text, new, stop, turn)
	r.mu.Lock()
	r.prompting = false
	r.mu.Unlock()
	var acpErr *acpError
	switch {
	case errors.As(err, &acpErr):
		turn.end(acpErr.message, true)
		return 1, nil
	case errors.Is(err, errEOF):
		<-r.server.done
		code := r.server.status
		turn.flush()
		r.emit(events.NewError(fmt.Sprintf("%s exited with status %d", r.command[0], code)))
		if code == 0 {
			code = 1
		}
		return code, nil
	case err != nil:
		// The server may still be running the turn; the next one starts a new server.
		if r.server != nil {
			r.server.kill()
		}
		return 1, err
	}
	if result == nil {
		return 1, nil // stopped before the prompt was sent
	}
	if usage, ok := result["usage"].(map[string]any); ok {
		// The turn's tokens; its cost comes from the usage updates.
		r.emit(events.NewUsage(events.UsageTotals{
			InputTokens: count(usage["inputTokens"]), CacheReadTokens: count(usage["cachedReadTokens"]),
			CacheWriteTokens: count(usage["cachedWriteTokens"]), OutputTokens: count(usage["outputTokens"]),
		}))
	}
	reason := str(result["stopReason"])
	switch reason {
	case "cancelled":
		turn.flush()
		return 1, nil
	case "end_turn":
		turn.end("", false)
		return 0, nil
	}
	explanation, ok := stopReasons[reason]
	if !ok {
		explanation = "the turn ended: " + reason
	}
	turn.end(explanation, true)
	return 1, nil
}

func (r *acpRunner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.prompting {
		r.cancel()
	}
}

// prompt returns the prompt's result, or nil if a stop came before it was sent.
func (r *acpRunner) prompt(text string, new bool, stop <-chan struct{}, turn *turn) (map[string]any, error) {
	if r.server == nil || r.server.exited() {
		if err := r.start(); err != nil {
			return nil, err
		}
	}
	switch {
	case new || r.session == "":
		if err := r.newSession(); err != nil {
			return nil, err
		}
	case !r.open:
		r.cost = 0
		// Loading replays the session as updates, which the logs already have.
		_, err := r.request("session/load", r.sessionParams(false))
		var acpErr *acpError
		if errors.As(err, &acpErr) {
			r.emit(events.NewError(fmt.Sprintf("can't resume session %s: %v; starting a new one", r.session, err)))
			if err := r.newSession(); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		} else {
			r.open = true
		}
	}
	r.mu.Lock()
	select {
	case <-stop:
		r.mu.Unlock()
		return nil, nil
	default:
	}
	id, err := r.sendRequest("session/prompt", map[string]any{
		"sessionId": r.session,
		"prompt":    []any{map[string]any{"type": "text", "text": text}},
	})
	if err == nil {
		r.prompting = true
	}
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return r.response(id, "session/prompt", turn.update)
}

// cancel asks the server to end the running prompt, which then finishes as cancelled.
func (r *acpRunner) cancel() {
	// If the server has exited, that ends the turn anyway.
	_ = r.send(map[string]any{
		"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": r.session},
	})
}

func (r *acpRunner) start() error {
	if old := r.server; old != nil {
		// The last server exited; its stderr closed once it was read to the end.
		old.stdout.Close()
		old.stdin.Close()
	}
	r.server = nil
	cmd := exec.Command(r.command[0], r.command[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, in, err := os.Pipe()
	if err != nil {
		return err
	}
	stderr := &lineWriter{emit: r.emit}
	cmd.Stdout, cmd.Stderr = in, stderr
	// A grandchild holding stderr open doesn't keep Wait from returning for long.
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Start()
	in.Close()
	if err != nil {
		out.Close()
		return &acpError{fmt.Sprintf("can't run %s: %v", r.command[0], err)}
	}
	server := &acpServer{
		stdin: stdin, stdout: out, reader: bufio.NewReaderSize(out, 64*1024),
		done: make(chan struct{}), proc: cmd.Process,
	}
	go func() {
		err := cmd.Wait()
		server.status = exitStatus(cmd, err)
		stderr.flush()
		close(server.done)
	}()
	r.server = server
	r.open = false
	_, err = r.request("initialize", map[string]any{
		"protocolVersion": acpProtocolVersion, "clientCapabilities": map[string]any{},
	})
	return err
}

func (s *acpServer) exited() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *acpServer) kill() {
	_ = s.proc.Kill()
	<-s.done
}

// lineWriter emits each non-blank line written to it as an error event.
type lineWriter struct {
	emit Emit
	buf  []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		w.line(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
}

func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		w.line(string(w.buf))
		w.buf = nil
	}
}

func (w *lineWriter) line(line string) {
	if strings.TrimSpace(line) != "" {
		w.emit(events.NewError(strings.TrimSuffix(line, "\r")))
	}
}

func (r *acpRunner) newSession() error {
	if r.open {
		// Stops the old session's background processes; it stays saved.
		_, err := r.request("session/close", map[string]any{"sessionId": r.session})
		var acpErr *acpError
		if err != nil && !errors.As(err, &acpErr) {
			return err
		}
		r.open = false
	}
	r.cost = 0
	result, err := r.request("session/new", r.sessionParams(true))
	if err != nil {
		return err
	}
	session, ok := result["sessionId"].(string)
	if !ok || session == "" {
		return &acpError{"session/new: the server gave no session ID"}
	}
	r.session = session
	if err := os.MkdirAll(filepath.Dir(r.sessionPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(r.sessionPath, []byte(session+"\n"), 0o644); err != nil {
		return err
	}
	r.open = true
	return nil
}

func (r *acpRunner) sessionParams(new bool) map[string]any {
	params := map[string]any{"cwd": layout.ContainerWorkspace, "mcpServers": []any{}}
	if !new {
		params["sessionId"] = r.session
	}
	return params
}

// send sends a message; errEOF if the server has exited.
func (r *acpRunner) send(message map[string]any) error {
	data := append(marshal(message, false), '\n')
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if _, err := r.server.stdin.Write(data); err != nil {
		return errEOF
	}
	return nil
}

// sendRequest sends a request and returns its ID. The caller holds r.mu or is the only
// goroutine that sends requests.
func (r *acpRunner) sendRequest(method string, params map[string]any) (int, error) {
	r.nextID++
	id := r.nextID
	return id, r.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func (r *acpRunner) request(method string, params map[string]any) (map[string]any, error) {
	r.mu.Lock()
	id, err := r.sendRequest(method, params)
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return r.response(id, method, nil)
}

// response reads until the request's response, handling what the server sends meanwhile.
//
// Session updates go to onUpdate; without one, as while a session loads, they're dropped.
func (r *acpRunner) response(id int, method string, onUpdate func(map[string]any)) (map[string]any, error) {
	for {
		line, readErr := r.server.reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var message map[string]any
			if err := unmarshal(line, &message); err != nil || message == nil {
				r.emit(events.NewError(strings.TrimSuffix(strings.ToValidUTF8(string(line), "�"), "\n")))
			} else if result, done, err := r.handle(message, id, method, onUpdate); done {
				return result, err
			}
		}
		if readErr != nil {
			return nil, errEOF
		}
	}
}

// handle deals with one message from the server; done is set when it's the response
// to the request with that ID.
func (r *acpRunner) handle(message map[string]any, id int, method string,
	onUpdate func(map[string]any)) (result map[string]any, done bool, err error) {
	_, isCall := message["method"]
	if message["id"] == float64(id) && !isCall {
		if rpcErr, ok := message["error"]; ok {
			fields, _ := rpcErr.(map[string]any)
			data := ""
			switch d := fields["data"].(type) {
			case nil:
			case string:
				if d != "" {
					data = " (" + d + ")"
				}
			default:
				data = " (" + string(marshal(d, false)) + ")"
			}
			return nil, true, &acpError{fmt.Sprintf("%s: %s%s", method, str(fields["message"]), data)}
		}
		result, _ = message["result"].(map[string]any)
		if result == nil {
			result = map[string]any{}
		}
		return result, true, nil
	}
	_, hasID := message["id"]
	switch {
	case message["method"] == "session/update":
		params, _ := message["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] == "usage_update" {
			r.usage(update, onUpdate != nil)
		} else if onUpdate != nil {
			onUpdate(update)
		}
	case message["method"] == "session/request_permission":
		// The container is the sandbox, so allow whatever the agent asks to do.
		params, _ := message["params"].(map[string]any)
		_ = r.send(map[string]any{"jsonrpc": "2.0", "id": message["id"],
			"result": map[string]any{"outcome": allow(params)}})
	case isCall && hasID:
		_ = r.send(map[string]any{"jsonrpc": "2.0", "id": message["id"],
			"error": map[string]any{"code": methodNotFound, "message": "Method not found"}})
	}
	return nil, false, nil
}

// usage notes the session's cost so far, which the server reports as it grows; when
// report is set, as during a prompt, it emits what it grew by.
func (r *acpRunner) usage(update map[string]any, report bool) {
	cost, _ := update["cost"].(map[string]any)
	if cost["currency"] != "USD" {
		return
	}
	amount, ok := cost["amount"].(float64)
	if !ok {
		return
	}
	if report && amount > r.cost {
		r.emit(events.NewUsage(events.UsageTotals{Cost: ptr(amount - r.cost)}))
	}
	r.cost = amount
}

// allow is the outcome of a permission request that picks its allow option.
func allow(params map[string]any) map[string]any {
	options, _ := params["options"].([]any)
	for _, kind := range []string{"allow_once", "allow_always"} {
		for _, option := range options {
			if option, ok := option.(map[string]any); ok && option["kind"] == kind {
				return map[string]any{"outcome": "selected", "optionId": option["optionId"]}
			}
		}
	}
	return map[string]any{"outcome": "cancelled"}
}

// turn translates one prompt's session updates into gnat's events.
//
// Text and thoughts arrive in chunks, which are joined into one event per block: a block
// ends when the other kind starts, at a tool call, and at the end of the turn.
type turn struct {
	emit   Emit
	kind   string
	chunks []string
	// reply is the assistant's text since the last tool call: its reply, once the turn ends.
	reply []string
}

func newTurn(emit Emit) *turn { return &turn{emit: emit} }

func (t *turn) update(update map[string]any) {
	switch update["sessionUpdate"] {
	case "agent_message_chunk":
		t.chunk(events.Message, update["content"])
	case "agent_thought_chunk":
		t.chunk(events.Thought, update["content"])
	case "tool_call":
		t.flush()
		t.reply = nil
		var input map[string]any
		switch raw := update["rawInput"].(type) {
		case map[string]any:
			input = raw
		case nil:
			input = map[string]any{}
		default:
			input = map[string]any{"input": raw}
		}
		// The tool's name is an extension some servers add, such as Ox; calls without
		// one, such as Ox's compactions, still have a title.
		name := firstNonEmpty(str(update["name"]), str(update["title"]), str(update["kind"]), "tool")
		t.emit(events.NewToolCall(str(update["toolCallId"]), name, input, str(update["title"])))
		t.result(update)
	case "tool_call_update":
		t.result(update)
	}
}

func (t *turn) chunk(kind string, content any) {
	content2, _ := content.(map[string]any)
	if content2["type"] != "text" {
		return
	}
	if kind != t.kind {
		t.flush()
		t.kind = kind
	}
	t.chunks = append(t.chunks, str(content2["text"]))
}

func (t *turn) result(update map[string]any) {
	status := update["status"]
	if status == "completed" || status == "failed" {
		t.emit(events.NewToolResult(str(update["toolCallId"]), toolCallOutput(update), status == "failed"))
	}
}

func (t *turn) flush() {
	text := strings.TrimSpace(strings.Join(t.chunks, ""))
	if text != "" && t.kind == events.Message {
		t.emit(events.NewMessage(text))
		t.reply = append(t.reply, text)
	} else if text != "" {
		t.emit(events.NewThought(text))
	}
	t.kind, t.chunks = "", nil
}

// end emits the rest of the text and the turn's end, with its reply and, if failed, why.
func (t *turn) end(reason string, failed bool) {
	t.flush()
	if !failed {
		reason = ""
	}
	t.emit(events.NewTurnEnd(strings.Join(t.reply, "\n\n"), reason))
}

func toolCallOutput(update map[string]any) string {
	raw := update["rawOutput"]
	if s, ok := raw.(string); ok {
		return s
	}
	var parts []string
	items, _ := update["content"].([]any)
	for _, item := range items {
		item, _ := item.(map[string]any)
		switch item["type"] {
		case "content":
			inner, _ := item["content"].(map[string]any)
			if inner["type"] == "text" {
				parts = append(parts, str(inner["text"]))
			} else {
				parts = append(parts, fmt.Sprintf("[%s]", str(inner["type"])))
			}
		case "diff":
			parts = append(parts, fmt.Sprintf("[diff of %s]", str(item["path"])))
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	if raw == nil {
		return ""
	}
	return string(marshal(raw, false))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
