package harness

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/kkestell/gnat/internal/v2/store"
)

type OpenCode struct{}

func openCodeServer(ctx context.Context, dir string) (*rpcProcess, error) {
	p, err := startRPC(ctx, "opencode", dir, "acp")
	if err != nil {
		return nil, err
	}
	init, err := p.setup(ctx, "initialize", map[string]any{"protocolVersion": 1, "clientInfo": map[string]any{"name": "gnat", "version": "2"}, "clientCapabilities": map[string]any{}})
	if err == nil {
		caps := object(init["agentCapabilities"])
		sessions := object(caps["sessionCapabilities"])
		if init["protocolVersion"] != float64(1) || object(caps["mcpCapabilities"])["http"] != true || sessions["resume"] == nil {
			err = fmt.Errorf("requires ACP 1, HTTP MCP, and session/resume support")
		}
	}
	if err != nil {
		p.close()
		return nil, fmt.Errorf("incompatible OpenCode ACP; install a release supporting the OpenCode 2 ACP contract: %w", err)
	}
	return p, nil
}
func (OpenCode) Probe(ctx context.Context, s store.Settings) error {
	if _, err := exec.LookPath("opencode"); err != nil {
		return fmt.Errorf("OpenCode is missing: install opencode and run opencode auth login, then choose Retry: %w", err)
	}
	p, err := openCodeServer(ctx, "")
	if err != nil {
		return err
	}
	p.close()
	return nil
}
func (OpenCode) Run(ctx context.Context, c *store.Claim, emit func(map[string]any) error) error {
	p, err := openCodeServer(ctx, c.Agent.WorkingDir)
	if err != nil {
		return err
	}
	defer p.close()
	servers := []any{}
	if c.MCPURL != "" {
		servers = append(servers, map[string]any{"type": "http", "name": "gnat", "url": c.MCPURL, "headers": []any{map[string]any{"name": "Authorization", "value": "Bearer " + c.MCPToken}}})
	}
	params := map[string]any{"cwd": c.Agent.WorkingDir, "mcpServers": servers}
	method := "session/new"
	if c.SessionRef != "" {
		method = "session/resume"
		params["sessionId"] = c.SessionRef
	}
	result, err := p.setup(ctx, method, params)
	if err != nil {
		return fmt.Errorf("OpenCode %w; check opencode auth login or explicitly start a new conversation if the saved session is unavailable", err)
	}
	ref := text(result["sessionId"])
	if err = session(ref, c.SessionRef, emit); err != nil {
		return err
	}
	result, err = setOpenCodeOption(ctx, p, ref, result, "model", c.Agent.Settings.Model)
	if err != nil {
		return err
	}
	if findOption(result, "effort") != nil {
		if _, err = setOpenCodeOption(ctx, p, ref, result, "effort", c.Agent.Settings.Effort); err != nil {
			return err
		}
	} else if c.Agent.Settings.Effort != "default" {
		return fmt.Errorf("OpenCode model %q has no effort option; configure effort: default", c.Agent.Settings.Model)
	}
	t := acpTurn{emit: emit, started: map[string]bool{}, completed: map[string]bool{}}
	update := func(m rpcMessage) error {
		if m.Method == "session/request_permission" && len(m.ID) > 0 {
			if text(m.Params["sessionId"]) != ref {
				return p.reject(m)
			}
			options, _ := m.Params["options"].([]any)
			for _, v := range options {
				o := object(v)
				if o["kind"] == "allow_once" {
					return p.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o["optionId"]}}})
				}
			}
			return fmt.Errorf("OpenCode offered no allow_once permission option")
		}
		if err := p.reject(m); err != nil {
			return err
		}
		if m.Method == "session/update" && m.Params["sessionId"] == ref {
			return t.update(object(m.Params["update"]))
		}
		return nil
	}
	defer func() {
		if ctx.Err() != nil {
			p.cancel("session/cancel", map[string]any{"sessionId": ref}, false, func(m rpcMessage) bool { return m.Method == "" && m.Result["stopReason"] == "cancelled" })
		}
	}()
	blocks := []any{}
	if c.Instructions != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": c.Instructions, "annotations": map[string]any{"audience": []string{"assistant"}}})
	}
	blocks = append(blocks, map[string]any{"type": "text", "text": prompt(c)})
	result, err = p.request(ctx, "session/prompt", map[string]any{"sessionId": ref, "prompt": blocks}, update)
	if err != nil {
		return fmt.Errorf("OpenCode prompt failed: %w; check local provider authentication with opencode auth login", err)
	}
	if u := object(result["usage"]); u != nil {
		v := map[string]any{"type": "usage"}
		for from, to := range map[string]string{"inputTokens": "input_tokens", "outputTokens": "output_tokens", "cachedReadTokens": "cache_read_input_tokens", "cachedWriteTokens": "cache_creation_input_tokens"} {
			if n, ok := u[from]; ok {
				v[to] = n
			}
		}
		if err := emit(v); err != nil {
			return err
		}
	}
	if result["stopReason"] != "end_turn" {
		return fmt.Errorf("OpenCode prompt ended: %s", text(result["stopReason"]))
	}
	return nil
}
func findOption(result map[string]any, id string) map[string]any {
	options, _ := result["configOptions"].([]any)
	for _, v := range options {
		if o := object(v); o["id"] == id {
			return o
		}
	}
	return nil
}
func optionValue(options []any, value string) bool {
	for _, v := range options {
		o := object(v)
		if o["value"] == value {
			return true
		}
		if group, ok := o["options"].([]any); ok && optionValue(group, value) {
			return true
		}
	}
	return false
}
func setOpenCodeOption(ctx context.Context, p *rpcProcess, ref string, result map[string]any, id, value string) (map[string]any, error) {
	o := findOption(result, id)
	options, _ := o["options"].([]any)
	if o == nil || !optionValue(options, value) {
		return nil, fmt.Errorf("OpenCode does not advertise %s %q; select a supported session config option", id, value)
	}
	result, err := p.setup(ctx, "session/set_config_option", map[string]any{"sessionId": ref, "configId": id, "value": value})
	if err != nil {
		return nil, err
	}
	if findOption(result, id)["currentValue"] != value {
		return nil, fmt.Errorf("OpenCode did not apply %s %q", id, value)
	}
	return result, nil
}

type acpTurn struct {
	emit               func(map[string]any) error
	started, completed map[string]bool
}

func (t *acpTurn) update(u map[string]any) error {
	parent := text(object(object(u["_meta"])["opencode/child-session"])["id"])
	send := func(v map[string]any) error {
		if parent != "" {
			v["parent_tool_use_id"] = parent
		}
		return t.emit(v)
	}
	switch u["sessionUpdate"] {
	case "agent_message_chunk":
		content := object(u["content"])
		if content["type"] == "text" {
			return send(map[string]any{"type": "text_delta", "text": content["text"]})
		}
	case "tool_call", "tool_call_update":
		id := text(u["toolCallId"])
		if id == "" || t.completed[id] {
			return nil
		}
		if !t.started[id] {
			t.started[id] = true
			name := text(u["title"])
			if name == "" {
				name = text(u["kind"])
			}
			if err := send(map[string]any{"type": "tool_call", "id": id, "name": name, "input": u["rawInput"]}); err != nil {
				return err
			}
		}
		if u["status"] == "completed" || u["status"] == "failed" {
			t.completed[id] = true
			output := u["rawOutput"]
			if output == nil {
				output = u["content"]
			}
			return send(map[string]any{"type": "tool_result", "id": id, "output": toolText(output), "error": u["status"] == "failed"})
		}
	}
	return nil
}
