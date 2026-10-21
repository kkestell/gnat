package harness

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/kkestell/gnat/internal/events"
	"github.com/kkestell/gnat/internal/layout"
)

// claudeSettings is gnat's hook, and no Claude attribution on the commits and pull
// requests agents make.
func claudeSettings() map[string]any {
	settings := hookSettings()
	settings["attribution"] = map[string]any{"commit": "", "pr": ""}
	return settings
}

// claude is Claude Code, run once per turn as `claude -p`, continuing the agent's last session.
type claude struct{ info }

func newClaude() *claude {
	home := layout.AgentHome + "/.claude"
	return &claude{info{
		name:             "claude",
		program:          "claude",
		credential:       "CLAUDE_CODE_OAUTH_TOKEN",
		credentialSource: "run `claude setup-token`",
		efforts:          []string{"low", "medium", "high", "xhigh", "max"},
		configDir:        home,
		stateDirs:        []string{home},
	}}
}

func (*claude) ConfigFiles(instructions, model, effort string) map[string][]byte {
	// Claude takes its model and effort as options; see claudeRunner.
	return map[string][]byte{"CLAUDE.md": []byte(instructions), "settings.json": jsonBytes(claudeSettings())}
}

func (*claude) Runner(model, effort string, emit Emit) Runner {
	return newClaudeRunner(model, effort, emit)
}

type claudeRunner struct {
	// command is claude and its options; the turn's message is its last argument.
	command []string
	emit    Emit
	// mu guards proc, which Stop kills from another goroutine.
	mu   sync.Mutex
	proc *os.Process
}

func newClaudeRunner(model, effort string, emit Emit) *claudeRunner {
	// The container is the sandbox, so skip permission checks. stream-json reports each
	// step as it happens, and -p requires --verbose for it.
	command := []string{
		"claude", "-p", "--dangerously-skip-permissions",
		"--output-format", "stream-json", "--verbose",
	}
	if model != "" {
		command = append(command, "--model", model)
	}
	if effort != "" {
		command = append(command, "--effort", effort)
	}
	return &claudeRunner{command: command, emit: emit}
}

func (r *claudeRunner) Run(text string, new bool, stop <-chan struct{}) (int, error) {
	// --continue resumes the session the previous message left off, or starts one on the
	// first; a message sent with --new starts a fresh session instead.
	args := append([]string(nil), r.command[1:]...)
	if !new {
		args = append(args, "--continue")
	}
	args = append(args, text)
	cmd := exec.Command(r.command[0], args...)
	// Its own process group, so a stop also kills what Claude's tools started (which would
	// otherwise keep its output open).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Claude's stderr goes where its stdout does.
	out, in, err := os.Pipe()
	if err != nil {
		return 1, err
	}
	defer out.Close()
	cmd.Stdout, cmd.Stderr = in, in
	err = cmd.Start()
	in.Close()
	if err != nil {
		r.emit(events.NewError(fmt.Sprintf("can't run claude: %v", err)))
		return 127, nil
	}
	r.mu.Lock()
	r.proc = cmd.Process
	select {
	case <-stop:
		killGroup(cmd.Process)
	default:
	}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.proc = nil
		r.mu.Unlock()
	}()

	reader := bufio.NewReader(out)
	for {
		line, readErr := reader.ReadString('\n')
		if line != "" {
			for _, event := range Translate(line) {
				r.emit(event)
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				killGroup(cmd.Process)
				_ = cmd.Wait()
				return 1, readErr
			}
			break
		}
	}
	err = cmd.Wait()
	return exitStatus(cmd, err), nil
}

func (r *claudeRunner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.proc != nil {
		killGroup(r.proc)
	}
}

// killGroup kills Claude and whatever its tools started.
func killGroup(proc *os.Process) {
	_ = syscall.Kill(-proc.Pid, syscall.SIGKILL)
}

// exitStatus is the exit status of a process that has been waited for, or minus the
// signal that killed it. A wait that failed only because something kept its output open
// past the wait delay still has the process's own status.
func exitStatus(cmd *exec.Cmd, err error) int {
	if state := cmd.ProcessState; state != nil {
		if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return -int(status.Signal())
		}
		return state.ExitCode()
	}
	if err != nil {
		return 1
	}
	return 0
}

// Translate returns gnat's events for one line of `claude -p --output-format stream-json`
// output.
func Translate(line string) []events.Event {
	if strings.TrimSpace(line) == "" {
		return nil
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(line), &event); err != nil || event == nil {
		// Claude's own errors, and anything else it prints that isn't an event.
		return []events.Event{events.NewError(strings.TrimSuffix(line, "\n"))}
	}
	// Events from a subagent name the tool call that started it.
	parent := str(event["parent_tool_use_id"])
	message, _ := event["message"].(map[string]any)
	content, isList := message["content"].([]any)
	switch event["type"] {
	case "assistant":
		if !isList {
			return nil
		}
		var out []events.Event
		for _, block := range content {
			if block, ok := block.(map[string]any); ok {
				if e := assistantEvent(block, parent); e != nil {
					out = append(out, e)
				}
			}
		}
		return out
	case "user":
		if !isList {
			return nil
		}
		var out []events.Event
		for _, block := range content {
			if block, ok := block.(map[string]any); ok && block["type"] == "tool_result" {
				out = append(out, events.NewToolResult(
					str(block["tool_use_id"]), toolOutput(block["content"]), truthy(block["is_error"])).WithParent(parent))
			}
		}
		return out
	case "result":
		if truthy(event["is_error"]) {
			reason := str(event["result"])
			if reason == "" {
				reason = str(event["subtype"])
			}
			if reason == "" {
				reason = "error"
			}
			return append(usageEvents(event), events.NewTurnEnd("", reason))
		}
		return append(usageEvents(event), events.NewTurnEnd(str(event["result"]), ""))
	}
	return nil
}

// usageEvents is the usage a result reports for its turn: every model's, subagents' included.
func usageEvents(result map[string]any) []events.Event {
	cost := number(result["total_cost_usd"])
	if models, ok := result["modelUsage"].(map[string]any); ok && len(models) > 0 {
		var input, cacheRead, cacheWrite, output float64
		for _, m := range models {
			if m, ok := m.(map[string]any); ok {
				input += orZero(m["inputTokens"])
				cacheRead += orZero(m["cacheReadInputTokens"])
				cacheWrite += orZero(m["cacheCreationInputTokens"])
				output += orZero(m["outputTokens"])
			}
		}
		return []events.Event{events.NewUsage(events.UsageTotals{
			InputTokens: ptr(int64(input)), CacheReadTokens: ptr(int64(cacheRead)),
			CacheWriteTokens: ptr(int64(cacheWrite)), OutputTokens: ptr(int64(output)), Cost: cost,
		})}
	}
	if usage, ok := result["usage"].(map[string]any); ok {
		return []events.Event{events.NewUsage(events.UsageTotals{
			InputTokens: count(usage["input_tokens"]), CacheReadTokens: count(usage["cache_read_input_tokens"]),
			CacheWriteTokens: count(usage["cache_creation_input_tokens"]),
			OutputTokens:     count(usage["output_tokens"]), Cost: cost,
		})}
	}
	return nil
}

func assistantEvent(block map[string]any, parent string) events.Event {
	switch block["type"] {
	case "text":
		return events.NewMessage(str(block["text"])).WithParent(parent)
	case "thinking":
		if thinking := str(block["thinking"]); thinking != "" {
			return events.NewThought(thinking).WithParent(parent)
		}
	case "tool_use":
		name, ok := block["name"].(string)
		if !ok {
			name = "tool"
		}
		input, _ := block["input"].(map[string]any)
		if input == nil {
			input = map[string]any{}
		}
		return events.NewToolCall(str(block["id"]), name, input, "").WithParent(parent)
	}
	return nil
}

// toolOutput is the text of a tool result's content.
func toolOutput(content any) string {
	switch content := content.(type) {
	case string:
		return content
	case []any:
		parts := make([]string, len(content))
		for i, part := range content {
			part, _ := part.(map[string]any)
			if part["type"] == "text" {
				parts[i] = str(part["text"])
			} else if kind, ok := part["type"].(string); ok {
				parts[i] = "[" + kind + "]"
			} else {
				parts[i] = "[None]"
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// truthy is whether Python would take v as true, since Claude's own JSON is read as it was.
func truthy(v any) bool {
	switch v := v.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return v != ""
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return false
}

// str is v if it's a string, else "".
func str(v any) string {
	s, _ := v.(string)
	return s
}

// number is v as a pointer to a float, or nil if it isn't a number.
func number(v any) *float64 {
	if f, ok := v.(float64); ok {
		return &f
	}
	return nil
}

// count is v as a pointer to a count of tokens, or nil if it isn't a number.
func count(v any) *int64 {
	if f, ok := v.(float64); ok {
		return ptr(int64(f))
	}
	return nil
}

func orZero(v any) float64 {
	f, _ := v.(float64)
	return f
}

func ptr[T any](v T) *T { return &v }
