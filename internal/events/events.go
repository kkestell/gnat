// Package events defines gnat's events: what an agent's harness does in a turn, in one
// format for every harness.
//
// Each harness translates its own output into these in the agent daemon, so the control
// plane, the readable logs, and the web UI see only this format. An event is a JSON
// object with a "type":
//
//   - message {text}: a complete block of the assistant's text.
//   - thought {text}: the assistant's reasoning; recorded, not shown.
//   - tool_call {id, name, title?, input}: a tool call; title is the harness's own
//     description of it, when it gives one.
//   - tool_result {id, error, output}: how the tool call with that id ended.
//   - turn_end {reply, error}: the harness's own report that the turn ended, with its
//     final reply and, if the turn failed, why. A turn that is stopped or crashes may
//     have none.
//   - usage {input_tokens, cache_read_tokens, cache_write_tokens, output_tokens, cost}:
//     what the harness used since its last usage event: input tokens neither read from
//     nor written to the prompt cache, those that were, output tokens, and the cost in
//     US dollars. A field the harness doesn't report is null.
//   - error {text}: something the harness printed that isn't an event, such as its
//     errors.
//
// An event from a subagent has a "parent": the id of the tool call that started it.
// The control plane records two more types of its own; see control.EventsPath.
//
// Events are maps rather than structs because they cross process boundaries as JSON
// and readers (render, the web UI) must tolerate fields they don't know.
package events

// An Event is a decoded JSON object with at least a "type".
type Event map[string]any

const (
	Message    = "message"
	Thought    = "thought"
	ToolCall   = "tool_call"
	ToolResult = "tool_result"
	TurnEnd    = "turn_end"
	Usage      = "usage"
	Error      = "error"
)

// UsageFields are a usage event's fields, which the control plane totals per agent.
var UsageFields = []string{"input_tokens", "cache_read_tokens", "cache_write_tokens", "output_tokens", "cost"}

// NewMessage is a complete block of the assistant's text.
func NewMessage(text string) Event { return Event{"type": Message, "text": text} }

// NewThought is the assistant's reasoning.
func NewThought(text string) Event { return Event{"type": Thought, "text": text} }

// NewToolCall is a tool call; title may be empty.
func NewToolCall(id, name string, input map[string]any, title string) Event {
	e := Event{"type": ToolCall, "id": id, "name": name, "input": input}
	if title != "" {
		e["title"] = title
	}
	return e
}

// NewToolResult is how the tool call with that id ended.
func NewToolResult(id, output string, isError bool) Event {
	return Event{"type": ToolResult, "id": id, "error": isError, "output": output}
}

// NewTurnEnd is the harness's report that a turn ended; an empty reply or failure is
// null.
func NewTurnEnd(reply, failure string) Event {
	return Event{"type": TurnEnd, "reply": nullable(reply), "error": nullable(failure)}
}

// NewUsage is what the harness used since its last usage event.
func NewUsage(u UsageTotals) Event {
	e := Event{"type": Usage}
	for field, v := range map[string]*int64{
		"input_tokens": u.InputTokens, "cache_read_tokens": u.CacheReadTokens,
		"cache_write_tokens": u.CacheWriteTokens, "output_tokens": u.OutputTokens,
	} {
		if v == nil {
			e[field] = nil
		} else {
			e[field] = *v
		}
	}
	if u.Cost == nil {
		e["cost"] = nil
	} else {
		e["cost"] = *u.Cost
	}
	return e
}

// NewError is something the harness printed that isn't an event.
func NewError(text string) Event { return Event{"type": Error, "text": text} }

// WithParent marks the event as a subagent's, naming the tool call that started it; an
// empty parent leaves it alone. It returns the event.
func (e Event) WithParent(parent string) Event {
	if parent != "" {
		e["parent"] = parent
	}
	return e
}

// Type is the event's type, empty if it has none or it isn't a string.
func (e Event) Type() string {
	s, _ := e["type"].(string)
	return s
}

// UsageTotals are token counts and cost; nil is a field not reported. It is the body of
// a usage event and what the control plane totals per agent.
type UsageTotals struct {
	InputTokens      *int64   `json:"input_tokens"`
	CacheReadTokens  *int64   `json:"cache_read_tokens"`
	CacheWriteTokens *int64   `json:"cache_write_tokens"`
	OutputTokens     *int64   `json:"output_tokens"`
	Cost             *float64 `json:"cost"`
}

// ParseUsage reads a usage event's fields, tolerating missing, null, and non-numeric
// ones as not reported.
func ParseUsage(e Event) UsageTotals {
	return UsageTotals{
		InputTokens:      count(e["input_tokens"]),
		CacheReadTokens:  count(e["cache_read_tokens"]),
		CacheWriteTokens: count(e["cache_write_tokens"]),
		OutputTokens:     count(e["output_tokens"]),
		Cost:             number(e["cost"]),
	}
}

func count(v any) *int64 {
	f := number(v)
	if f == nil {
		return nil
	}
	n := int64(*f)
	return &n
}

func number(v any) *float64 {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case int:
		f = float64(x)
	case int64:
		f = float64(x)
	default:
		return nil
	}
	return &f
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
