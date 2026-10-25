package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/kkestell/gnat/internal/v2/store"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the documented MCP path independently of model-driven team creation.
func TestLiveAdapterMCP(t *testing.T) {
	if os.Getenv("GNAT_LIVE_TEST") != "1" {
		t.Skip("requires authenticated local harnesses")
	}
	settings := []store.Settings{
		{Harness: "claude", Model: "sonnet", Effort: "low"},
		{Harness: "codex", Model: "gpt-6.1-sol", Effort: "low"},
		{Harness: "opencode", Model: "deepseek/deepseek-flash", Effort: "default"},
	}
	for _, s := range settings {
		t.Run(s.Harness, func(t *testing.T) {
			for _, role := range []string{"orchestrator", "worker"} {
				t.Run(role, func(t *testing.T) { liveAdapterMCP(t, s, role) })
			}
		})
	}
}
func liveAdapterMCP(t *testing.T, s store.Settings, role string) {
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("missing launch-scoped MCP authorization")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var m rpcMessage
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			t.Error(err)
			return
		}
		if len(m.ID) == 0 {
			w.WriteHeader(202)
			return
		}
		var result any
		switch m.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "gnat", "version": "2"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "check", "description": "Check Gnat MCP connectivity.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
		case "tools/call":
			called.Store(m.Params["name"] == "check")
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "connected"}}}
		case "resources/list":
			result = map[string]any{"resources": []any{}}
		case "resources/templates/list":
			result = map[string]any{"resourceTemplates": []any{}}
		default:
			result = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
	}))
	defer server.Close()
	c := fixtureClaim(t.TempDir(), "Call the check tool on the gnat MCP server once, then end. Do not edit files.", "")
	c.Agent.Settings = s
	c.Agent.Role = role
	c.Instructions = fmt.Sprintf("Gnat runtime facts: your role is %s. Coordination tools are on the gnat MCP server.", role)
	c.MCPURL = server.URL
	c.MCPToken = "fixture-token"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var reply strings.Builder
	if err := (Registry{}).Run(ctx, c, func(e map[string]any) error {
		if e["type"] == "text_delta" {
			reply.WriteString(text(e["text"]))
		}
		if e["type"] == "message" {
			t.Log(e["text"])
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called.Load() {
		t.Fatal("Adapter completed without calling its configured MCP tool:", reply.String())
	}
}
