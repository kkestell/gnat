// Package mcp is a stateless, JSON-response Streamable HTTP transport over core operations.
package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/kkestell/gnat/internal/core"
	"github.com/kkestell/gnat/internal/store"
)

func tool(name, description string, required []string, properties map[string]any) map[string]any {
	return map[string]any{"name": name, "description": description, "inputSchema": map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}
}
func text(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
func tools(orchestrator bool) []map[string]any {
	out := []map[string]any{
		tool("list_agents", "List current swarm agents and execution/checkout metadata.", []string{}, map[string]any{}),
		tool("send_message", "Enqueue an ordinary message from your authenticated identity. Each message starts a recipient turn when available.", []string{"request_id", "agent_id", "text"}, map[string]any{"request_id": text("Unique operation ID; reuse only for retries."), "agent_id": text("Recipient agent ID."), "text": text("Message text.")}),
		tool("read_agent", "Inspect bounded recent history, including removed agents. Events are newest first; pass before to read older events.", []string{"agent_id"}, map[string]any{"agent_id": text("Agent ID."), "before": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}),
	}
	if orchestrator {
		out = append(out,
			tool("create_agent", "Allocate a worker asynchronously with its own worktree and conversation. Initial prompt queues durably; inspect provisioning via list_agents/read_agent.", []string{"request_id", "name", "prompt"}, map[string]any{"request_id": text("Unique operation ID; reuse only for retries."), "name": text("Display name."), "prompt": text("Initial message."), "harness": text("Optional harness override."), "model": text("Optional model override."), "effort": text("Optional effort override."), "base": text("Optional committed ref; defaults to the original checkout HEAD.")}),
			tool("interrupt_agent", "Interrupt the observed worker turn; queued messages and history remain.", []string{"request_id", "agent_id", "turn_id"}, map[string]any{"request_id": text("Unique operation ID."), "agent_id": text("Worker ID."), "turn_id": text("Currently running turn ID from list_agents.")}),
			tool("remove_agent", "Stop a worker and remove its worktree and branch with ordinary Git safeguards. Refusals remain visible; repair using ordinary tools and retry.", []string{"request_id", "agent_id"}, map[string]any{"request_id": text("Unique operation ID."), "agent_id": text("Worker ID.")}),
		)
	}
	return out
}
func Handler(db *store.Store, service *core.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			http.Error(w, "runtime unavailable", http.StatusServiceUnavailable)
			return
		}
		b, ok := service.Authenticate(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if !ok || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "agent credential required", http.StatusUnauthorized)
			return
		}
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			http.Error(w, "MCP notification stream is not offered", http.StatusMethodNotAllowed)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
			return
		}
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
			http.Error(w, "invalid JSON-RPC request", http.StatusBadRequest)
			return
		}
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		respond := func(result any, code int, message string) {
			w.Header().Set("Content-Type", "application/json")
			response := map[string]any{"jsonrpc": "2.0", "id": req.ID}
			if code != 0 {
				response["error"] = map[string]any{"code": code, "message": message}
			} else {
				response["result"] = result
			}
			_ = json.NewEncoder(w).Encode(response)
		}
		a, err := db.Agent(b.SwarmID, b.AgentID)
		if err != nil {
			respond(nil, -32603, err.Error())
			return
		}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			version := p.ProtocolVersion
			switch version {
			case "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25":
			default:
				version = "2025-06-18"
			}
			respond(map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "gnat", "version": "2"}}, 0, "")
		case "ping":
			respond(map[string]any{}, 0, "")
		case "tools/list":
			respond(map[string]any{"tools": tools(a.Role == "orchestrator")}, 0, "")
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err = json.Unmarshal(req.Params, &p); err != nil {
				respond(nil, -32602, "invalid tool call")
				return
			}
			var spec map[string]any
			for _, t := range tools(a.Role == "orchestrator") {
				if t["name"] == p.Name {
					spec = t
					break
				}
			}
			if spec == nil {
				respond(nil, -32602, "unknown or unauthorized tool")
				return
			}
			if err = validate(p.Arguments, spec); err != nil {
				respond(nil, -32602, err.Error())
				return
			}
			var result any
			switch p.Name {
			case "list_agents":
				result, err = db.Agents(b.SwarmID, false)
			case "read_agent":
				var input struct {
					AgentID string `json:"agent_id"`
					Before  int64  `json:"before"`
					Limit   int    `json:"limit"`
				}
				err = json.Unmarshal(p.Arguments, &input)
				if err == nil {
					result, err = db.ReadAgent(b.SwarmID, input.AgentID, input.Before, input.Limit)
				}
			default:
				var c store.Command
				err = json.Unmarshal(p.Arguments, &c)
				if err == nil {
					result, err = service.Coordinate(b, p.Name, c)
				}
			}
			if err != nil {
				respond(map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": err.Error()}}}, 0, "")
				return
			}
			data, _ := json.Marshal(result)
			respond(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(data)}}, "structuredContent": map[string]any{"result": result}}, 0, "")
		default:
			respond(nil, -32601, "method not found")
		}
	})
}
func validate(raw json.RawMessage, spec map[string]any) error {
	var args map[string]json.RawMessage
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return fmt.Errorf("arguments must be an object")
	}
	schema := spec["inputSchema"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	for _, key := range schema["required"].([]string) {
		if _, ok := args[key]; !ok {
			return fmt.Errorf("%s is required", key)
		}
	}
	for key, value := range args {
		p, ok := properties[key]
		if !ok {
			return fmt.Errorf("unknown argument %q", key)
		}
		if bytes.Equal(value, []byte("null")) {
			return fmt.Errorf("%s cannot be null", key)
		}
		if p.(map[string]any)["type"] == "string" {
			var text string
			if json.Unmarshal(value, &text) != nil {
				return fmt.Errorf("%s must be a string", key)
			}
		} else {
			var n int64
			if json.Unmarshal(value, &n) != nil || n < 0 {
				return fmt.Errorf("%s must be a nonnegative integer", key)
			}
		}
	}
	return nil
}
