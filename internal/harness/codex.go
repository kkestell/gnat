package harness

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kkestell/gnat/internal/events"
	"github.com/kkestell/gnat/internal/layout"
)

const codexRenew = "renew with `gnat auth login codex`, then run `gnat down SWARM` and `gnat up SWARM`"

// CodexCredential is a shared ChatGPT access snapshot, with no refresh token.
type CodexCredential struct {
	AccessToken string `json:"access_token"`
	AccountID   string `json:"account_id"`
	ExpiresAt   int64  `json:"expires_at"`
}

func (c CodexCredential) Validate() error {
	if c.AccessToken == "" || c.AccountID == "" || c.ExpiresAt == 0 {
		return errors.New("invalid Codex ChatGPT credential; " + codexRenew)
	}
	if c.ExpiresAt <= time.Now().Unix() {
		return errors.New("codex ChatGPT credential expired; " + codexRenew)
	}
	return nil
}

type codex struct{ info }

func newCodex() *codex {
	home := layout.AgentHome + "/.codex"
	return &codex{info{
		name: "codex", program: "codex", credential: "GNAT_CODEX_AUTH",
		credentialName: "Codex ChatGPT credential", credentialSource: "install the Codex CLI",
		efforts:   []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"},
		configDir: home, stateDirs: []string{home},
	}}
}

func (*codex) ConfigFiles(instructions, model, effort string) map[string][]byte {
	return map[string][]byte{
		"AGENTS.md":   []byte(instructions),
		"config.toml": []byte("cli_auth_credentials_store = \"ephemeral\"\n[features]\nhooks = true\n"),
		"hooks.json":  jsonBytes(hookSettings()),
	}
}

func (h *codex) Runner(model, effort string, emit Emit) Runner {
	return newCodexRunner(model, effort, emit, h.ConfigDir()+"/gnat-session.json")
}

type codexUsage struct {
	Input, Cached, Written, Output int64
}

type codexSession struct {
	Thread string     `json:"thread"`
	Usage  codexUsage `json:"usage"`
}

type codexRPC struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params map[string]any  `json:"params"`
	Result map[string]any  `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type codexRead struct {
	message codexRPC
	err     error
}

type codexServer struct {
	proc   *os.Process
	stdin  io.WriteCloser
	stdout *os.File
	lines  chan codexRead
	closed chan struct{}
	done   chan struct{}
}

func (s *codexServer) close() {
	close(s.closed)
	killGroup(s.proc)
	s.stdin.Close()
	s.stdout.Close()
	<-s.done
}

type codexRunner struct {
	command       []string
	model, effort string
	emit          Emit
	sessionPath   string
	session       codexSession
	loadErr       error
	server        *codexServer
	open          bool
	nextID        int
	pending       []codexRPC
	credential    CodexCredential
	requestWait   time.Duration
	stopWait      time.Duration
	// Only the turn goroutine touches the server. Stop signals that specific turn.
	mu     sync.Mutex
	signal chan struct{}
}

func newCodexRunner(model, effort string, emit Emit, sessionPath string) *codexRunner {
	r := &codexRunner{
		command: []string{"codex", "app-server"},
		model:   model, effort: effort, emit: emit, sessionPath: sessionPath,
		requestWait: 30 * time.Second, stopWait: 5 * time.Second,
	}
	data, err := os.ReadFile(sessionPath)
	if err == nil {
		if json.Unmarshal(data, &r.session) != nil || r.session.Thread == "" {
			r.loadErr = errors.New("invalid saved Codex session; use --new to start a fresh conversation")
		}
	} else if !os.IsNotExist(err) {
		r.loadErr = errors.New("can't read the saved Codex session")
	}
	return r
}

func (r *codexRunner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.signal != nil {
		select {
		case r.signal <- struct{}{}:
		default:
		}
	}
}

var errCodexStopped = errors.New("codex turn stopped")

func (r *codexRunner) Run(text string, fresh bool, stop <-chan struct{}) (int, error) {
	signal := make(chan struct{}, 1)
	r.mu.Lock()
	r.signal = signal
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.signal = nil
		r.mu.Unlock()
	}()
	code, err := r.run(text, fresh, stop, signal)
	if err != nil {
		r.shutdown()
		if !errors.Is(err, errCodexStopped) {
			r.emit(events.NewTurnEnd("", r.safe(err.Error())))
		}
		return 1, nil
	}
	return code, nil
}

func (r *codexRunner) run(text string, fresh bool, stop, signal <-chan struct{}) (int, error) {
	select {
	case <-stop:
		return 1, errCodexStopped
	case <-signal:
		return 1, errCodexStopped
	default:
	}
	if json.Unmarshal([]byte(os.Getenv("GNAT_CODEX_AUTH")), &r.credential) != nil {
		return 1, errors.New("invalid Codex ChatGPT credential; " + codexRenew)
	}
	if err := r.credential.Validate(); err != nil {
		return 1, err
	}
	if fresh {
		r.loadErr = nil
	}
	if r.loadErr != nil {
		return 1, r.loadErr
	}
	if r.server != nil {
		select {
		case <-r.server.done:
			r.shutdown()
		default:
		}
	}
	if r.server == nil {
		if err := r.start(stop, signal); err != nil {
			return 1, err
		}
	}
	if fresh || r.session.Thread == "" || !r.open {
		method := "thread/start"
		params := map[string]any{"cwd": layout.ContainerWorkspace, "approvalPolicy": "never", "sandbox": "danger-full-access"}
		if r.model != "" {
			params["model"] = r.model
		}
		if !fresh && r.session.Thread != "" {
			method = "thread/resume"
			params["threadId"] = r.session.Thread
		}
		result, err := r.request(method, params, stop, signal)
		if err != nil {
			return 1, err
		}
		thread, _ := result["thread"].(map[string]any)
		id := str(thread["id"])
		if id == "" || (method == "thread/resume" && id != r.session.Thread) {
			return 1, errors.New("codex returned an unexpected thread")
		}
		if method == "thread/start" {
			r.session = codexSession{Thread: id}
		}
		if err := r.save(); err != nil {
			return 1, err
		}
		r.open = true
	}
	params := map[string]any{"threadId": r.session.Thread, "input": []any{map[string]any{"type": "text", "text": text}}}
	if r.effort != "" {
		params["effort"] = r.effort
	}
	result, err := r.request("turn/start", params, stop, signal)
	if err != nil {
		return 1, err
	}
	turn, _ := result["turn"].(map[string]any)
	id := str(turn["id"])
	if id == "" {
		return 1, errors.New("codex returned no turn ID")
	}
	t := codexTurn{emit: r.emit, tools: map[string]bool{}, completed: map[string]bool{}, deltas: map[string]string{}}
	var deadline time.Time
	stopping := false
	for {
		var message codexRPC
		var err error
		if len(r.pending) > 0 {
			message, r.pending = r.pending[0], r.pending[1:]
		} else {
			message, err = r.receive(deadline, stop, signal)
		}
		if errors.Is(err, errCodexStopped) && !stopping {
			if _, err := r.sendRequest("turn/interrupt", map[string]any{"threadId": r.session.Thread, "turnId": id}); err != nil {
				return 1, errCodexStopped
			}
			stopping, stop, signal = true, nil, nil
			deadline = time.Now().Add(r.stopWait)
			continue
		}
		if err != nil {
			if stopping {
				return 1, errCodexStopped
			}
			return 1, err
		}
		if err := r.serverRequest(message); err != nil {
			return 1, err
		}
		p := message.Params
		turnID := str(p["turnId"])
		if message.Method == "turn/completed" {
			turn, _ := p["turn"].(map[string]any)
			turnID = str(turn["id"])
		}
		if str(p["threadId"]) != r.session.Thread || turnID != id {
			continue
		}
		switch message.Method {
		case "turn/completed":
			turn, _ := p["turn"].(map[string]any)
			if str(turn["id"]) != id {
				continue
			}
			if stopping || turn["status"] == "interrupted" {
				return 1, nil
			}
			if turn["status"] != "completed" {
				failure, _ := turn["error"].(map[string]any)
				return 1, fmt.Errorf("codex turn failed: %s", r.safe(str(failure["message"])))
			}
			r.emit(events.NewTurnEnd(t.reply, ""))
			return 0, nil
		case "thread/tokenUsage/updated":
			if err := r.usage(p); err != nil {
				return 1, err
			}
		default:
			t.update(message.Method, p)
		}
	}
}

func (r *codexRunner) start(stop, signal <-chan struct{}) error {
	cmd := exec.Command(r.command[0], r.command[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, writer, err := os.Pipe()
	if err != nil {
		in.Close()
		return err
	}
	cmd.Stdout, cmd.Stderr = writer, io.Discard
	if err := cmd.Start(); err != nil {
		in.Close()
		out.Close()
		writer.Close()
		return errors.New("can't start codex app-server; check that the image has the supported Codex release")
	}
	writer.Close()
	s := &codexServer{proc: cmd.Process, stdin: in, stdout: out, lines: make(chan codexRead, 32), closed: make(chan struct{}), done: make(chan struct{})}
	r.server = s
	go func() { _ = cmd.Wait(); close(s.done) }()
	go func() {
		defer close(s.lines)
		reader := bufio.NewReader(out)
		for {
			line, err := reader.ReadBytes('\n')
			var msg codexRPC
			if len(line) > 0 {
				if json.Unmarshal(line, &msg) != nil {
					err = errors.New("invalid JSON from codex app-server")
				} else if errors.Is(err, io.EOF) {
					err = nil
				}
			}
			select {
			case s.lines <- codexRead{msg, err}:
			case <-s.closed:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	if _, err := r.request("initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "gnat", "version": "0.1.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, stop, signal); err != nil {
		return err
	}
	if err := r.send(map[string]any{"method": "initialized"}); err != nil {
		return err
	}
	if err := r.trustHook(stop, signal); err != nil {
		return err
	}
	_, err = r.request("account/login/start", map[string]any{
		"type": "chatgptAuthTokens", "accessToken": r.credential.AccessToken, "chatgptAccountId": r.credential.AccountID,
	}, stop, signal)
	return err
}

// The CLI's hook-trust bypass is not applied to app-server in the pinned release.
// Trust only the hook gnat installed, using Codex's own definition hash.
func (r *codexRunner) trustHook(stop, signal <-chan struct{}) error {
	result, err := r.request("hooks/list", map[string]any{"cwds": []string{layout.ContainerWorkspace}}, stop, signal)
	if err != nil {
		return err
	}
	entries, _ := result["data"].([]any)
	for _, entry := range entries {
		entry, _ := entry.(map[string]any)
		hooks, _ := entry["hooks"].([]any)
		for _, hook := range hooks {
			hook, _ := hook.(map[string]any)
			if hook["sourcePath"] != layout.AgentHome+"/.codex/hooks.json" || hook["eventName"] != "postToolUse" ||
				hook["handlerType"] != "command" || hook["command"] != strings.Join(layout.Command("hook"), " ") || hook["enabled"] != true {
				continue
			}
			if hook["trustStatus"] == "trusted" {
				return nil
			}
			key, hash := str(hook["key"]), str(hook["currentHash"])
			if key == "" || hash == "" {
				continue
			}
			_, err := r.request("config/value/write", map[string]any{
				"keyPath": "hooks.state", "mergeStrategy": "upsert",
				"value": map[string]any{key: map[string]any{"enabled": true, "trusted_hash": hash}},
			}, stop, signal)
			return err
		}
	}
	return errors.New("codex did not discover gnat's message-delivery hook")
}

func (r *codexRunner) shutdown() {
	if r.server != nil {
		r.server.close()
		r.server, r.open = nil, false
		r.pending = nil
	}
}

func (r *codexRunner) send(value any) error {
	data := marshal(value, false)
	data = append(data, '\n')
	// A stalled server must not hold the daemon in a write forever.
	if f, ok := r.server.stdin.(*os.File); ok {
		_ = f.SetWriteDeadline(time.Now().Add(r.requestWait))
	}
	_, err := r.server.stdin.Write(data)
	if err != nil {
		return errors.New("can't write to codex app-server")
	}
	return nil
}

func (r *codexRunner) sendRequest(method string, params map[string]any) (int, error) {
	r.nextID++
	return r.nextID, r.send(map[string]any{"id": r.nextID, "method": method, "params": params})
}

func (r *codexRunner) request(method string, params map[string]any, stop, signal <-chan struct{}) (map[string]any, error) {
	id, err := r.sendRequest(method, params)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(r.requestWait)
	for {
		msg, err := r.receive(deadline, stop, signal)
		if err != nil {
			return nil, err
		}
		if err := r.serverRequest(msg); err != nil {
			return nil, err
		}
		if string(msg.ID) != fmt.Sprint(id) || msg.Method != "" {
			if method == "turn/start" && msg.Method != "" {
				r.pending = append(r.pending, msg)
			}
			continue
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("codex %s failed: %s", method, r.safe(msg.Error.Message))
		}
		return msg.Result, nil
	}
}

func (r *codexRunner) receive(deadline time.Time, stop, signal <-chan struct{}) (codexRPC, error) {
	var timeout <-chan time.Time
	if !deadline.IsZero() {
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		timeout = timer.C
	}
	select {
	case <-stop:
		return codexRPC{}, errCodexStopped
	case <-signal:
		return codexRPC{}, errCodexStopped
	case <-timeout:
		return codexRPC{}, errors.New("codex app-server did not respond in time")
	case line, ok := <-r.server.lines:
		if !ok || errors.Is(line.err, io.EOF) {
			return codexRPC{}, errors.New("codex app-server exited before the turn completed")
		}
		return line.message, line.err
	}
}

func (r *codexRunner) serverRequest(msg codexRPC) error {
	if msg.Method == "" || len(msg.ID) == 0 {
		return nil
	}
	reason := "codex requested interactive input (" + msg.Method + ") in an unattended agent"
	if msg.Method == "account/chatgptAuthTokens/refresh" {
		reason = "codex ChatGPT authentication needs renewal; " + codexRenew
	}
	_ = r.send(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32000, "message": reason}})
	return errors.New(reason)
}

func (r *codexRunner) safe(text string) string {
	for _, secret := range []string{r.credential.AccessToken, os.Getenv("GNAT_CODEX_AUTH"), os.Getenv("GNAT_SECRET")} {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[credential]")
		}
	}
	return text
}

func (r *codexRunner) save() error {
	if err := os.MkdirAll(filepath.Dir(r.sessionPath), 0o700); err != nil {
		return errors.New("can't create the Codex session directory")
	}
	tmp := r.sessionPath + ".tmp"
	if err := os.WriteFile(tmp, jsonBytes(r.session), 0o600); err != nil {
		return errors.New("can't save the Codex session")
	}
	if err := os.Rename(tmp, r.sessionPath); err != nil {
		return errors.New("can't save the Codex session")
	}
	return nil
}

func (r *codexRunner) usage(params map[string]any) error {
	usage, _ := params["tokenUsage"].(map[string]any)
	total, _ := usage["total"].(map[string]any)
	last, _ := usage["last"].(map[string]any)
	if count(total["inputTokens"]) == nil || count(total["outputTokens"]) == nil {
		return nil
	}
	now := codexUsage{int64(orZero(total["inputTokens"])), int64(orZero(total["cachedInputTokens"])), int64(orZero(total["cacheWriteInputTokens"])), int64(orZero(total["outputTokens"]))}
	previous := r.session.Usage
	input := max(0, now.Input-previous.Input-(now.Cached-previous.Cached)-(now.Written-previous.Written))
	cached, output := max(0, now.Cached-previous.Cached), max(0, now.Output-previous.Output)
	var written *int64
	if count(total["cacheWriteInputTokens"]) != nil {
		written = ptr(max(0, now.Written-previous.Written))
	}
	r.session.Usage = now
	if err := r.save(); err != nil {
		return err
	}
	r.emit(events.NewUsage(events.UsageTotals{InputTokens: &input, CacheReadTokens: &cached, CacheWriteTokens: written, OutputTokens: &output}))
	if used := count(last["inputTokens"]); used != nil {
		r.emit(events.NewContext(*used, count(usage["modelContextWindow"])))
	}
	return nil
}

type codexTurn struct {
	emit      Emit
	tools     map[string]bool
	completed map[string]bool
	deltas    map[string]string
	reply     string
}

func (t *codexTurn) update(method string, params map[string]any) {
	if method == "item/agentMessage/delta" {
		t.deltas[str(params["itemId"])] += str(params["delta"])
		return
	}
	if method != "item/started" && method != "item/completed" {
		return
	}
	item, _ := params["item"].(map[string]any)
	id, kind := str(item["id"]), str(item["type"])
	if id == "" || t.completed[id] {
		return
	}
	complete := method == "item/completed"
	if complete {
		t.completed[id] = true
	}
	switch kind {
	case "agentMessage":
		if complete {
			text := str(item["text"])
			if text == "" {
				text = t.deltas[id]
			}
			delete(t.deltas, id)
			t.emit(events.NewMessage(text))
			if item["phase"] == "final_answer" || item["phase"] == nil {
				t.reply = text
			}
		}
	case "reasoning":
		if complete {
			parts, _ := item["summary"].([]any)
			if len(parts) == 0 {
				parts, _ = item["content"].([]any)
			}
			var text []string
			for _, part := range parts {
				text = append(text, str(part))
			}
			if len(text) > 0 {
				t.emit(events.NewThought(strings.Join(text, "\n")))
			}
		}
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "webSearch", "imageView", "collabAgentToolCall":
		name, input, output := kind, map[string]any{}, ""
		switch kind {
		case "commandExecution":
			name, input["command"], output = "Bash", item["command"], str(item["aggregatedOutput"])
		case "fileChange":
			name, input["changes"] = "apply_patch", item["changes"]
			output = string(marshal(item["changes"], false))
		case "mcpToolCall", "dynamicToolCall":
			name = str(item["tool"])
			if kind == "mcpToolCall" {
				name = "mcp__" + str(item["server"]) + "__" + name
			}
			input, _ = item["arguments"].(map[string]any)
			if input == nil {
				input = map[string]any{"arguments": item["arguments"]}
			}
			if item["error"] != nil {
				output = string(marshal(item["error"], false))
			} else if item["result"] != nil {
				output = string(marshal(item["result"], false))
			} else if item["contentItems"] != nil {
				output = string(marshal(item["contentItems"], false))
			}
		case "webSearch":
			input["query"] = item["query"]
		case "imageView":
			input["path"] = item["path"]
		case "collabAgentToolCall":
			name, input["prompt"] = str(item["tool"]), item["prompt"]
		}
		if !t.tools[id] {
			t.emit(events.NewToolCall(id, name, input, ""))
			t.tools[id] = true
		}
		if complete {
			failed := item["status"] == "failed" || item["status"] == "declined" || item["error"] != nil || item["success"] == false
			if exit := count(item["exitCode"]); exit != nil && *exit != 0 {
				failed = true
			}
			t.emit(events.NewToolResult(id, output, failed))
		}
	}
}
