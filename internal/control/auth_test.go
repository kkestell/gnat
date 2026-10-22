package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kkestell/gnat/internal/protocol"
)

// An agent that attaches without its secret, or with one that isn't any agent's, is told so,
// and isn't attached as anybody.
func TestAttachingNeedsAnAgentsSecret(t *testing.T) {
	p := startPlane(t, t.TempDir(), "alpha", "beta")
	for name, secret := range map[string]string{"none": "", "forged": "guess", "a name": "alpha", "a prefix": "alpha-secre", "padded": "alpha-secret "} {
		conn := p.rawAgentConn()
		d := &fakeDaemon{t: t, conn: conn, r: bufioReader(conn)}
		d.send(protocol.Request{Op: protocol.OpAttach, Agent: "alpha", Secret: secret})
		if got := d.receive(); got.Type != protocol.LineError || got.Error != errUnauthorized {
			t.Errorf("%s: got %+v", name, got)
		}
	}
	// Nobody is attached, and a message to alpha just waits.
	p.send("alpha", "hello")
	if p.hasDaemon("alpha") || p.hasDaemon("beta") {
		t.Error("a daemon is attached")
	}
	if s := p.status("alpha"); s.Queued != 1 || s.State != "starting" {
		t.Errorf("status %+v", s)
	}
}

// What the request says about who it is has no effect: beta's secret attaches beta, however
// the request names alpha, and alpha's turns don't go to it.
func TestAttachingIsAsTheAgentWhoseSecretItIs(t *testing.T) {
	p := startPlane(t, t.TempDir(), "alpha", "beta")
	conn := p.rawAgentConn()
	d := &fakeDaemon{t: t, conn: conn, r: bufioReader(conn)}
	d.send(protocol.Request{Op: protocol.OpAttach, Agent: "alpha", Secret: p.secrets["beta"]})
	p.waitUntil("beta attached", func() bool { return p.hasDaemon("beta") })
	if p.hasDaemon("alpha") {
		t.Fatal("alpha is attached")
	}
	p.send("alpha", "for alpha")
	p.send("beta", "for beta")
	if got := d.receive().Text; got != "for beta" {
		t.Errorf("got %q", got)
	}
}

func TestAMessageIsFromTheAgentWhoseSecretItHas(t *testing.T) {
	p := startPlane(t, t.TempDir(), "alpha", "beta", "gamma")
	beta := p.attach("beta")
	// Alpha says it is gamma, and says nothing; it is alpha both times.
	for _, sender := range []string{"gamma", ""} {
		resp, err := protocol.CallAddr(p.agentAddr, protocol.Request{Op: protocol.OpSend, Agent: "beta", Text: "hi " + sender,
			Sender: sender, Secret: p.secrets["alpha"]}, 0)
		if err != nil || !resp.OK {
			t.Fatalf("%v, %v", resp, err)
		}
	}
	if got := beta.receive().Text; got != "Message from agent alpha:\n\nhi gamma" {
		t.Errorf("got %q", got)
	}
	beta.done(0, false)
	if got := beta.receive().Text; got != "Message from agent alpha:\n\nhi " {
		t.Errorf("got %q", got)
	}
	if strings.Contains(p.log("beta"), "gamma:") {
		t.Errorf("log: %s", p.log("beta"))
	}
}

func TestAnAgentOnTheNetworkNeedsItsSecretForEverythingItSends(t *testing.T) {
	p := startPlane(t, t.TempDir(), "alpha", "beta")
	for _, secret := range []string{"", "forged", "alpha"} {
		for _, req := range []protocol.Request{
			{Op: protocol.OpSend, Agent: "beta", Text: "x", Sender: "alpha"},
			{Op: protocol.OpSend, Agent: "beta", Text: "x"},
			{Op: protocol.OpTake, Agent: "beta"},
			{Op: protocol.OpOutput, Agent: "beta", Text: "x"},
			{Op: protocol.OpStatus, Agent: "beta"},
			{Op: protocol.OpWait, Agent: "beta"},
			{Op: protocol.OpStop, Agent: "beta"},
			{Op: protocol.OpGitHub, GitHub: &protocol.GitHubRequest{Action: protocol.PRList}},
		} {
			req.Secret = secret
			resp, err := protocol.CallAddr(p.agentAddr, req, 0)
			if err == nil || !strings.Contains(err.Error(), "unauthorized") {
				t.Errorf("%s with secret %q: %v, %v", req.Op, secret, resp, err)
			}
		}
	}
	if s := p.status("beta"); s.Queued != 0 {
		t.Errorf("a message got through: %+v", s)
	}
	if strings.Contains(p.log("beta"), "x") {
		t.Errorf("output got through: %q", p.log("beta"))
	}
}

func TestAgentsTakeAndReportAsThemselves(t *testing.T) {
	p := startPlane(t, t.TempDir(), "alpha", "beta")
	p.must(protocol.Request{Op: protocol.OpSend, Agent: "alpha", Text: "for alpha", Now: true})
	p.must(protocol.Request{Op: protocol.OpSend, Agent: "beta", Text: "for beta", Now: true})
	// Alpha asks for beta's urgent messages, and gets its own.
	resp, err := protocol.CallAddr(p.agentAddr, protocol.Request{Op: protocol.OpTake, Agent: "beta", Secret: p.secrets["alpha"]}, 0)
	if err != nil || len(resp.Messages) != 1 || resp.Messages[0].Text != "for alpha" {
		t.Fatalf("%+v, %v", resp, err)
	}
	if s := p.status("beta"); s.Queued != 1 {
		t.Errorf("beta's message was taken: %+v", s)
	}
	// And its output is its own.
	if _, err := protocol.CallAddr(p.agentAddr, protocol.Request{Op: protocol.OpOutput, Agent: "beta", Text: "from alpha\n", Secret: p.secrets["alpha"]}, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.log("alpha"), "from alpha") || strings.Contains(p.log("beta"), "from alpha") {
		t.Errorf("logs: alpha %q, beta %q", p.log("alpha"), p.log("beta"))
	}
}

func TestAgentsCantDoWhatOnlyTheOperatorCan(t *testing.T) {
	p := startPlane(t, t.TempDir(), "alpha", "beta")
	for _, op := range []string{protocol.OpStatus, protocol.OpWait, protocol.OpStop} {
		resp, err := protocol.CallAddr(p.agentAddr, protocol.Request{Op: op, Agent: "beta", Secret: p.secrets["alpha"]}, 0)
		if err == nil || !strings.Contains(err.Error(), "only for the operator") {
			t.Errorf("%s: %v, %v", op, resp, err)
		}
	}
	for _, op := range []string{"nonsense", "", "Send"} {
		if _, err := protocol.CallAddr(p.agentAddr, protocol.Request{Op: op, Agent: "beta", Secret: p.secrets["alpha"]}, 0); err == nil {
			t.Errorf("%q worked", op)
		}
	}
}

func TestTheOperatorsCommandsAreNotTheAgents(t *testing.T) {
	p := startPlane(t, t.TempDir(), "alpha", "beta")
	for _, req := range []protocol.Request{
		{Op: protocol.OpAttach, Agent: "alpha"},
		{Op: protocol.OpTake, Agent: "alpha"},
		{Op: protocol.OpOutput, Agent: "alpha", Text: "x"},
		{Op: protocol.OpGitHub, GitHub: &protocol.GitHubRequest{Action: protocol.PRList}, Secret: p.secrets["alpha"]},
		{Op: protocol.OpSend, Agent: "alpha", Text: "x", Sender: "beta"},
	} {
		resp, err := protocol.CallAddr(p.addr, req, 0)
		if err == nil || !strings.Contains(err.Error(), "only for agents") && !strings.Contains(err.Error(), "sender") {
			t.Errorf("%s: %v, %v", req.Op, resp, err)
		}
	}
	if s := p.status("alpha"); s.Queued != 0 {
		t.Errorf("a message got through: %+v", s)
	}
	// The operator's own work isn't affected.
	p.send("alpha", "from the operator")
	if s := p.status("alpha"); s.Queued != 1 {
		t.Errorf("status %+v", s)
	}
}

func TestTheOperatorsSocketIsTheOperatorsAlone(t *testing.T) {
	p := startPlane(t, t.TempDir(), "alpha")
	path := filepath.Join(t.TempDir(), "control.sock")
	if err := os.WriteFile(path, []byte("left by the last run"), 0o644); err != nil {
		t.Fatal(err)
	}
	ln, err := listenOperator(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.control.Serve(ctx, ln) }()
	t.Cleanup(func() { cancel(); <-done })
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket %v, %v", info, err)
	}
	// What `gnat` sends in the container goes by it, with no secret.
	if _, err := protocol.CallAddr(path, protocol.Request{Op: protocol.OpSend, Agent: "alpha", Text: "hi"}, 0); err != nil {
		t.Fatal(err)
	}
	resp, err := protocol.CallAddr(path, protocol.Request{Op: protocol.OpStatus, Agent: "alpha"}, 0)
	if err != nil || resp.Queued != 1 {
		t.Fatalf("%+v, %v", resp, err)
	}
	// Not the agents' requests, even with a secret.
	if _, err := protocol.CallAddr(path, protocol.Request{Op: protocol.OpTake, Agent: "alpha", Secret: p.secrets["alpha"]}, 0); err == nil {
		t.Error("an agent's request worked")
	}
}
