package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/events"
	"github.com/kkestell/gnat/internal/layout"
	"github.com/kkestell/gnat/internal/protocol"
	"github.com/kkestell/gnat/internal/recorded"
	"github.com/kkestell/gnat/internal/web"
)

func i64(n int64) *int64     { return &n }
func f64(x float64) *float64 { return &x }
func str(s string) *string   { return &s }

// testMaxRequestBytes, if set, is the line limit of the planes started.
var testMaxRequestBytes int

// plane is a control plane serving on localhost, as in its container.
type plane struct {
	t       *testing.T
	dir     string
	control *Control
	// addr is where the operator's requests go, and agentAddr where the agents', whose
	// secrets are secrets.
	addr, agentAddr string
	secrets         map[string]string
	cancel          context.CancelFunc
	done            chan struct{}
}

// startPlane starts a control plane for the named agents (default: alpha) with its logs
// and database in dir.
func startPlane(t *testing.T, dir string, agents ...string) *plane {
	t.Helper()
	if len(agents) == 0 {
		agents = []string{"alpha"}
	}
	db, err := Prepare(dir, agents)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(agents, db, nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	if testMaxRequestBytes > 0 {
		c.MaxRequestBytes = testMaxRequestBytes
	}
	secrets := map[string]string{}
	for _, agent := range agents {
		secrets[agent] = agent + "-secret"
	}
	if err := c.SetSecrets(secrets); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	agentsLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &plane{t: t, dir: dir, control: c, addr: ln.Addr().String(), agentAddr: agentsLn.Addr().String(),
		secrets: secrets, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); c.Serve(ctx, ln) }()
		go func() { defer wg.Done(); c.ServeAgents(ctx, agentsLn) }()
		wg.Wait()
	}()
	t.Cleanup(p.close)
	return p
}

func (p *plane) close() {
	p.cancel()
	<-p.done
}

// request makes a request as its op needs: what only agents can do goes to the agents'
// address, with the secret of the agent it is by, if the request has none of its own. A
// request that isn't the operator's and names no agent is sent as it is.
func (p *plane) request(req protocol.Request) (*protocol.Response, error) {
	addr := p.addr
	switch {
	case req.Secret != "" || req.Op == protocol.OpGitHub:
		addr = p.agentAddr
	case req.Op == protocol.OpTake || req.Op == protocol.OpOutput:
		addr, req.Secret = p.agentAddr, p.secrets[req.Agent]
	case req.Op == protocol.OpSend && req.Sender != "":
		// Whose message it is is the sender's secret's.
		addr, req.Secret = p.agentAddr, p.secrets[req.Sender]
	}
	return protocol.CallAddr(addr, req, 5*time.Second)
}

// must is a request that has to succeed.
func (p *plane) must(req protocol.Request) *protocol.Response {
	p.t.Helper()
	resp, err := p.request(req)
	if err != nil {
		p.t.Fatalf("%+v: %v", req, err)
	}
	return resp
}

func (p *plane) send(agent, text string) {
	p.t.Helper()
	p.must(protocol.Request{Op: protocol.OpSend, Agent: agent, Text: text})
}

func (p *plane) status(agent string) *protocol.Response {
	p.t.Helper()
	return p.must(protocol.Request{Op: protocol.OpStatus, Agent: agent})
}

func (p *plane) wait(agent string) *protocol.Response {
	p.t.Helper()
	return p.must(protocol.Request{Op: protocol.OpWait, Agent: agent})
}

func (p *plane) waitUntil(what string, predicate func() bool) {
	p.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !predicate() {
		if time.Now().After(deadline) {
			p.t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (p *plane) hasDaemon(agent string) bool {
	p.control.mu.Lock()
	defer p.control.mu.Unlock()
	return p.control.daemons[agent] != nil
}

func (p *plane) log(agent string) string {
	data, err := os.ReadFile(filepath.Join(p.dir, agent+".log"))
	if err != nil {
		p.t.Fatal(err)
	}
	return string(data)
}

// recorded is the events recorded for the web UI so far.
func (p *plane) recorded() []events.Event {
	p.t.Helper()
	data, err := os.ReadFile(filepath.Join(p.dir, EventsFile))
	if err != nil {
		p.t.Fatal(err)
	}
	var out []events.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var record struct{ Event events.Event }
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			p.t.Fatal(err)
		}
		out = append(out, record.Event)
	}
	return out
}

func (p *plane) recordedTypes() []string {
	var types []string
	for _, e := range p.recorded() {
		types = append(types, e.Type())
	}
	return types
}

func (p *plane) hasRecorded(want events.Event) bool {
	for _, e := range p.recorded() {
		if reflect.DeepEqual(e, want) {
			return true
		}
	}
	return false
}

// fakeDaemon is the control plane's peer, an agent daemon driven by hand.
type fakeDaemon struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func (p *plane) attach(agent string) *fakeDaemon {
	p.t.Helper()
	conn, err := net.DialTimeout("tcp", p.agentAddr, 5*time.Second)
	if err != nil {
		p.t.Fatal(err)
	}
	d := &fakeDaemon{t: p.t, conn: conn, r: bufio.NewReader(conn)}
	p.t.Cleanup(func() { conn.Close() })
	d.send(protocol.Request{Op: protocol.OpAttach, Agent: agent, Secret: p.secrets[agent]})
	return d
}

func (d *fakeDaemon) send(v any) {
	d.t.Helper()
	data, err := protocol.Encode(v)
	if err != nil {
		d.t.Fatal(err)
	}
	d.sendRaw(data)
}

func (d *fakeDaemon) sendRaw(data []byte) {
	d.t.Helper()
	if _, err := d.conn.Write(data); err != nil {
		d.t.Fatal(err)
	}
}

func (d *fakeDaemon) receive() protocol.Line {
	d.t.Helper()
	d.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := d.r.ReadBytes('\n')
	if err != nil {
		d.t.Fatalf("the control plane closed the connection: %v", err)
	}
	var l protocol.Line
	if err := json.Unmarshal(line, &l); err != nil {
		d.t.Fatal(err)
	}
	return l
}

func (d *fakeDaemon) event(e events.Event) {
	d.t.Helper()
	d.send(protocol.Line{Type: protocol.LineEvent, Event: e})
}

func (d *fakeDaemon) done(exit int, stopped bool) {
	d.t.Helper()
	d.send(protocol.Line{Type: protocol.LineDone, Exit: &exit, Stopped: stopped})
}

func (d *fakeDaemon) turnEnd(reply, failure string) {
	d.t.Helper()
	d.event(events.NewTurnEnd(reply, failure))
}

func requestErr(t *testing.T, err error, want string) {
	t.Helper()
	var re *protocol.RequestError
	if !errors.As(err, &re) || !strings.Contains(re.Message, want) {
		t.Fatalf("got error %v, want one containing %q", err, want)
	}
}

func TestMailboxPutsUrgentMessagesFirst(t *testing.T) {
	db, err := Connect(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	m := NewMailbox(db, "alpha")
	for _, put := range []struct {
		text       string
		now, front bool
	}{{"first", false, false}, {"second", false, false}, {"urgent", true, false}, {"zeroth", false, true}} {
		if err := m.Put(Message{Text: put.text}, put.now, put.front); err != nil {
			t.Fatal(err)
		}
	}
	type got struct {
		text   string
		urgent bool
	}
	var order []got
	for range 4 {
		msg, urgent, err := m.Get(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, got{msg.Text, urgent})
	}
	want := []got{{"urgent", true}, {"zeroth", false}, {"first", false}, {"second", false}}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("got %v, want %v", order, want)
	}
}

func TestMailboxGetWaitsForAMessage(t *testing.T) {
	db, _ := Connect(":memory:")
	m := NewMailbox(db, "alpha")
	cancel := make(chan struct{})
	close(cancel)
	if _, _, err := m.Get(cancel, nil); !errors.Is(err, ErrCancelled) {
		t.Fatalf("got %v", err)
	}
	got := make(chan Message)
	go func() {
		msg, _, _ := m.Get(nil, nil)
		got <- msg
	}()
	time.Sleep(20 * time.Millisecond)
	m.Put(Message{Text: "late", Sender: "beta", New: true}, false, false)
	select {
	case msg := <-got:
		if msg != (Message{Text: "late", Sender: "beta", New: true}) {
			t.Fatalf("got %+v", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
}

func TestTakeUrgentLeavesRegularMessages(t *testing.T) {
	db, _ := Connect(":memory:")
	m := NewMailbox(db, "alpha")
	m.Put(Message{Text: "later"}, false, false)
	m.Put(Message{Text: "a"}, true, false)
	m.Put(Message{Text: "b"}, true, false)
	first, _ := m.TakeUrgent()
	second, _ := m.TakeUrgent()
	msg, urgent, _ := m.Get(nil, nil)
	if !reflect.DeepEqual(first, []Message{{Text: "a"}, {Text: "b"}}) || len(second) != 0 || msg.Text != "later" || urgent {
		t.Fatalf("got %v, %v, %v, %v", first, second, msg, urgent)
	}
}

func TestWaitForPassesAgainWhenAnAgentWasWokenAfterItWasChecked(t *testing.T) {
	// alpha is idle when checked, then beta's turn messages it: its turn count moves on.
	idle := true
	statuses := []*protocol.Response{
		{Idle: &idle, Turns: 1}, {Idle: &idle, Turns: 1},
		{Idle: &idle, Turns: 2}, {Idle: &idle, Turns: 1},
		{Idle: &idle, Turns: 2}, {Idle: &idle, Turns: 1},
	}
	var requests []string
	useCall(t, func(_ context.Context, req protocol.Request) (*protocol.Response, error) {
		requests = append(requests, req.Agent)
		next := statuses[0]
		statuses = statuses[1:]
		return next, nil
	})
	results, err := WaitFor([]string{"alpha", "beta"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alpha", "beta", "alpha", "beta", "alpha", "beta"}; !reflect.DeepEqual(requests, want) {
		t.Fatalf("got %v", requests)
	}
	if results[0].Turns != 2 || results[1].Turns != 1 {
		t.Fatalf("got %d, %d", results[0].Turns, results[1].Turns)
	}
}

// useCall has the commands' requests go to f for the rest of the test.
func useCall(t *testing.T, f func(context.Context, protocol.Request) (*protocol.Response, error)) {
	old := call
	t.Cleanup(func() { call = old })
	call = f
}

// useCall has the commands' requests go to the control plane p.
func (p *plane) useCall() {
	useCall(p.t, func(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
		return protocol.CallContext(ctx, p.addr, req, 5*time.Second)
	})
}

func result(reply *string, exit *int, turns int) AgentResult {
	return AgentResult{Response: &protocol.Response{Reply: reply, Exit: exit, Turns: turns}}
}

func TestReportHeadsRepliesByAgentAndListsFailures(t *testing.T) {
	one, zero, two, killed := new(int), new(int), 2, -9
	_ = one
	a := result(str("hi\n"), zero, 1)
	a.Agent = "alpha"
	out, failures := Report([]AgentResult{a}, false)
	if out != "hi\n" || len(failures) != 0 {
		t.Fatalf("got %q, %v", out, failures)
	}

	named := func(name string, r AgentResult) AgentResult { r.Agent = name; return r }
	epsilon := result(nil, &killed, 1)
	epsilon.Stopped = true
	out, failures = Report([]AgentResult{
		named("alpha", result(str("hi"), zero, 1)), named("beta", result(nil, &two, 1)),
		named("gamma", result(nil, nil, 1)), named("delta", result(nil, nil, 0)), named("epsilon", epsilon),
	}, true)
	if want := "==> alpha <==\nhi\n==> beta <==\n==> gamma <==\n==> delta <==\n==> epsilon <==\n"; out != want {
		t.Fatalf("got %q", out)
	}
	want := []string{"beta: its last turn failed with status 2",
		"gamma: its last turn was interrupted by a restart or `gnat down`",
		"epsilon: its last turn was stopped"}
	if !reflect.DeepEqual(failures, want) {
		t.Fatalf("got %q", failures)
	}
}

func TestReportGivesTheHarnessReasonForAFailure(t *testing.T) {
	exit := 1
	r := result(nil, &exit, 1)
	r.Agent, r.Error = "alpha", str("rate limited")
	_, failures := Report([]AgentResult{r}, false)
	if want := []string{"alpha: its last turn failed with status 1: rate limited"}; !reflect.DeepEqual(failures, want) {
		t.Fatalf("got %q", failures)
	}
}

func TestWatchReplaysOnlyCurrentAgentsEarlierEvents(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, EventsFile),
		[]byte(`{"agent": "gone", "event": "old"}`+"\n"+`{"agent": "alpha", "event": "kept"}`+"\n"), 0o644)
	db, _ := Connect(":memory:")
	c, err := New([]string{"alpha"}, db, nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	records, stop := c.Watch()
	defer stop()
	select {
	case record := <-records:
		if !strings.Contains(record, `"kept"`) {
			t.Fatalf("got %s", record)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
	select {
	case record := <-records:
		t.Fatalf("unexpected %s", record)
	case <-time.After(50 * time.Millisecond):
	}
	// Then each new one.
	c.record("alpha", events.NewMessage("new"))
	select {
	case record := <-records:
		if !strings.Contains(record, `"new"`) {
			t.Fatalf("got %s", record)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
}

func TestControlServesTheWebUI(t *testing.T) {
	var _ web.Events = (*Control)(nil)
}

func TestATurnOverTheWire(t *testing.T) {
	p := startPlane(t, t.TempDir())
	daemon := p.attach("alpha")
	p.send("alpha", "hello")
	if got := daemon.receive(); !reflect.DeepEqual(got, (protocol.Line{Type: "turn", Text: "hello"})) {
		t.Fatalf("got %+v", got)
	}
	p.must(protocol.Request{Op: protocol.OpSend, Agent: "alpha", Text: "mid", Now: true})
	taken := p.must(protocol.Request{Op: protocol.OpTake, Agent: "alpha"}).Messages
	if len(taken) != 1 || taken[0].Text != "mid" || taken[0].Sender != nil {
		t.Fatalf("got %+v", taken)
	}
	daemon.event(events.NewMessage("hi"))
	daemon.turnEnd("hi", "")
	daemon.done(0, false)
	if reply := p.wait("alpha").Reply; reply == nil || *reply != "hi" {
		t.Fatalf("got %v", reply)
	}
	_, err := p.request(protocol.Request{Op: protocol.OpSend, Agent: "beta", Text: "x"})
	requestErr(t, err, "no agent 'beta'")
	if got, want := p.log("alpha"), ">>> hello\n>>> (mid-turn) mid\nhi\n\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	want := []string{"swarm_message", "swarm_message", "message", "turn_end", "swarm_done"}
	if got := p.recordedTypes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestWaitReturnsTheFinalReplyOnceTheAgentIsIdle(t *testing.T) {
	p := startPlane(t, t.TempDir())
	if s := p.status("alpha"); s.State != "starting" {
		t.Fatalf("got %s", s.State)
	}
	daemon := p.attach("alpha")
	p.waitUntil("idle", func() bool { return p.status("alpha").State == "idle" })
	p.send("alpha", "one")
	p.send("alpha", "two")
	for _, reply := range []string{"first", "second"} {
		daemon.receive()
		daemon.turnEnd(reply, "")
		daemon.done(0, false)
	}
	r := p.wait("alpha")
	if r.SinceActive == nil || *r.SinceActive < 0 {
		t.Fatalf("since_active %v", r.SinceActive)
	}
	r.SinceActive = nil
	zero, idle := 0, true
	want := &protocol.Response{OK: true, Idle: &idle, State: "idle", Turns: 2, Exit: &zero, Reply: str("second"),
		Usage: &events.UsageTotals{}}
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("got %+v, want %+v", r, want)
	}
}

func TestAFailedTurnKeepsTheHarnessReason(t *testing.T) {
	p := startPlane(t, t.TempDir())
	daemon := p.attach("alpha")
	p.send("alpha", "one")
	daemon.receive()
	daemon.turnEnd("", "rate limited")
	daemon.done(1, false)
	r := p.wait("alpha")
	if r.Exit == nil || *r.Exit != 1 || r.Reply != nil || r.Error == nil || *r.Error != "rate limited" {
		t.Fatalf("got %+v", r)
	}
}

func TestWaitTimesOutWhileTheAgentIsBusy(t *testing.T) {
	p := startPlane(t, t.TempDir())
	daemon := p.attach("alpha")
	p.send("alpha", "one")
	daemon.receive()
	r := p.must(protocol.Request{Op: protocol.OpWait, Agent: "alpha", Timeout: f64(0.1)})
	if *r.Idle || r.State != "busy" {
		t.Fatalf("got %+v", r)
	}
}

func TestWaitEndsWhenTheCallerHangsUp(t *testing.T) {
	p := startPlane(t, t.TempDir())
	daemon := p.attach("alpha")
	p.send("alpha", "one")
	daemon.receive()
	conn, err := net.Dial("tcp", p.addr)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := protocol.Encode(protocol.Request{Op: protocol.OpWait, Agent: "alpha"})
	conn.Write(data)
	time.Sleep(50 * time.Millisecond)
	conn.Close()
	// The control plane is still serving, and the wait was dropped rather than answered.
	p.status("alpha")
	daemon.turnEnd("ok", "")
	daemon.done(0, false)
	if p.wait("alpha").Turns != 1 {
		t.Fatal("turn didn't end")
	}
}

func TestADaemonThatAttachesMidTurnRestartedAndItsTurnWasInterrupted(t *testing.T) {
	p := startPlane(t, t.TempDir())
	first := p.attach("alpha")
	p.send("alpha", "one")
	p.send("alpha", "two")
	if got := first.receive().Text; got != "one" {
		t.Fatalf("got %q", got)
	}
	// A new connection without reporting done: the daemon restarted.
	second := p.attach("alpha")
	if got := second.receive().Text; got != "two" {
		t.Fatalf("got %q", got)
	}
	s := p.status("alpha")
	if s.State != "busy" || s.Turns != 1 || s.Exit != nil {
		t.Fatalf("got %+v", s)
	}
	if !strings.Contains(p.log("alpha"), "turn was interrupted") {
		t.Fatal("interruption not logged")
	}
	if !p.hasRecorded(events.Event{"type": "swarm_done", "exit": nil, "stopped": false}) {
		t.Fatalf("not recorded: %v", p.recorded())
	}
}

func TestQueuesAndStatusesOutliveTheControlPlane(t *testing.T) {
	dir := t.TempDir()
	first := startPlane(t, dir)
	daemon := first.attach("alpha")
	first.send("alpha", "one")
	daemon.receive()
	daemon.turnEnd("done one", "")
	daemon.done(0, false)
	first.wait("alpha")
	first.send("alpha", "two")
	daemon.receive()
	first.must(protocol.Request{Op: protocol.OpSend, Agent: "alpha", Text: "three", New: true})
	first.must(protocol.Request{Op: protocol.OpSend, Agent: "alpha", Text: "urgent", Now: true})
	first.close()

	// The swarm went down mid-turn and came back up, with an agent it didn't have.
	second := startPlane(t, dir, "alpha", "beta")
	s := second.status("alpha")
	if s.State != "starting" || s.Queued != 2 || s.Turns != 1 || s.Exit == nil || *s.Exit != 0 {
		t.Fatalf("got %+v", s)
	}
	if second.status("beta").Turns != 0 {
		t.Fatal("beta has turns")
	}
	daemon = second.attach("alpha")
	if got := daemon.receive(); !reflect.DeepEqual(got, (protocol.Line{Type: "turn", Text: "urgent"})) {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(second.log("alpha"), "turn was interrupted") {
		t.Fatal("interruption not logged")
	}
	if !second.hasRecorded(events.Event{"type": "swarm_done", "exit": nil, "stopped": false}) {
		t.Fatalf("not recorded: %v", second.recorded())
	}
	daemon.done(0, false)
	if got := daemon.receive(); !reflect.DeepEqual(got, (protocol.Line{Type: "turn", Text: "three", New: true})) {
		t.Fatalf("got %+v", got)
	}
}

func TestAMessageWaitsForADaemonThatDropped(t *testing.T) {
	p := startPlane(t, t.TempDir())
	p.attach("alpha").conn.Close()
	p.waitUntil("the daemon is gone", func() bool { return !p.hasDaemon("alpha") })
	p.send("alpha", "kept")
	if q := p.status("alpha").Queued; q != 1 {
		t.Fatalf("queued %d", q)
	}
	if got := p.attach("alpha").receive().Text; got != "kept" {
		t.Fatalf("got %q", got)
	}
}

func TestAMessageTakenAsTheDaemonDropsIsKept(t *testing.T) {
	// The daemon hangs up while the control plane is handing it a message.
	p := startPlane(t, t.TempDir())
	if err := p.control.mailboxes["alpha"].Put(Message{Text: "kept"}, false, false); err != nil {
		t.Fatal(err)
	}
	reading := make(chan struct{})
	close(reading)
	p.control.pushTurns("alpha", newDaemon(nil), reading)
	if q := p.status("alpha").Queued; q != 1 {
		t.Fatalf("queued %d", q)
	}
}

func TestAgentsMessageEachOther(t *testing.T) {
	p := startPlane(t, t.TempDir(), "alpha", "beta")
	p.must(protocol.Request{Op: protocol.OpSend, Agent: "beta", Text: "fix the tests", Sender: "alpha"})
	p.must(protocol.Request{Op: protocol.OpSend, Agent: "beta", Text: "now", Now: true, Sender: "alpha"})
	for _, c := range [][2]string{{"alpha", "alpha"}, {"beta", "gamma"}} {
		if _, err := p.request(protocol.Request{Op: protocol.OpSend, Agent: c[0], Text: "x", Sender: c[1]}); err == nil {
			t.Fatalf("%v was allowed", c)
		}
	}
	taken := p.must(protocol.Request{Op: protocol.OpTake, Agent: "beta"}).Messages
	if len(taken) != 1 || taken[0].Text != "now" || *taken[0].Sender != "alpha" {
		t.Fatalf("got %+v", taken)
	}
	if got := p.attach("beta").receive().Text; got != "Message from agent alpha:\n\nfix the tests" {
		t.Fatalf("got %q", got)
	}
	if got, want := p.log("beta"), ">>> (mid-turn) [from alpha] now\n>>> [from alpha] fix the tests\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	want := []events.Event{
		{"type": "swarm_sent", "to": "beta", "text": "fix the tests"},
		{"type": "swarm_sent", "to": "beta", "text": "now"},
		{"type": "swarm_message", "text": "now", "sender": "alpha", "new": false, "mid_turn": true},
		{"type": "swarm_message", "text": "fix the tests", "sender": "alpha", "new": false, "mid_turn": false},
	}
	if got := p.recorded(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestUsageIsTotalledPerAgent(t *testing.T) {
	dir := t.TempDir()
	first := startPlane(t, dir)
	daemon := first.attach("alpha")
	for _, turn := range []struct {
		text  string
		usage []events.UsageTotals
	}{
		{"one", []events.UsageTotals{{InputTokens: i64(10), OutputTokens: i64(5), Cost: f64(0.25)}, {Cost: f64(0.5)}}},
		{"two", []events.UsageTotals{{InputTokens: i64(1), CacheReadTokens: i64(2000)}}},
	} {
		first.send("alpha", turn.text)
		daemon.receive()
		for _, u := range turn.usage {
			daemon.event(events.NewUsage(u))
		}
		daemon.done(0, false)
	}
	first.wait("alpha")
	total := events.UsageTotals{InputTokens: i64(11), CacheReadTokens: i64(2000), OutputTokens: i64(5), Cost: f64(0.75)}
	if got := first.status("alpha").Usage; !reflect.DeepEqual(*got, total) {
		t.Fatalf("got %+v", got)
	}
	first.close()
	// The totals outlive the control plane.
	if got := startPlane(t, dir).status("alpha").Usage; !reflect.DeepEqual(*got, total) {
		t.Fatalf("got %+v", got)
	}
}

func TestRecordedWriteReadsTheStatusOfADownSwarm(t *testing.T) {
	dir := t.TempDir()
	p := startPlane(t, dir, "alpha", "beta")
	alpha, beta := p.attach("alpha"), p.attach("beta")
	p.send("alpha", "one")
	alpha.receive()
	alpha.event(events.NewUsage(events.UsageTotals{InputTokens: i64(10), OutputTokens: i64(5), Cost: f64(0.25)}))
	alpha.done(0, false)
	p.wait("alpha")
	p.send("beta", "one")
	beta.receive()
	p.send("beta", "queued")
	p.close()

	var out strings.Builder
	if err := recorded.Write(dir, []string{"alpha", "beta", "gamma"}, &out); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		got = append(got, map[string]any{"agent": m["agent"], "state": m["state"], "queued": m["queued"],
			"turns": m["turns"], "exit": m["exit"], "usage": m["usage"]})
	}
	none := map[string]any{"input_tokens": nil, "cache_read_tokens": nil, "cache_write_tokens": nil, "output_tokens": nil, "cost": nil}
	want := []map[string]any{
		{"agent": "alpha", "state": "down", "queued": 0.0, "turns": 1.0, "exit": 0.0,
			"usage": map[string]any{"input_tokens": 10.0, "cache_read_tokens": nil, "cache_write_tokens": nil, "output_tokens": 5.0, "cost": 0.25}},
		// Beta went down mid-turn, which coming back up reports as interrupted.
		{"agent": "beta", "state": "down", "queued": 1.0, "turns": 1.0, "exit": nil, "usage": none},
		{"agent": "gamma", "state": "down", "queued": 0.0, "turns": 0.0, "exit": nil, "usage": none},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRecordedWriteWithoutADatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-up")
	var out strings.Builder
	if err := recorded.Write(dir, []string{"alpha"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"turns":0`) {
		t.Fatalf("got %s", out.String())
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created %s: %v", dir, err)
	}
}

func TestStatusTellsHowLongSinceTheAgentWasLastActive(t *testing.T) {
	p := startPlane(t, t.TempDir())
	if p.status("alpha").SinceActive != nil {
		t.Fatal("active before anything happened")
	}
	daemon := p.attach("alpha")
	p.send("alpha", "one")
	daemon.receive()
	if s := p.status("alpha").SinceActive; s == nil || *s < 0 || *s >= 5 {
		t.Fatalf("got %v", s)
	}
}

func TestStopEndsOnlyARunningTurnAndKeepsTheQueue(t *testing.T) {
	p := startPlane(t, t.TempDir())
	daemon := p.attach("alpha")
	p.waitUntil("attached", func() bool { return p.hasDaemon("alpha") })
	// Nothing running: nothing to push, so it can't linger and kill the next turn.
	if s := p.must(protocol.Request{Op: protocol.OpStop, Agent: "alpha"}).Stopping; *s {
		t.Fatal("stopping with nothing running")
	}
	p.send("alpha", "one")
	p.send("alpha", "two")
	if got := daemon.receive().Text; got != "one" {
		t.Fatalf("got %q", got)
	}
	if s := p.must(protocol.Request{Op: protocol.OpStop, Agent: "alpha"}).Stopping; !*s {
		t.Fatal("not stopping")
	}
	if got := daemon.receive(); !reflect.DeepEqual(got, (protocol.Line{Type: "stop"})) {
		t.Fatalf("got %+v", got)
	}
	daemon.done(-9, true)
	if got := daemon.receive().Text; got != "two" {
		t.Fatalf("got %q", got)
	}
	s := p.status("alpha")
	if s.Turns != 1 || !s.Stopped || *s.Exit != -9 {
		t.Fatalf("got %+v", s)
	}
	daemon.done(0, false)
	if p.wait("alpha").Stopped {
		t.Fatal("still stopped")
	}
	if !strings.Contains(p.log("alpha"), "[turn stopped]") {
		t.Fatal("stop not logged")
	}
	exit := any(-9)
	if !p.hasRecorded(events.Event{"type": "swarm_done", "exit": float64(exit.(int)), "stopped": true}) {
		t.Fatalf("not recorded: %v", p.recorded())
	}
}

func TestNewSessionsKeepTheirSenderAndArePassedToTheAgent(t *testing.T) {
	p := startPlane(t, t.TempDir(), "alpha", "beta")
	p.must(protocol.Request{Op: protocol.OpSend, Agent: "alpha", Text: "a", New: true, Sender: "beta"})
	if _, err := p.request(protocol.Request{Op: protocol.OpSend, Agent: "alpha", Text: "b", New: true, Now: true}); err == nil {
		t.Fatal("new and now allowed")
	}
	if got := p.attach("alpha").receive(); !reflect.DeepEqual(got, (protocol.Line{Type: "turn", Text: "Message from agent beta:\n\na", New: true})) {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(p.log("alpha"), ">>> (new session) [from beta] a") {
		t.Fatalf("got %q", p.log("alpha"))
	}
}

func TestBadLinesFromADaemonAreAnsweredAndLogged(t *testing.T) {
	p := startPlane(t, t.TempDir())
	daemon := p.attach("alpha")
	daemon.sendRaw([]byte("not json\n"))
	if daemon.receive().Type != "error" {
		t.Fatal("not answered")
	}
	daemon.sendRaw([]byte(`{"type": "event", "event": "not an object"}` + "\n"))
	if daemon.receive().Type != "error" {
		t.Fatal("not answered")
	}
	daemon.sendRaw([]byte(`{"type": "done", "exit": 0}` + "\n"))
	if got := daemon.receive().Error; !strings.Contains(got, "no turn is running") {
		t.Fatalf("got %q", got)
	}
	// The connection still works.
	p.send("alpha", "one")
	if got := daemon.receive().Text; got != "one" {
		t.Fatalf("got %q", got)
	}
	if n := strings.Count(p.log("alpha"), "[bad message from the agent daemon"); n != 3 {
		t.Fatalf("logged %d", n)
	}
}

func TestAnEventLineOver64KiBReachesTheControlPlane(t *testing.T) {
	// A long thinking block makes one Claude event longer than a typical line limit.
	p := startPlane(t, t.TempDir())
	daemon := p.attach("alpha")
	p.send("alpha", "one")
	daemon.receive()
	daemon.event(events.NewThought(strings.Repeat("x", 200_000)))
	daemon.event(events.NewMessage("hi"))
	daemon.done(0, false)
	if p.wait("alpha").Turns != 1 {
		t.Fatal("turn didn't end")
	}
	if got, want := p.log("alpha"), ">>> one\nhi\n\n"; got != want {
		t.Fatalf("got %q", got)
	}
}

func TestALineOverTheLimitIsAnsweredWithAnError(t *testing.T) {
	testMaxRequestBytes = 1024
	t.Cleanup(func() { testMaxRequestBytes = 0 })
	p := startPlane(t, t.TempDir())
	_, err := p.request(protocol.Request{Op: protocol.OpSend, Agent: "alpha", Text: strings.Repeat("x", 4096)})
	requestErr(t, err, "bad request")
	if protocol.IsNoResponse(err) {
		t.Fatal("dropped rather than answered")
	}
	daemon := p.attach("alpha")
	daemon.event(events.NewMessage(strings.Repeat("x", 4096)))
	if daemon.receive().Type != "error" {
		t.Fatal("not answered")
	}
}

func TestMainStatusAndStopPrintLikePythons(t *testing.T) {
	p := startPlane(t, t.TempDir())
	p.useCall()

	var out, errOut strings.Builder
	if code := run([]string{"status", "alpha"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(out.String()), &line); err != nil {
		t.Fatal(err)
	}
	if line["agent"] != "alpha" || line["ok"] != true || line["state"] != "starting" || line["exit"] != nil || line["since_active"] != nil {
		t.Fatalf("got %v", line)
	}
	for _, k := range []string{"queued", "turns", "stopped", "reply", "error", "usage"} {
		if _, ok := line[k]; !ok {
			t.Errorf("no %s", k)
		}
	}

	out.Reset()
	if code := run([]string{"stop", "alpha"}, &out, &errOut); code != 0 || out.String() != "alpha: not running a turn\n" {
		t.Fatalf("exit %d: %q", code, out.String())
	}

	// send takes flags after the positionals, as argparse does, and --wait times out
	// while the agent has no daemon.
	out.Reset()
	if code := run([]string{"send", "hi", "alpha", "--wait", "--timeout", "0.1"}, &out, &errOut); code != layout.TimeoutStatus {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errOut.String(), "timed out waiting for alpha") {
		t.Fatalf("got %q", errOut.String())
	}
	if q := p.status("alpha").Queued; q != 1 {
		t.Fatalf("queued %d", q)
	}
}

func TestMainWaitAnyReturnsOnTheFirstAgentToFinish(t *testing.T) {
	p := startPlane(t, t.TempDir(), "alpha", "beta", "gamma")
	p.useCall()
	daemons := map[string]*fakeDaemon{}
	for _, agent := range []string{"alpha", "beta", "gamma"} {
		daemons[agent] = p.attach(agent)
		p.send(agent, "work")
		daemons[agent].receive()
	}

	type ran struct {
		code        int
		out, errOut string
	}
	waitAny := func(args ...string) <-chan ran {
		finished := make(chan ran, 1)
		go func() {
			var out, errOut strings.Builder
			code := run(append([]string{"wait", "--any"}, args...), &out, &errOut)
			finished <- ran{code, out.String(), errOut.String()}
		}()
		return finished
	}
	finished := waitAny("alpha", "beta", "gamma")
	select {
	case r := <-finished:
		t.Fatalf("returned with every agent busy: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	daemons["beta"].turnEnd("from beta", "")
	daemons["beta"].done(0, false)
	r := <-finished
	if r.code != 0 || r.out != "==> beta <==\nfrom beta\n" || r.errOut != "still waiting on: alpha, gamma\n" {
		t.Fatalf("got %+v", r)
	}
	for _, agent := range []string{"alpha", "gamma"} {
		if s := p.status(agent).State; s != "busy" {
			t.Fatalf("%s: %s", agent, s)
		}
	}

	// Agents already finished count at once, and a failed last turn exits 1.
	daemons["alpha"].turnEnd("", "rate limited")
	daemons["alpha"].done(1, false)
	p.waitUntil("alpha is idle", func() bool { return p.status("alpha").State == "idle" })
	r = <-waitAny("alpha", "beta", "gamma")
	if r.code != 1 || r.out != "==> alpha <==\n==> beta <==\nfrom beta\n" ||
		r.errOut != "alpha: its last turn failed with status 1: rate limited\nstill waiting on: gamma\n" {
		t.Fatalf("got %+v", r)
	}

	// With none left busy, there is nothing still to wait on.
	r = <-waitAny("alpha", "beta")
	if r.code != 1 || r.out != "==> alpha <==\n==> beta <==\nfrom beta\n" ||
		r.errOut != "alpha: its last turn failed with status 1: rate limited\n" {
		t.Fatalf("got %+v", r)
	}

	r = <-waitAny("gamma", "--timeout", "0.1")
	if r.code != layout.TimeoutStatus || r.out != "" || r.errOut != "error: timed out waiting for any of gamma\n" {
		t.Fatalf("got %+v", r)
	}
}

func TestWaitForAnyHangsUpOnTheOthersWhenOneFinishes(t *testing.T) {
	idle, busy := true, false
	hungUp := make(chan string, 2)
	useCall(t, func(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
		if *req.Timeout == 0 {
			return &protocol.Response{Idle: &busy}, nil
		}
		if req.Agent == "beta" {
			return &protocol.Response{Idle: &idle, Turns: 1}, nil
		}
		<-ctx.Done()
		hungUp <- req.Agent
		return nil, ctx.Err()
	})
	results, err := WaitForAny([]string{"alpha", "beta", "gamma"}, f64(60))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Agent != "beta" || results[0].Turns != 1 {
		t.Fatalf("got %+v", results)
	}
	got := []string{<-hungUp, <-hungUp}
	slices.Sort(got)
	if !reflect.DeepEqual(got, []string{"alpha", "gamma"}) {
		t.Fatalf("got %v", got)
	}
}

func TestMainRejectsBadCommandLines(t *testing.T) {
	for _, args := range [][]string{{}, {"nope"}, {"send", "only-text"}, {"wait", "a", "--timeout"}, {"wait", "a", "--timeout", "x"}, {"serve"}, {"stop", "a", "--now"}} {
		var out, errOut strings.Builder
		if code := run(args, &out, &errOut); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

// A message that has been sent keeps its agent busy until its turn ends: there is no
// moment between the mailbox giving the message up and the status showing the turn
// when a wait would find the agent idle.
func TestAnAgentIsNeverIdleBetweenAMessageBeingSentAndItsTurnEnding(t *testing.T) {
	p := startPlane(t, t.TempDir())
	daemon := p.attach("alpha")
	p.waitUntil("attached", func() bool { return p.hasDaemon("alpha") })
	for i := range 100 {
		stop := make(chan struct{})
		seen := make(chan bool)
		p.send("alpha", "one")
		go func() {
			for {
				select {
				case <-stop:
					seen <- false
					return
				default:
					if p.control.idle("alpha") {
						seen <- true
						return
					}
				}
			}
		}()
		daemon.receive()
		close(stop)
		if <-seen {
			t.Fatalf("round %d: idle with a turn running", i)
		}
		daemon.done(0, false)
		p.wait("alpha")
	}
}

// wait reports idle when it returned because the agent was idle, even if a message
// reaches the agent right after: the next pass of `gnat wait` handles that one.
func TestWaitWithoutATimeoutAlwaysReportsTheAgentIdle(t *testing.T) {
	p := startPlane(t, t.TempDir())
	daemon := p.attach("alpha")
	p.waitUntil("attached", func() bool { return p.hasDaemon("alpha") })
	finished := make(chan struct{})
	defer func() { daemon.conn.Close(); <-finished }()
	go func() { // The daemon: finishes every turn at once.
		defer close(finished)
		for {
			line, err := daemon.r.ReadBytes('\n')
			if err != nil {
				return
			}
			if strings.Contains(string(line), `"turn"`) {
				exit := 0
				data, _ := protocol.Encode(protocol.Line{Type: protocol.LineDone, Exit: &exit})
				daemon.conn.Write(data)
			}
		}
	}()
	for i := range 100 {
		// Another agent's message arrives about when the wait looks.
		go func() {
			time.Sleep(time.Duration(i%7) * 50 * time.Microsecond)
			p.request(protocol.Request{Op: protocol.OpSend, Agent: "alpha", Text: "x"})
		}()
		// The database syncs to disk on every change, which can be slow.
		r, err := protocol.CallAddr(p.addr, protocol.Request{Op: protocol.OpWait, Agent: "alpha"}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if r.Idle == nil || !*r.Idle {
			t.Fatalf("round %d: wait without a timeout returned with the agent not idle", i)
		}
	}
}

// rawAgentConn is a connection to the agents' address, for requests to send by hand.
func (p *plane) rawAgentConn() net.Conn {
	p.t.Helper()
	conn, err := net.DialTimeout("tcp", p.agentAddr, 5*time.Second)
	if err != nil {
		p.t.Fatal(err)
	}
	p.t.Cleanup(func() { conn.Close() })
	return conn
}

func bufioReader(conn net.Conn) *bufio.Reader { return bufio.NewReader(conn) }
