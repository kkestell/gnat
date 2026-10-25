// Package harness translates local provider protocols behind the core adapter contract.
package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/kkestell/gnat/internal/v2/store"
)

type Claude struct{}

func (Claude) Probe(ctx context.Context, s store.Settings) error {
	if s.Harness != "claude" {
		return fmt.Errorf("%s execution is not available yet; create a workspace configured with claude (other adapters arrive in milestone four)", s.Harness)
	}
	if _, err := exec.LookPath("claude"); err != nil {
		return fmt.Errorf("Claude Code is missing: install claude, then choose Retry: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "claude", "--version").CombinedOutput(); err != nil {
		return fmt.Errorf("cannot run Claude Code: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	out, err := exec.CommandContext(ctx, "claude", "auth", "status").Output()
	var auth struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if err != nil || json.Unmarshal(out, &auth) != nil || !auth.LoggedIn {
		return fmt.Errorf("Claude Code authentication is unavailable: run claude auth login in a terminal, then choose Retry")
	}
	return nil
}
func (Claude) Run(ctx context.Context, c *store.Claim, emit func(map[string]any) error) error {
	args := []string{"-p", "--dangerously-skip-permissions", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--model", c.Agent.Settings.Model, "--effort", c.Agent.Settings.Effort}
	if c.SessionRef != "" {
		args = append(args, "--resume", c.SessionRef)
	}
	if c.MCPURL != "" {
		config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"gnat": map[string]any{"type": "http", "url": c.MCPURL, "headers": map[string]string{"Authorization": "Bearer " + c.MCPToken}}}})
		if err != nil {
			return err
		}
		args = append(args, "--mcp-config", string(config))
	}
	if c.Instructions != "" {
		args = append(args, "--append-system-prompt", c.Instructions)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = c.Agent.WorkingDir
	input := c.Text
	if c.Sender != "" && c.Sender != "user" {
		input = "Message from " + c.Sender + ":\n" + input
	}
	cmd.Stdin = strings.NewReader(input)
	// Inherit local configuration, credentials, and native delegation unchanged.
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	out, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer out.Close()
	cmd.Stdout = writer
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		writer.Close()
		return fmt.Errorf("start Claude Code: %w", err)
	}
	writer.Close()
	waited := make(chan error, 1)
	go func() { err := cmd.Wait(); _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); waited <- err }()
	decoder := translator{emit: emit}
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	var streamErr error
	for scanner.Scan() {
		if streamErr = decoder.line(scanner.Bytes()); streamErr != nil {
			_ = cmd.Cancel()
			break
		}
	}
	if err = scanner.Err(); streamErr == nil {
		streamErr = err
	}
	if streamErr != nil {
		_ = cmd.Cancel()
	}
	waitErr := <-waited
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if streamErr != nil {
		return streamErr
	}
	if waitErr != nil {
		return fmt.Errorf("Claude Code failed: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	if !decoder.result {
		return fmt.Errorf("Claude Code exited without a turn result; retry after checking the saved session")
	}
	return nil
}

type translator struct {
	emit    func(map[string]any) error
	partial map[string]map[int]bool
	result  bool
	session string
}

func (t *translator) line(line []byte) error {
	var e map[string]any
	if err := json.Unmarshal(line, &e); err != nil {
		return fmt.Errorf("invalid Claude Code output: %w", err)
	}
	parent, _ := e["parent_tool_use_id"].(string)
	send := func(v map[string]any) error {
		if parent != "" {
			v["parent_tool_use_id"] = parent
		}
		return t.emit(v)
	}
	if ref, _ := e["session_id"].(string); ref != "" && t.session == "" {
		if err := t.emit(map[string]any{"type": "session", "session_ref": ref}); err != nil {
			return err
		}
		t.session = ref
	}
	switch e["type"] {
	case "stream_event":
		event, _ := e["event"].(map[string]any)
		delta, _ := event["delta"].(map[string]any)
		if delta["type"] == "text_delta" {
			if t.partial == nil {
				t.partial = map[string]map[int]bool{}
			}
			if t.partial[parent] == nil {
				t.partial[parent] = map[int]bool{}
			}
			index, _ := event["index"].(float64)
			t.partial[parent][int(index)] = true
			return send(map[string]any{"type": "text_delta", "text": delta["text"]})
		}
	case "assistant", "user":
		m, _ := e["message"].(map[string]any)
		content, _ := m["content"].([]any)
		for index, item := range content {
			b, _ := item.(map[string]any)
			var v map[string]any
			switch b["type"] {
			case "text":
				if e["type"] == "assistant" && !t.partial[parent][index] {
					v = map[string]any{"type": "message", "text": b["text"]}
				}
			case "tool_use":
				v = map[string]any{"type": "tool_call", "id": b["id"], "name": b["name"], "input": b["input"]}
			case "tool_result":
				v = map[string]any{"type": "tool_result", "id": b["tool_use_id"], "output": toolText(b["content"]), "error": b["is_error"]}
			}
			if v != nil {
				if err := send(v); err != nil {
					return err
				}
			}
		}
		if e["type"] == "assistant" {
			delete(t.partial, parent)
		}
	case "result":
		t.result = true
		if usage, ok := e["usage"].(map[string]any); ok {
			v := map[string]any{"type": "usage"}
			for _, key := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
				if n, ok := usage[key]; ok {
					v[key] = n
				}
			}
			if cost, ok := e["total_cost_usd"]; ok {
				v["cost"] = cost
			}
			if err := send(v); err != nil {
				return err
			}
		}
		if e["is_error"] == true {
			return fmt.Errorf("Claude Code: %v (%v); retry the saved session or explicitly start a new conversation", e["result"], e["subtype"])
		}
		if t.session == "" {
			return fmt.Errorf("Claude Code returned no session identity")
		}
	}
	return nil
}
func toolText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
