package render

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kkestell/gnat/internal/events"
)

func eq(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestText(t *testing.T) {
	eq(t, Render(events.NewMessage("hi")), "hi\n")
}

func TestToolCallSummarizesItsInput(t *testing.T) {
	eq(t, Render(events.NewToolCall("t1", "Bash", map[string]any{"command": "ls\npwd"}, "")), "→ Bash: ls …\n")
}

func TestToolCallWithoutATellingInputShowsItsTitle(t *testing.T) {
	e := events.NewToolCall("t1", "apply_patch", map[string]any{"patch": "..."}, "Edit hello.py")
	eq(t, Render(e), "→ apply_patch: Edit hello.py\n")
	eq(t, Render(events.NewToolCall("t1", "x", map[string]any{"a": 1}, "")), "→ x: {\"a\": 1}\n")
}

func TestSummaryEscapesLikePythonsJSON(t *testing.T) {
	in := map[string]any{"b": []any{1.5, nil, true}, "a": "é\"😀"}
	eq(t, Summary(events.NewToolCall("t", "x", in, "")), `{"a": "\u00e9\"\ud83d\ude00", "b": [1.5, null, true]}`)
}

func TestToolResultIsTruncated(t *testing.T) {
	var lines []string
	for i := range 8 {
		lines = append(lines, strconv.Itoa(i))
	}
	got := strings.Split(strings.TrimRight(Render(events.NewToolResult("t1", strings.Join(lines, "\n"), false)), "\n"), "\n")
	want := []string{"  ← 0", "  ← 1", "  ← 2", "  ← 3", "  ← 4", "    … 3 more lines"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
}

func TestToolError(t *testing.T) {
	eq(t, Render(events.NewToolResult("t1", "boom", true)), "  ✗ boom\n")
}

func TestEmptyToolResult(t *testing.T) {
	eq(t, Render(events.NewToolResult("t1", "", false)), "  ← \n")
}

func TestSubagentEventsAreIndented(t *testing.T) {
	eq(t, Render(events.NewMessage("a\nb").WithParent("t1")), "  | a\n  | b\n")
}

func TestFailedTurnEnd(t *testing.T) {
	eq(t, Render(events.NewTurnEnd("", "rate limited")), "[error: rate limited]\n")
}

func TestErrors(t *testing.T) {
	eq(t, Render(events.NewError("Error: no auth")), "Error: no auth\n")
}

func TestOtherEventsAreLeftOut(t *testing.T) {
	eq(t, Render(events.NewThought("hm")), "")
	eq(t, Render(events.NewTurnEnd("Done.", "")), "")
	eq(t, Render(events.Event{"type": "unknown"}), "")
}

func TestUsageEventsAreLeftOut(t *testing.T) {
	cost := 1.0
	eq(t, Render(events.NewUsage(events.UsageTotals{Cost: &cost})), "")
}

func TestUsageSummaryShowsWhatTheHarnessReported(t *testing.T) {
	i := func(n int64) *int64 { return &n }
	f := func(x float64) *float64 { return &x }
	eq(t, UsageSummary(events.UsageTotals{
		InputTokens: i(5), CacheReadTokens: i(12_300), CacheWriteTokens: i(700), OutputTokens: i(950), Cost: f(0.0421),
	}), "13k in (12.3k cached), 950 out, $0.0421")
	eq(t, UsageSummary(events.UsageTotals{InputTokens: i(0), OutputTokens: i(0)}), "0 in, 0 out")
	eq(t, UsageSummary(events.UsageTotals{Cost: f(3.5)}), "$3.50")
	eq(t, UsageSummary(events.UsageTotals{}), "")
}

func TestUsageSummaryOfAnEvent(t *testing.T) {
	e := events.Event{"type": "usage", "input_tokens": float64(5), "output_tokens": nil, "cost": 2.0}
	eq(t, UsageSummary(events.ParseUsage(e)), "5 in, $2.00")
}

func TestTokens(t *testing.T) {
	var got []string
	for _, n := range []int64{0, 999, 1000, 12_345, 999_949, 999_950, 2_340_000} {
		got = append(got, Tokens(n))
	}
	want := "0 999 1k 12.3k 999.9k 1M 2.3M"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

func TestLongLinesAreClipped(t *testing.T) {
	text := Render(events.NewToolCall("t1", "Read", map[string]any{"file_path": strings.Repeat("x", 300)}, ""))
	if got, want := utf8.RuneCountInString(strings.TrimRight(text, "\n")), len("→ Read: ")-2+MaxWidth; got != want {
		// "→" is one rune but three bytes.
		t.Fatalf("got %d runes, want %d", got, want)
	}
}

func TestSplitLines(t *testing.T) {
	got := splitLines("a\r\nb\rc\x0bd e\n", true)
	if want := []string{"a\r\n", "b\r", "c\x0b", "d ", "e\n"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	if got := splitLines("", false); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}
