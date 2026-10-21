package hook

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kkestell/gnat/internal/protocol"
)

func send(t *testing.T, f *fakeCall, args []string, stdin, sender string) (stdout, stderr string, status int) {
	t.Helper()
	f.install(t)
	var out, errOut bytes.Buffer
	status = runSend(args, sender, strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), status
}

func TestSendsAsTheCurrentAgent(t *testing.T) {
	f := &fakeCall{response: protocol.OK()}
	out, _, status := send(t, f, []string{"beta", "gamma", "--now", "-m", "hi"}, "", "alpha")
	if status != 0 || out != "sent to beta\nsent to gamma\n" {
		t.Errorf("status %d, out %q", status, out)
	}
	var want []protocol.Request
	for _, name := range []string{"beta", "gamma"} {
		want = append(want, protocol.Request{Op: protocol.OpSend, Agent: name, Text: "hi", Now: true, Sender: "alpha"})
	}
	if !reflect.DeepEqual(f.requests, want) {
		t.Errorf("requests = %+v", f.requests)
	}
}

func TestFlagsMayComeBeforeOrAfterTheAgents(t *testing.T) {
	for _, args := range [][]string{
		{"-m", "hi", "beta"}, {"beta", "-m", "hi"}, {"--message=hi", "beta"}, {"beta", "--message", "hi"},
	} {
		f := &fakeCall{response: protocol.OK()}
		if _, _, status := send(t, f, args, "", "alpha"); status != 0 {
			t.Errorf("%v: status %d", args, status)
		}
		want := []protocol.Request{{Op: protocol.OpSend, Agent: "beta", Text: "hi", Sender: "alpha"}}
		if !reflect.DeepEqual(f.requests, want) {
			t.Errorf("%v: requests = %+v", args, f.requests)
		}
	}
}

func TestSendReadsTheMessageFromStdinWithoutOne(t *testing.T) {
	f := &fakeCall{response: protocol.OK()}
	if _, _, status := send(t, f, []string{"beta"}, "from stdin\nsecond line\n", "alpha"); status != 0 {
		t.Fatalf("status %d", status)
	}
	if f.requests[0].Text != "from stdin\nsecond line\n" {
		t.Errorf("text = %q", f.requests[0].Text)
	}
}

func TestSendNeedsAnAgentTurn(t *testing.T) {
	f := &fakeCall{response: protocol.OK()}
	_, errOut, status := send(t, f, []string{"beta", "-m", "hi"}, "", "")
	if status != 1 || !strings.Contains(errOut, "GNAT_AGENT") || len(f.requests) != 0 {
		t.Errorf("status %d, err %q, requests %v", status, errOut, f.requests)
	}
}

func TestSendRefusesAnEmptyMessage(t *testing.T) {
	for _, args := range [][]string{{"beta", "-m", "  \n"}, {"beta", "-m", ""}, {"beta"}} {
		f := &fakeCall{response: protocol.OK()}
		_, errOut, status := send(t, f, args, "\n", "alpha")
		if status != 1 || !strings.Contains(errOut, "message is empty") || len(f.requests) != 0 {
			t.Errorf("%v: status %d, err %q", args, status, errOut)
		}
	}
}

func TestSendUsageErrorsExitTwo(t *testing.T) {
	for _, args := range [][]string{{"-m", "hi"}, {"beta", "--nope"}, {"beta", "-m"}} {
		f := &fakeCall{response: protocol.OK()}
		_, errOut, status := send(t, f, args, "", "alpha")
		if status != 2 || !strings.Contains(errOut, "usage:") {
			t.Errorf("%v: status %d, err %q", args, status, errOut)
		}
	}
}

func TestSendReportsAFailureAndStops(t *testing.T) {
	f := &fakeCall{err: errors.New("no such agent")}
	out, errOut, status := send(t, f, []string{"beta", "gamma", "-m", "hi"}, "", "alpha")
	if status != 1 || out != "" || errOut != "error: no such agent\n" || len(f.requests) != 1 {
		t.Errorf("status %d, out %q, err %q, requests %d", status, out, errOut, len(f.requests))
	}
}
