package harness

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"github.com/kkestell/gnat/internal/v2/store"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

//go:embed testdata/rpc.py
var rpcFixture string

func installRPCFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"codex", "opencode"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(rpcFixture), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GNAT_TEST_TRACE", filepath.Join(dir, "trace"))
	t.Setenv("GNAT_TEST_CHILD", filepath.Join(dir, "child"))
	return dir
}
func TestNativeAdaptersSessionsAndEvents(t *testing.T) {
	for _, name := range []string{"codex", "opencode"} {
		t.Run(name, func(t *testing.T) {
			dir := installRPCFixture(t)
			a := Registry{}
			c := fixtureClaim(dir, "first", "")
			c.Agent.Settings.Harness = name
			c.Instructions = "identity facts"
			c.Sender = "teammate"
			c.MCPURL = "http://127.0.0.1:9999/mcp"
			c.MCPToken = "binding-1"
			if err := a.Probe(context.Background(), c.Agent.Settings); err != nil {
				t.Fatal(err)
			}
			var events []map[string]any
			emit := func(v map[string]any) error { events = append(events, v); return nil }
			if err := a.Run(context.Background(), c, emit); err != nil {
				t.Fatal(err)
			}
			if events[0]["type"] != "session" || events[0]["session_ref"] != "fixture-"+name {
				t.Fatal(events)
			}
			var messages, tools, results, usage int
			for _, e := range events {
				switch e["type"] {
				case "message":
					t.Fatal("duplicated streamed text", events)
				case "text_delta":
					messages++
					if e["text"] == "wrong turn" {
						t.Fatal("crossed turn")
					}
				case "tool_call":
					tools++
				case "tool_result":
					results++
				case "usage":
					usage++
				}
			}
			if messages < 1 || tools != 1 || results != 1 || usage != 1 {
				t.Fatal(events)
			}
			u := events[len(events)-1]
			if u["input_tokens"] != float64(25) || u["output_tokens"] != float64(20) {
				t.Fatal("cumulative usage or wrong per-turn accounting", u)
			}
			if _, ok := u["cache_creation_input_tokens"]; ok {
				t.Fatal("unknown metric synthesized", u)
			}
			c.SessionRef = "fixture-" + name
			c.MCPToken = "binding-2"
			c.Text = "follow-up"
			if err := a.Run(context.Background(), c, emit); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "trace"))
			if err != nil {
				t.Fatal(err)
			}
			trace := string(data)
			for _, want := range []string{dir, "identity facts", "binding-1", "binding-2", "Message from teammate:", "follow-up", c.Agent.Settings.Model} {
				if !strings.Contains(trace, want) {
					t.Fatalf("missing %q in trace", want)
				}
			}
			resume := "thread/resume"
			if name == "opencode" {
				resume = "session/resume"
			}
			if !strings.Contains(trace, resume) {
				t.Fatal("did not resume exact session")
			}
			if name == "opencode" && !strings.Contains(trace, `"optionId": "allow"`) {
				t.Fatal("permission request unanswered")
			}
			caps, err := a.Capabilities(name)
			if err != nil || !caps.Resume || !caps.HTTPMCP || caps.MidTurnDelivery {
				t.Fatal(caps, err)
			}
			for _, mode := range []string{"bad-resume", "wrong-session", "invalid", "eof", "failed"} {
				t.Setenv("GNAT_RPC_MODE", mode)
				if err := a.Run(context.Background(), c, emit); err == nil {
					t.Fatal("accepted failure", mode)
				}
			}
			t.Setenv("GNAT_RPC_MODE", "")
			if err := a.Run(context.Background(), c, func(map[string]any) error { return errors.New("disk failure") }); err == nil || !strings.Contains(err.Error(), "disk failure") {
				t.Fatal("lost persistence failure", err)
			}
			c.SessionRef = ""
			if err := a.Run(context.Background(), c, emit); err != nil {
				t.Fatal("new conversation failed", err)
			}
		})
	}
}
func TestNativeAdapterDiscoveryErrors(t *testing.T) {
	installRPCFixture(t)
	a := Registry{}
	for _, name := range []string{"codex", "opencode"} {
		s := fixtureClaim("", "", "").Agent.Settings
		s.Harness = name
		if name == "codex" {
			t.Setenv("GNAT_RPC_MODE", "unauthenticated")
			if err := a.Probe(context.Background(), s); err == nil || !strings.Contains(err.Error(), "codex login") {
				t.Fatal(err)
			}
			t.Setenv("GNAT_RPC_MODE", "")
			s.Model = "missing"
			if err := a.Probe(context.Background(), s); err == nil {
				t.Fatal("unknown model accepted")
			}
			s.Model = "fixture-model"
			s.Effort = "missing"
			if err := a.Probe(context.Background(), s); err == nil {
				t.Fatal("unknown effort accepted")
			}
		} else {
			t.Setenv("GNAT_RPC_MODE", "incompatible")
			if err := a.Probe(context.Background(), s); err == nil {
				t.Fatal("incompatible protocol accepted")
			}
		}
	}
	if err := a.Probe(context.Background(), store.Settings{Harness: "ox", Model: "fixture", Effort: "high"}); err == nil {
		t.Fatal("unsupported provider accepted")
	}
	if _, err := a.Capabilities("ox"); err == nil {
		t.Fatal("ox supported")
	}
}
func TestNativeAdaptersFailedTurnUsage(t *testing.T) {
	for _, name := range []string{"codex", "opencode"} {
		t.Run(name, func(t *testing.T) {
			dir := installRPCFixture(t)
			t.Setenv("GNAT_RPC_MODE", "failed")
			c := fixtureClaim(dir, "fail", "")
			c.Agent.Settings.Harness = name
			var usage []map[string]any
			err := (Registry{}).Run(context.Background(), c, func(e map[string]any) error {
				if e["type"] == "usage" {
					usage = append(usage, e)
				}
				return nil
			})
			if err == nil {
				t.Fatal("failed turn accepted")
			}
			if len(usage) != 1 || usage[0]["input_tokens"] != float64(25) || usage[0]["output_tokens"] != float64(20) {
				t.Fatal("lost supplied usage for failed turn", usage)
			}
		})
	}
}
func TestNativeAdapterCancellationAndChildren(t *testing.T) {
	for _, name := range []string{"codex", "opencode"} {
		for _, mode := range []string{"block", "ignore-cancel", "orphan"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				dir := installRPCFixture(t)
				t.Setenv("GNAT_RPC_MODE", mode)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				c := fixtureClaim(dir, "block", "")
				c.Agent.Settings.Harness = name
				done := make(chan error, 1)
				go func() { done <- (Registry{}).Run(ctx, c, func(map[string]any) error { return nil }) }()
				deadline := time.Now().Add(5 * time.Second)
				var pid int
				for time.Now().Before(deadline) {
					data, err := os.ReadFile(filepath.Join(dir, "child"))
					if err == nil {
						_, _ = fmt.Sscanf(string(data), "%d", &pid)
						if pid > 0 {
							break
						}
					}
					time.Sleep(5 * time.Millisecond)
				}
				if pid == 0 {
					t.Fatal("child did not start")
				}
				if mode != "orphan" {
					cancel()
				}
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("expected cancellation/failure")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("process did not settle")
				}
				if mode != "orphan" {
					data, _ := os.ReadFile(filepath.Join(dir, "trace"))
					want := "turn/interrupt"
					if name == "opencode" {
						want = "session/cancel"
					}
					if !strings.Contains(string(data), want) {
						t.Fatal("no provider cancellation")
					}
				}
				deadline = time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					if syscall.Kill(pid, 0) == syscall.ESRCH {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("child survived", pid)
			})
		}
	}
}
func TestOpenCodeOptions(t *testing.T) {
	dir := installRPCFixture(t)
	c := fixtureClaim(dir, "", "")
	c.Agent.Settings.Harness = "opencode"
	c.Agent.Settings.Model = "missing"
	if err := (Registry{}).Run(context.Background(), c, func(map[string]any) error { return nil }); err == nil || !strings.Contains(err.Error(), "does not advertise model") {
		t.Fatal(err)
	}
	c.Agent.Settings.Model = "fixture-model"
	c.Agent.Settings.Effort = "missing"
	if err := (Registry{}).Run(context.Background(), c, func(map[string]any) error { return nil }); err == nil || !strings.Contains(err.Error(), "does not advertise effort") {
		t.Fatal(err)
	}
}
