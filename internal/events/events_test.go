package events

import (
	"encoding/json"
	"testing"
)

func marshal(t *testing.T, e Event) string {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestConstructorsMatchTheWireFormat(t *testing.T) {
	cases := []struct {
		event Event
		want  string
	}{
		{NewMessage("hi"), `{"text":"hi","type":"message"}`},
		{NewMessage("hi").WithParent("t1"), `{"parent":"t1","text":"hi","type":"message"}`},
		{NewMessage("hi").WithParent(""), `{"text":"hi","type":"message"}`},
		{NewThought("hm"), `{"text":"hm","type":"thought"}`},
		{NewToolCall("t1", "Bash", map[string]any{"command": "ls"}, ""), `{"id":"t1","input":{"command":"ls"},"name":"Bash","type":"tool_call"}`},
		{NewToolCall("t1", "x", map[string]any{}, "Edit"), `{"id":"t1","input":{},"name":"x","title":"Edit","type":"tool_call"}`},
		{NewToolResult("t1", "ok", false), `{"error":false,"id":"t1","output":"ok","type":"tool_result"}`},
		{NewTurnEnd("", ""), `{"error":null,"reply":null,"type":"turn_end"}`},
		{NewTurnEnd("Done.", "boom"), `{"error":"boom","reply":"Done.","type":"turn_end"}`},
		{NewError("oops"), `{"text":"oops","type":"error"}`},
		{NewUsage(UsageTotals{}), `{"cache_read_tokens":null,"cache_write_tokens":null,"cost":null,"input_tokens":null,"output_tokens":null,"type":"usage"}`},
		{NewContext(5, nil), `{"size":null,"type":"context","used":5}`},
		{NewLimits(map[string]LimitWindow{"five_hour": {Used: 0.07, ResetsAt: 1792779600}}), `{"type":"limits","windows":{"five_hour":{"resets_at":1792779600,"used":0.07}}}`},
	}
	for _, c := range cases {
		if got := marshal(t, c.event); got != c.want {
			t.Errorf("got %s, want %s", got, c.want)
		}
	}
}

func TestUsageRoundTrips(t *testing.T) {
	in, out, cost := int64(5), int64(7), 0.25
	e := NewUsage(UsageTotals{InputTokens: &in, OutputTokens: &out, Cost: &cost})
	var decoded Event
	if err := json.Unmarshal([]byte(marshal(t, e)), &decoded); err != nil {
		t.Fatal(err)
	}
	got := ParseUsage(decoded)
	if *got.InputTokens != 5 || *got.OutputTokens != 7 || *got.Cost != 0.25 || got.CacheReadTokens != nil {
		t.Fatalf("got %+v", got)
	}
	if got := ParseUsage(e); *got.InputTokens != 5 {
		t.Fatalf("got %+v", got)
	}
}

func TestContextAndLimitsRoundTrip(t *testing.T) {
	var decoded Event
	size := int64(1_000_000)
	if err := json.Unmarshal([]byte(marshal(t, NewContext(25_246, &size))), &decoded); err != nil {
		t.Fatal(err)
	}
	if got, ok := ParseContext(decoded); !ok || got.Used != 25_246 || *got.Size != size {
		t.Fatalf("got %+v", got)
	}
	if _, ok := ParseContext(Event{"type": Context}); ok {
		t.Fatal("a context event without used parsed")
	}
	windows := map[string]LimitWindow{"five_hour": {Used: 0.07, ResetsAt: 1792779600}, "seven_day": {Used: 0.49, ResetsAt: 1792983600}}
	decoded = nil
	if err := json.Unmarshal([]byte(marshal(t, NewLimits(windows))), &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["windows"].(map[string]any)["bogus"] = map[string]any{"used": 0.5}
	if got := ParseLimits(decoded); len(got) != 2 || got["seven_day"] != windows["seven_day"] {
		t.Fatalf("got %+v", got)
	}
}

func TestType(t *testing.T) {
	if (Event{"type": 3}).Type() != "" || (Event{}).Type() != "" || NewError("x").Type() != Error {
		t.Fatal("wrong type")
	}
}
