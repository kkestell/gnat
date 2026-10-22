package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/layout"
	"github.com/kkestell/gnat/internal/protocol"
)

// fakeCall records the requests the hook or send makes and answers each with response,
// or its error.
type fakeCall struct {
	requests []protocol.Request
	timeouts []time.Duration
	response *protocol.Response
	err      error
}

func (f *fakeCall) install(t *testing.T) {
	old := call
	call = func(req protocol.Request, timeout time.Duration) (*protocol.Response, error) {
		f.requests = append(f.requests, req)
		f.timeouts = append(f.timeouts, timeout)
		return f.response, f.err
	}
	t.Cleanup(func() { call = old })
}

func str(s string) *string { return &s }

func runHookWith(t *testing.T, f *fakeCall) (stdout, stderr string, status int) {
	t.Helper()
	f.install(t)
	var out, errOut bytes.Buffer
	status = runHook("alpha", &out, &errOut)
	want := []protocol.Request{{Op: protocol.OpTake, Agent: "alpha"}}
	if !reflect.DeepEqual(f.requests, want) {
		t.Fatalf("requests = %+v", f.requests)
	}
	if f.timeouts[0] != layout.HookTimeoutSeconds*time.Second {
		t.Errorf("timeout = %v", f.timeouts[0])
	}
	return out.String(), errOut.String(), status
}

func TestInjectsMessagesIntoTheTurn(t *testing.T) {
	out, _, status := runHookWith(t, &fakeCall{response: &protocol.Response{OK: true, Messages: []protocol.TakenMessage{
		{Text: "a"}, {Text: "b", Sender: str("beta")}}}})
	if status != 0 {
		t.Fatalf("status = %d", status)
	}
	var parsed struct {
		Output struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if parsed.Output.Event != "PostToolUse" {
		t.Errorf("event = %q", parsed.Output.Event)
	}
	context := parsed.Output.Context
	if !strings.Contains(context, "swarm operator, received while you were working:\n\na") {
		t.Errorf("context = %q", context)
	}
	if !strings.HasSuffix(context, "agent beta, received while you were working:\n\nb") {
		t.Errorf("context = %q", context)
	}
}

func TestMessagesAreNotHTMLEscaped(t *testing.T) {
	out, _, _ := runHookWith(t, &fakeCall{response: &protocol.Response{OK: true, Messages: []protocol.TakenMessage{
		{Text: "a <b> & c"}}}})
	if !strings.Contains(out, "a <b> & c") {
		t.Errorf("output = %q", out)
	}
}

func TestNoMessagesPrintsNothing(t *testing.T) {
	out, errOut, status := runHookWith(t, &fakeCall{response: &protocol.Response{OK: true}})
	if out != "" || errOut != "" || status != 0 {
		t.Errorf("out %q, err %q, status %d", out, errOut, status)
	}
}

func TestAnUnreachableControlPlaneDoesntBreakTheTurn(t *testing.T) {
	out, errOut, status := runHookWith(t, &fakeCall{err: errors.New("refused")})
	if out != "" || !strings.Contains(errOut, "refused") || status != 0 {
		t.Errorf("out %q, err %q, status %d", out, errOut, status)
	}
}

func TestTheHookNeedsAnAgent(t *testing.T) {
	var out, errOut bytes.Buffer
	if status := runHook("", &out, &errOut); status != 1 || !strings.Contains(errOut.String(), "GNAT_AGENT") {
		t.Errorf("status %d, err %q", status, errOut.String())
	}
}
