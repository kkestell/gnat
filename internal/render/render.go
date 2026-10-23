// Package render turns gnat's events (see the events package) into readable log lines.
package render

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kkestell/gnat/internal/events"
)

const (
	MaxWidth       = 200
	MaxResultLines = 5
)

// SummaryKeys are the input fields that best describe a call, for tools that have one.
var SummaryKeys = []string{"command", "file_path", "path", "pattern", "url", "query", "description"}

// Render is log text for one event; empty for events the log leaves out.
func Render(event events.Event) string {
	var text string
	switch event.Type() {
	case events.Message:
		text = strings.TrimRight(str(event["text"]), "\n") + "\n"
	case events.ToolCall:
		text = fmt.Sprintf("→ %s: %s\n", str(event["name"]), Summary(event))
	case events.ToolResult:
		text = result(event)
	case events.TurnEnd:
		if !truthy(event["error"]) {
			return ""
		}
		text = fmt.Sprintf("[error: %s]\n", str(event["error"]))
	case events.Error:
		text = strings.TrimRight(str(event["text"]), "\n") + "\n"
	default:
		return ""
	}
	// Events from a subagent name the tool call that started it.
	if truthy(event["parent"]) {
		var b strings.Builder
		for _, row := range splitLines(text, true) {
			b.WriteString("  | " + row)
		}
		text = b.String()
	}
	return text
}

// Summary is one line describing a tool call: its most telling input, else its title.
func Summary(event events.Event) string {
	input, _ := event["input"].(map[string]any)
	for _, key := range SummaryKeys {
		if s, ok := input[key].(string); ok {
			return clip(s)
		}
	}
	if truthy(event["title"]) {
		return clip(str(event["title"]))
	}
	if input == nil {
		input = map[string]any{}
	}
	return clip(dumps(input))
}

func result(event events.Event) string {
	lines := splitLines(strings.TrimRight(str(event["output"]), "\n"), false)
	if len(lines) == 0 {
		lines = []string{""}
	}
	marker := "←"
	if truthy(event["error"]) {
		marker = "✗"
	}
	var b strings.Builder
	for _, line := range lines[:min(len(lines), MaxResultLines)] {
		fmt.Fprintf(&b, "  %s %s\n", marker, clip(line))
	}
	if len(lines) > MaxResultLines {
		fmt.Fprintf(&b, "    … %d more lines\n", len(lines)-MaxResultLines)
	}
	return b.String()
}

func clip(text string) string {
	lines := splitLines(text, false)
	if len(lines) == 0 {
		lines = []string{""}
	}
	first := lines[0]
	if len(lines) > 1 {
		first += " …"
	}
	if utf8.RuneCountInString(first) <= MaxWidth {
		return first
	}
	return string([]rune(first)[:MaxWidth-1]) + "…"
}

// UsageSummary is one line of usage totals, such as "1.2M in (1.1M cached), 34k out,
// $1.23"; the fields the harness reported, empty if none.
func UsageSummary(u events.UsageTotals) string {
	var parts []string
	if u.InputTokens != nil || u.CacheReadTokens != nil || u.CacheWriteTokens != nil {
		var total int64
		for _, c := range []*int64{u.InputTokens, u.CacheReadTokens, u.CacheWriteTokens} {
			if c != nil {
				total += *c
			}
		}
		cached := ""
		if u.CacheReadTokens != nil && *u.CacheReadTokens != 0 {
			cached = fmt.Sprintf(" (%s cached)", Tokens(*u.CacheReadTokens))
		}
		parts = append(parts, Tokens(total)+" in"+cached)
	}
	if u.OutputTokens != nil {
		parts = append(parts, Tokens(*u.OutputTokens)+" out")
	}
	if u.Cost != nil {
		if *u.Cost >= 1 {
			parts = append(parts, fmt.Sprintf("$%.2f", *u.Cost))
		} else {
			parts = append(parts, fmt.Sprintf("$%.4f", *u.Cost))
		}
	}
	return strings.Join(parts, ", ")
}

// ContextSummary is how full a context window is, such as "context 23% of 1M", or
// "context 230k" if its size isn't known.
func ContextSummary(c events.ContextWindow) string {
	if c.Size == nil || *c.Size <= 0 {
		return "context " + Tokens(c.Used)
	}
	return fmt.Sprintf("context %s of %s", Percent(float64(c.Used)/float64(*c.Size)), Tokens(*c.Size))
}

// Percent is a fraction as a whole percentage, such as 23%, or <1% for a little.
func Percent(fraction float64) string {
	if fraction > 0 && fraction < 0.005 {
		return "<1%"
	}
	return fmt.Sprintf("%.0f%%", fraction*100)
}

// limitNames are the short names of usage limit windows, in the order they're shown;
// others follow by name.
var limitNames = map[string]string{"five_hour": "5h", "seven_day": "7d"}

// LimitsSummary is usage limits at now, in now's time zone, such as "5h 7% (resets
// 13:20), 7d 49% (resets Sun 22:00)"; a window that has reset since is left out, and
// with it, empty if every one has.
func LimitsSummary(windows map[string]events.LimitWindow, now time.Time) string {
	names := make([]string, 0, len(windows))
	for name := range windows {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := limitNames[names[i]], limitNames[names[j]]
		if (a == "") != (b == "") {
			return a != ""
		}
		return names[i] < names[j]
	})
	var parts []string
	for _, name := range names {
		w := windows[name]
		resets := time.Unix(w.ResetsAt, 0).In(now.Location())
		if !resets.After(now) {
			continue
		}
		label := limitNames[name]
		if label == "" {
			label = strings.ReplaceAll(name, "_", " ")
		}
		parts = append(parts, fmt.Sprintf("%s %s (resets %s)", label, Percent(w.Used), when(resets, now)))
	}
	return strings.Join(parts, ", ")
}

// when is a time to come, as briefly as now allows: 13:20 today, Sun 22:00 within the
// week, and Nov 2 22:00 past that.
func when(t, now time.Time) string {
	y, m, d := now.Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	switch {
	case t.Before(midnight.AddDate(0, 0, 1)):
		return t.Format("15:04")
	case t.Before(midnight.AddDate(0, 0, 7)):
		return t.Format("Mon 15:04")
	}
	return t.Format("Jan 2 15:04")
}

// Tokens is a token count, such as 950, 12.3k, or 1.2M.
func Tokens(count int64) string {
	switch {
	case count < 1000:
		return strconv.FormatInt(count, 10)
	case count < 999_950:
		return strings.Replace(fmt.Sprintf("%.1fk", float64(count)/1000), ".0k", "k", 1)
	}
	return strings.Replace(fmt.Sprintf("%.1fM", float64(count)/1_000_000), ".0M", "M", 1)
}

// truthy is Python's truthiness for decoded JSON: what a log line shows only when set.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// str is Python's str() of decoded JSON.
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	}
	return dumps(v)
}

// splitLines is Python's str.splitlines: it splits on every Unicode line boundary Python
// knows, not just "\n".
func splitLines(s string, keepEnds bool) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		end := i + size
		switch r {
		case '\r':
			if end < len(s) && s[end] == '\n' {
				end++
			}
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		default:
			i = end
			continue
		}
		if keepEnds {
			lines = append(lines, s[start:end])
		} else {
			lines = append(lines, s[start:i])
		}
		start, i = end, end
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// dumps is v as Python's json.dumps writes it: ", " and ": " separators, non-ASCII
// escaped, keys sorted (decoded objects have no order).
func dumps(v any) string {
	var b strings.Builder
	writeJSON(&b, v)
	return b.String()
}

func writeJSON(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case string:
		writeString(b, x)
	case float64:
		if x == float64(int64(x)) {
			b.WriteString(strconv.FormatInt(int64(x), 10))
		} else {
			b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
		}
	case []any:
		b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writeJSON(b, item)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			writeString(b, k)
			b.WriteString(": ")
			writeJSON(b, x[k])
		}
		b.WriteByte('}')
	default:
		// A Go type from the harness code: round-trip it into the types above.
		data, err := json.Marshal(v)
		if err != nil {
			fmt.Fprintf(b, "%v", v)
			return
		}
		var decoded any
		if json.Unmarshal(data, &decoded) != nil {
			fmt.Fprintf(b, "%v", v)
			return
		}
		writeJSON(b, decoded)
	}
}

func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20 || (r > 0x7e && r < 0x10000):
			if r == 0x7f {
				b.WriteRune(r)
			} else {
				fmt.Fprintf(b, `\u%04x`, r)
			}
		case r >= 0x10000:
			r -= 0x10000
			fmt.Fprintf(b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}
