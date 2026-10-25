package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/v2/core"
	"github.com/kkestell/gnat/internal/v2/store"
	"github.com/kkestell/gnat/internal/v2/workspace"
)

type adapter struct{ claims chan *store.Claim }

func (a adapter) Probe(context.Context, store.Settings) error { return nil }
func (a adapter) Run(ctx context.Context, c *store.Claim, _ func(map[string]any) error) error {
	a.claims <- c
	<-ctx.Done()
	return ctx.Err()
}
func TestMCPBindingsToolsAndDurableMessages(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := db.OpenWorkspace(workspace.Workspace{Root: "/fixture", Name: "fixture"}, func() (store.Settings, error) {
		return store.Settings{Harness: "claude", Model: "fixture", Effort: "high"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	claims := make(chan *store.Claim, 4)
	service, err := core.New(db, adapter{claims})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if _, err = service.Mutate(v.ID, "initial", "send", "hello", ""); err != nil {
		t.Fatal(err)
	}
	var claim *store.Claim
	select {
	case claim = <-claims:
	case <-time.After(3 * time.Second):
		t.Fatal("no initial turn")
	}
	// A worker record is sufficient for scope/discovery checks; no process or Git needed.
	created, _, err := db.Coordinate(v.ID, v.Orchestrator.ID, "create_agent", store.Command{RequestID: "worker", Name: "worker", Prompt: "initial"}, "fixture-base", v.Orchestrator.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.WorkerState(v.ID, created.AgentID, "active", "worktree_created", ""); err != nil {
		t.Fatal(err)
	}
	if err = db.ReadyAgent(v.ID, created.AgentID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Coordinate(core.Binding{SwarmID: v.ID, AgentID: v.Orchestrator.ID}, "send_message", store.Command{RequestID: "start", AgentID: created.AgentID, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	var worker *store.Claim
	select {
	case worker = <-claims:
	case <-time.After(3 * time.Second):
		t.Fatal("no worker turn")
	}
	handler := Handler(db, service)
	call := func(token, method, params string) map[string]any {
		t.Helper()
		req := httptest.NewRequest("POST", "http://127.0.0.1/mcp", strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":%q,"params":%s}`, method, params)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("MCP status %d: %s", w.Code, w.Body.String())
		}
		var out map[string]any
		if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	init := call(claim.MCPToken, "initialize", `{"protocolVersion":"2025-06-18","clientInfo":{"name":"fixture","version":"1"},"capabilities":{}}`)
	if init["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatal("protocol mismatch")
	}
	for _, tc := range []struct {
		token string
		count int
	}{{claim.MCPToken, 6}, {worker.MCPToken, 3}} {
		result := call(tc.token, "tools/list", `{}`)
		if len(result["result"].(map[string]any)["tools"].([]any)) != tc.count {
			t.Fatal("incorrect role-specific discovery", result)
		}
	}
	unauthorized := call(worker.MCPToken, "tools/call", `{"name":"remove_agent","arguments":{"request_id":"bad","agent_id":"`+created.AgentID+`"}}`)
	if unauthorized["error"] == nil {
		t.Fatal("guessed management tool authorized")
	}
	spoof := call(worker.MCPToken, "tools/call", `{"name":"send_message","arguments":{"request_id":"spoof","agent_id":"`+v.Orchestrator.ID+`","text":"bad","sender":"user"}}`)
	if spoof["error"] == nil {
		t.Fatal("sender argument accepted")
	}
	body := `{"name":"send_message","arguments":{"request_id":"reply","agent_id":"` + v.Orchestrator.ID + `","text":"worker reply"}}`
	for range 2 {
		result := call(worker.MCPToken, "tools/call", body)
		if result["error"] != nil || result["result"].(map[string]any)["isError"] == true {
			t.Fatal("message rejected", result)
		}
	}
	got, _ := db.Snapshot(v.ID)
	count := 0
	for _, m := range got.Messages {
		if m.Text == "worker reply" {
			count++
			if m.Sender != created.AgentID || m.AgentID != v.Orchestrator.ID {
				t.Fatal("wrong attribution", m)
			}
		}
	}
	if count != 1 {
		t.Fatal("transport retry duplicated message")
	}
	req := httptest.NewRequest("POST", "http://127.0.0.1/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer unknown")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("unknown credential accepted")
	}
	req = httptest.NewRequest("GET", "http://127.0.0.1/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+claim.MCPToken)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 405 {
		t.Fatal("unsupported stream not refused")
	}
}
