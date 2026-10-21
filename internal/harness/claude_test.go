package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/events"
)

// fakeClaude stands in for claude: the message and options are its arguments.
func fakeClaude(mode string) {
	switch mode {
	case "echo":
		out, _ := json.Marshal(map[string]any{
			"type": "result", "is_error": false, "result": strings.Join(os.Args[1:], " ")})
		fmt.Println(string(out))
	case "long":
		out, _ := json.Marshal(map[string]any{"type": "result", "result": strings.Repeat("x", 1<<20)})
		fmt.Println(string(out))
	case "child":
		// A child holds the output open, as a tool would.
		_ = exec.Command("sleep", "30").Run()
	case "sleep":
		time.Sleep(30 * time.Second)
	}
}

func line(kind string, fields map[string]any, content ...any) string {
	event := map[string]any{"type": kind, "message": map[string]any{"content": content}}
	for k, v := range fields {
		event[k] = v
	}
	out, _ := json.Marshal(event)
	return string(out)
}

func j(v any) string {
	out, _ := json.Marshal(v)
	return string(out)
}

type m = map[string]any

func TestClaudeTextThinkingAndToolCalls(t *testing.T) {
	got := Translate(line("assistant", nil,
		m{"type": "thinking", "thinking": "hm", "signature": "x"},
		m{"type": "thinking", "thinking": "", "signature": strings.Repeat("x", 1000)},
		m{"type": "text", "text": "Let me look."},
		m{"type": "tool_use", "id": "t1", "name": "Bash", "input": m{"command": "ls"}},
	))
	assertEvents(t, got, []events.Event{
		events.NewThought("hm"), events.NewMessage("Let me look."),
		events.NewToolCall("t1", "Bash", m{"command": "ls"}, ""),
	})
}

func TestClaudeToolResults(t *testing.T) {
	got := Translate(line("user", nil,
		m{"type": "tool_result", "tool_use_id": "t1", "content": "a\nb"},
		m{"type": "tool_result", "tool_use_id": "t2", "is_error": true,
			"content": []any{m{"type": "text", "text": "boom"}, m{"type": "image"}}},
	))
	assertEvents(t, got, []events.Event{
		events.NewToolResult("t1", "a\nb", false), events.NewToolResult("t2", "boom\n[image]", true),
	})
}

func TestClaudeSubagentEventsNameTheirToolCall(t *testing.T) {
	got := Translate(line("assistant", m{"parent_tool_use_id": "t1"}, m{"type": "text", "text": "a"}))
	assertEvents(t, got, []events.Event{events.NewMessage("a").WithParent("t1")})
	got = Translate(line("user", m{"parent_tool_use_id": "t1"},
		m{"type": "tool_result", "tool_use_id": "t2", "content": "x"}))
	assertEvents(t, got, []events.Event{events.NewToolResult("t2", "x", false).WithParent("t1")})
}

func TestClaudeFinalResults(t *testing.T) {
	ok := j(m{"type": "result", "subtype": "success", "is_error": false, "result": "Done."})
	failed := j(m{"type": "result", "subtype": "error_during_execution", "is_error": true})
	limited := j(m{"type": "result", "is_error": true, "result": "rate limited"})
	assertEvents(t, Translate(ok), []events.Event{events.NewTurnEnd("Done.", "")})
	assertEvents(t, Translate(failed), []events.Event{events.NewTurnEnd("", "error_during_execution")})
	assertEvents(t, Translate(limited), []events.Event{events.NewTurnEnd("", "rate limited")})
}

func TestClaudeResultsReportEveryModelsUsage(t *testing.T) {
	result := m{"type": "result", "is_error": false, "result": "Done.", "total_cost_usd": 0.5,
		"usage": m{"input_tokens": 1, "output_tokens": 1},
		"modelUsage": m{
			"opus": m{"inputTokens": 2, "cacheReadInputTokens": 100,
				"cacheCreationInputTokens": 30, "outputTokens": 40},
			"haiku": m{"inputTokens": 3, "cacheReadInputTokens": 10, "outputTokens": 5}}}
	assertEvents(t, Translate(j(result)), []events.Event{
		usageEvent(n(5), n(110), n(30), n(45), ptr(0.5)),
		events.NewTurnEnd("Done.", "")})
	delete(result, "modelUsage")
	result["is_error"] = true
	assertEvents(t, Translate(j(result)), []events.Event{
		usageEvent(n(1), nil, nil, n(1), ptr(0.5)), events.NewTurnEnd("", "Done.")})
}

func TestClaudeOutputThatIsntAnEventIsAnError(t *testing.T) {
	assertEvents(t, Translate("Error: no auth\n"), []events.Event{events.NewError("Error: no auth")})
	assertEvents(t, Translate("\n"), nil)
	assertEvents(t, Translate(j(m{"type": "system", "subtype": "init"})), nil)
}

func fakeClaudeRunner(t *testing.T, mode string) (*claudeRunner, *collector) {
	t.Helper()
	t.Setenv("GNAT_FAKE_CLAUDE", mode)
	c := &collector{}
	runner := newClaudeRunner("", "", c.emit)
	runner.command = []string{os.Args[0]}
	return runner, c
}

func never() <-chan struct{} { return make(chan struct{}) }

func TestClaudeRunsATurnContinuingTheSessionUnlessNew(t *testing.T) {
	runner, c := fakeClaudeRunner(t, "echo")
	for _, new := range []bool{false, true} {
		if code, err := runner.Run("hi", new, never()); code != 0 || err != nil {
			t.Fatalf("Run = %d, %v", code, err)
		}
	}
	assertEvents(t, c.all(), []events.Event{events.NewTurnEnd("--continue hi", ""), events.NewTurnEnd("hi", "")})
}

func TestClaudeStopKillsTheTurnAndWhatItStarted(t *testing.T) {
	runner, _ := fakeClaudeRunner(t, "child")
	stop := make(chan struct{})
	time.AfterFunc(200*time.Millisecond, func() { close(stop); runner.Stop() })
	started := time.Now()
	if code, _ := runner.Run("x", false, stop); code == 0 {
		t.Error("a stopped turn exited 0")
	}
	if time.Since(started) > 10*time.Second {
		t.Error("the stop took too long")
	}
}

func TestClaudeAStopBeforeTheTurnStartsEndsItAtOnce(t *testing.T) {
	runner, _ := fakeClaudeRunner(t, "sleep")
	stop := make(chan struct{})
	close(stop)
	started := time.Now()
	if code, _ := runner.Run("x", false, stop); code == 0 {
		t.Error("a stopped turn exited 0")
	}
	if time.Since(started) > 10*time.Second {
		t.Error("the stop took too long")
	}
}

func TestClaudeMissingProgram(t *testing.T) {
	c := &collector{}
	runner := newClaudeRunner("", "", c.emit)
	runner.command = []string{"/nonexistent/claude"}
	if code, _ := runner.Run("x", false, never()); code != 127 {
		t.Errorf("exit = %d", code)
	}
	if c.all()[0]["type"] != "error" {
		t.Errorf("events = %v", c.all())
	}
}

func TestClaudeTakesItsModelAndEffortAsOptions(t *testing.T) {
	runner := newClaudeRunner("opus", "high", func(events.Event) {})
	got := runner.command[len(runner.command)-4:]
	want := []string{"--model", "opus", "--effort", "high"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("options = %v", got)
	}
}

func TestClaudeReadsLinesLongerThanAPipeBuffer(t *testing.T) {
	runner, c := fakeClaudeRunner(t, "long")
	if code, err := runner.Run("x", false, never()); code != 0 || err != nil {
		t.Fatalf("Run = %d, %v", code, err)
	}
	assertEvents(t, c.all(), []events.Event{events.NewTurnEnd(strings.Repeat("x", 1<<20), "")})
}
