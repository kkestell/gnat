package harness

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/kkestell/gnat/internal/events"
	"github.com/kkestell/gnat/internal/layout"
	"github.com/kkestell/gnat/internal/state"
)

func TestMain(m *testing.M) {
	// The test binary stands in for claude and for an ACP server.
	switch {
	case os.Getenv("GNAT_FAKE_ACP") != "":
		fakeACPServer()
		return
	case os.Getenv("GNAT_FAKE_CLAUDE") != "":
		fakeClaude(os.Getenv("GNAT_FAKE_CLAUDE"))
		return
	}
	os.Exit(m.Run())
}

// collector is an Emit that records the events it gets, which can come from several goroutines.
type collector struct {
	mu     sync.Mutex
	events []events.Event
}

func (c *collector) emit(e events.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *collector) all() []events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]events.Event(nil), c.events...)
}

func (c *collector) ofType(kind string) []events.Event {
	var out []events.Event
	for _, e := range c.all() {
		if e.Type() == kind {
			out = append(out, e)
		}
	}
	return out
}

func (c *collector) last() events.Event {
	all := c.all()
	return all[len(all)-1]
}

func (c *collector) contains(e events.Event) bool {
	for _, got := range c.all() {
		if reflect.DeepEqual(got, e) {
			return true
		}
	}
	return false
}

func assertEvents(t *testing.T, got, want []events.Event) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		t.Fatalf("events:\n got %s\nwant %s", g, w)
	}
}

// decode parses a settings file.
func decode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestEveryHarnessFillsInTheInterface(t *testing.T) {
	if len(All()) == 0 {
		t.Fatal("no harnesses")
	}
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			h, err := Get(name)
			if err != nil || h.Name() != name {
				t.Fatalf("Get(%q) = %v, %v", name, h, err)
			}
			for field, value := range map[string]string{
				"program": h.Program(), "credential": h.Credential(), "credential name": h.CredentialName(),
				"credential source": h.CredentialSource(), "config dir": h.ConfigDir(),
			} {
				if value == "" {
					t.Errorf("%s is empty", field)
				}
			}
			if len(h.Efforts()) == 0 {
				t.Error("no efforts")
			}
			if !strings.HasPrefix(h.ConfigDir(), layout.AgentHome+"/") {
				t.Errorf("config dir %q isn't under the agent's home", h.ConfigDir())
			}
			for _, dir := range h.StateDirs() {
				if !strings.HasPrefix(dir, layout.AgentHome+"/") {
					t.Errorf("state dir %q isn't under the agent's home", dir)
				}
			}
			files := h.ConfigFiles("Be brief.", "model", h.Efforts()[0])
			found := false
			for _, data := range files {
				found = found || string(data) == "Be brief."
			}
			if !found {
				t.Error("no config file holds the instructions")
			}
			settings := decode(t, files["settings.json"])
			want := decode(t, jsonBytes(hookSettings()))
			if !reflect.DeepEqual(settings["hooks"], want["hooks"]) {
				t.Errorf("hooks = %v, want %v", settings["hooks"], want["hooks"])
			}
			if h.Runner("", "", func(events.Event) {}) == nil {
				t.Error("no runner")
			}
		})
	}
}

func TestTheDefaultHarnessIsRegistered(t *testing.T) {
	if _, err := Get(state.DefaultHarness); err != nil {
		t.Fatal(err)
	}
	if _, err := Get("nope"); err == nil {
		t.Fatal("Get found a harness that doesn't exist")
	}
}

func TestSettingsInstallTheHook(t *testing.T) {
	hook := hookSettings()["hooks"].(map[string]any)["PostToolUse"].([]any)[0].(map[string]any)
	if hook["matcher"] != "*" {
		t.Errorf("matcher = %v", hook["matcher"])
	}
	command := hook["hooks"].([]any)[0].(map[string]any)
	if command["command"] != strings.Join(layout.Command("hook"), " ") || command["type"] != "command" {
		t.Errorf("hook = %v", command)
	}
}

func TestTheHarnessWaitsTwiceAsLongOnTheHookAsTheHookDoesOnTheControlPlane(t *testing.T) {
	hooks := hookSettings()["hooks"].(map[string]any)["PostToolUse"].([]any)[0].(map[string]any)["hooks"].([]any)
	if got := hooks[0].(map[string]any)["timeout"]; got != layout.HookTimeoutSeconds*2 {
		t.Errorf("timeout = %v, want %d", got, layout.HookTimeoutSeconds*2)
	}
}

func TestOxSettings(t *testing.T) {
	ox, _ := Get("ox")
	files := ox.ConfigFiles("Build.", "openrouter:x/y", "high")
	if string(files["AGENTS.md"]) != "Build." {
		t.Errorf("AGENTS.md = %q", files["AGENTS.md"])
	}
	want := hookSettings()
	want["mode"], want["model"], want["effort"] = "auto", "openrouter:x/y", "high"
	assertSettings(t, files["settings.json"], want)
	delete(want, "model")
	delete(want, "effort")
	assertSettings(t, ox.ConfigFiles("x", "", "")["settings.json"], want)
}

func TestClaudeSettings(t *testing.T) {
	claude, _ := Get("claude")
	files := claude.ConfigFiles("Review.", "opus", "high")
	if string(files["CLAUDE.md"]) != "Review." {
		t.Errorf("CLAUDE.md = %q", files["CLAUDE.md"])
	}
	want := hookSettings()
	want["attribution"] = map[string]any{"commit": "", "pr": ""}
	assertSettings(t, files["settings.json"], want)
}

func assertSettings(t *testing.T, data []byte, want map[string]any) {
	t.Helper()
	if !reflect.DeepEqual(decode(t, data), decode(t, jsonBytes(want))) {
		t.Errorf("settings = %s, want %v", data, want)
	}
}

func TestSettingsAreIndentedWithoutEscapingHTML(t *testing.T) {
	got := string(jsonBytes(map[string]any{"a": "<b>&"}))
	if got != "{\n  \"a\": \"<b>&\"\n}\n" {
		t.Errorf("got %q", got)
	}
}

func TestOxKeepsItsSessionAndServerState(t *testing.T) {
	ox, _ := Get("ox")
	want := []string{layout.AgentHome + "/.local/share/ox", layout.AgentHome + "/.local/state/gnat"}
	if !reflect.DeepEqual(ox.StateDirs(), want) {
		t.Errorf("state dirs = %v", ox.StateDirs())
	}
	if acpSessionPath != layout.AgentHome+"/.local/state/gnat/acp-session" {
		t.Errorf("session path = %s", acpSessionPath)
	}
}

func usageEvent(input, cacheRead, cacheWrite, output *int64, cost *float64) events.Event {
	return events.NewUsage(events.UsageTotals{
		InputTokens: input, CacheReadTokens: cacheRead, CacheWriteTokens: cacheWrite,
		OutputTokens: output, Cost: cost,
	})
}

func n(v int64) *int64 { return &v }
