package harness

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/kkestell/gnat/internal/store"
)

type Codex struct{}

func codexServer(ctx context.Context, dir string) (*rpcProcess, error) {
	p, err := startRPC(ctx, "codex", dir, "app-server")
	if err != nil {
		return nil, err
	}
	_, err = p.setup(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "gnat", "version": "2"}, "capabilities": map[string]any{}})
	if err == nil {
		err = p.notify("initialized", map[string]any{})
	}
	if err != nil {
		p.close()
		return nil, fmt.Errorf("incompatible Codex app-server; update Codex: %w", err)
	}
	return p, nil
}
func (Codex) Probe(ctx context.Context, s store.Settings) error {
	if _, err := exec.LookPath("codex"); err != nil {
		return fmt.Errorf("Codex is missing: install codex, then choose Recheck harness: %w", err)
	}
	p, err := codexServer(ctx, "")
	if err != nil {
		return err
	}
	defer p.close()
	account, err := p.setup(ctx, "account/read", map[string]any{})
	if err != nil {
		return err
	}
	if account["account"] == nil && account["requiresOpenaiAuth"] != false {
		return fmt.Errorf("Codex authentication unavailable: run codex login, then choose Recheck harness")
	}
	cursor := ""
	for {
		args := map[string]any{"limit": 100}
		if cursor != "" {
			args["cursor"] = cursor
		}
		result, err := p.setup(ctx, "model/list", args)
		if err != nil {
			return err
		}
		models, _ := result["data"].([]any)
		for _, v := range models {
			model := object(v)
			if model["model"] != s.Model && model["id"] != s.Model {
				continue
			}
			efforts, _ := model["supportedReasoningEfforts"].([]any)
			for _, v := range efforts {
				if object(v)["reasoningEffort"] == s.Effort {
					return nil
				}
			}
			return fmt.Errorf("Codex model %q does not advertise effort %q; select a supported effort", s.Model, s.Effort)
		}
		next := text(result["nextCursor"])
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	return fmt.Errorf("Codex model %q is unavailable in model/list; select an installed account model", s.Model)
}
func (Codex) Run(ctx context.Context, c *store.Claim, emit func(map[string]any) error) error {
	p, err := codexServer(ctx, c.Agent.WorkingDir)
	if err != nil {
		return err
	}
	defer p.close()
	config := map[string]any{}
	if c.MCPURL != "" {
		config["mcp_servers.gnat"] = map[string]any{"url": c.MCPURL, "http_headers": map[string]string{"Authorization": "Bearer " + c.MCPToken}, "required": true}
	}
	params := map[string]any{"cwd": c.Agent.WorkingDir, "model": c.Agent.Settings.Model, "approvalPolicy": "never", "sandbox": "danger-full-access", "developerInstructions": c.Instructions, "config": config}
	method := "thread/start"
	if c.SessionRef != "" {
		method = "thread/resume"
		params["threadId"] = c.SessionRef
	}
	result, err := p.setup(ctx, method, params)
	if err != nil {
		return err
	}
	thread := text(object(result["thread"])["id"])
	if err = session(thread, c.SessionRef, emit); err != nil {
		return err
	}
	var pending []rpcMessage
	result, err = p.request(ctx, "turn/start", map[string]any{"threadId": thread, "effort": c.Agent.Settings.Effort, "input": []any{map[string]any{"type": "text", "text": prompt(c)}}}, func(m rpcMessage) error {
		if err := p.reject(m); err != nil {
			return err
		}
		pending = append(pending, m)
		return nil
	})
	if err != nil {
		return err
	}
	turn := text(object(result["turn"])["id"])
	if turn == "" {
		return fmt.Errorf("Codex returned no turn identity")
	}
	defer func() {
		if ctx.Err() != nil {
			p.cancel("turn/interrupt", map[string]any{"threadId": thread, "turnId": turn}, true, func(m rpcMessage) bool {
				return m.Method == "turn/completed" && text(m.Params["threadId"]) == thread && text(object(m.Params["turn"])["id"]) == turn
			})
		}
	}()
	t := codexTurn{emit: emit, streamed: map[string]bool{}, started: map[string]bool{}, completed: map[string]bool{}}
	for {
		var m rpcMessage
		if len(pending) > 0 {
			m, pending = pending[0], pending[1:]
		} else {
			m, err = p.receive(ctx)
			if err != nil {
				return err
			}
		}
		if err = p.reject(m); err != nil {
			return err
		}
		if text(m.Params["threadId"]) != thread {
			continue
		}
		id := text(m.Params["turnId"])
		if m.Method == "turn/completed" {
			id = text(object(m.Params["turn"])["id"])
		}
		if id != turn {
			continue
		}
		if m.Method == "turn/completed" {
			ended := object(m.Params["turn"])
			if len(t.usage) > 0 {
				if err := emit(t.usage); err != nil {
					return err
				}
			}
			if ended["status"] != "completed" {
				return fmt.Errorf("Codex turn %s: %s", text(ended["status"]), text(object(ended["error"])["message"]))
			}
			return nil
		}
		if err = t.update(m); err != nil {
			return err
		}
	}
}

type codexTurn struct {
	emit                         func(map[string]any) error
	streamed, started, completed map[string]bool
	baseline, usage              map[string]any
}

func (t *codexTurn) update(m rpcMessage) error {
	p := m.Params
	if m.Method == "thread/tokenUsage/updated" {
		usage := object(p["tokenUsage"])
		total, last := object(usage["total"]), object(usage["last"])
		if t.baseline == nil {
			t.baseline = map[string]any{}
			for k, v := range total {
				if n, ok := v.(float64); ok {
					if l, ok := last[k].(float64); ok {
						t.baseline[k] = n - l
					}
				}
			}
		}
		t.usage = map[string]any{"type": "usage"}
		for from, to := range map[string]string{"inputTokens": "input_tokens", "outputTokens": "output_tokens", "cachedInputTokens": "cache_read_input_tokens", "cacheWriteInputTokens": "cache_creation_input_tokens"} {
			n, ok := total[from].(float64)
			b, known := t.baseline[from].(float64)
			if ok && known {
				t.usage[to] = max(0, n-b)
			}
		}
		// Provider input totals include cached tokens; normalized input excludes them.
		if n, ok := t.usage["input_tokens"].(float64); ok {
			for _, k := range []string{"cache_read_input_tokens", "cache_creation_input_tokens"} {
				if cached, ok := t.usage[k].(float64); ok {
					n -= cached
				}
			}
			t.usage["input_tokens"] = max(0, n)
		}
		return nil
	}
	id := text(p["itemId"])
	if m.Method == "item/agentMessage/delta" {
		t.streamed[id] = true
		return t.emit(map[string]any{"type": "text_delta", "text": p["delta"]})
	}
	if m.Method != "item/started" && m.Method != "item/completed" {
		return nil
	}
	item := object(p["item"])
	id = text(item["id"])
	kind := text(item["type"])
	if id == "" || t.completed[id] {
		return nil
	}
	complete := m.Method == "item/completed"
	if complete {
		t.completed[id] = true
	}
	switch kind {
	case "agentMessage":
		if complete && !t.streamed[id] {
			return t.emit(map[string]any{"type": "message", "text": item["text"]})
		}
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "webSearch", "imageView", "collabToolCall", "collabAgentToolCall":
		name, input := kind, any(item)
		if kind == "mcpToolCall" {
			name = "mcp__" + text(item["server"]) + "__" + text(item["tool"])
			input = item["arguments"]
		}
		// Activity from native delegation stays inside this parent's transcript.
		if !t.started[id] {
			t.started[id] = true
			if err := t.emit(map[string]any{"type": "tool_call", "id": id, "name": name, "input": input}); err != nil {
				return err
			}
		}
		if complete {
			output := item["result"]
			if output == nil {
				output = item["aggregatedOutput"]
			}
			if output == nil {
				output = item["changes"]
			}
			if output == nil {
				output = item["contentItems"]
			}
			if output == nil {
				output = item
			}
			failed := item["status"] == "failed" || item["error"] != nil || item["success"] == false
			if item["error"] != nil {
				output = item["error"]
			}
			if code, ok := item["exitCode"].(float64); ok && code != 0 {
				failed = true
			}
			return t.emit(map[string]any{"type": "tool_result", "id": id, "output": toolText(output), "error": failed})
		}
	}
	return nil
}
