package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/events"
	"github.com/kkestell/gnat/internal/harness"
	"github.com/kkestell/gnat/internal/protocol"
	"github.com/kkestell/gnat/internal/state"
)

// realRequest is the request the daemon makes, before a test replaces it.
var realRequest = request

// fakeRunner replies with the message in capitals, or for "block", runs until stopped.
type fakeRunner struct {
	emit harness.Emit
	// stopped is closed by Stop.
	stopped chan struct{}
	once    sync.Once

	mu    sync.Mutex
	turns []turn
}

func newFakeRunner(emit harness.Emit) harness.Runner {
	return &fakeRunner{emit: emit, stopped: make(chan struct{})}
}

func (r *fakeRunner) Run(text string, new bool, stop <-chan struct{}) (int, error) {
	r.mu.Lock()
	r.turns = append(r.turns, turn{text, new})
	r.mu.Unlock()
	r.emit(events.NewMessage("on " + text))
	switch text {
	case "block":
		select {
		case <-r.stopped:
		case <-time.After(10 * time.Second):
		}
		return -9, nil
	case "crash":
		return 0, errors.New("boom")
	case "panic":
		panic("boom")
	}
	r.emit(events.NewTurnEnd(strings.ToUpper(text), ""))
	return 0, nil
}

func (r *fakeRunner) Stop() { r.once.Do(func() { close(r.stopped) }) }

func (r *fakeRunner) turnsSoFar() []turn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]turn(nil), r.turns...)
}

// plane stands in for the control plane: it accepts one daemon's connection, and
// records the lines it sends.
type plane struct {
	t        *testing.T
	listener net.Listener
	connMu   sync.Mutex
	conn     net.Conn
	ready    chan struct{}
	lines    chan protocol.Line
	attached chan string
	// secret is the secret the daemon attached with.
	secret atomic.Value
}

func newPlane(t *testing.T) *plane {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &plane{
		t: t, listener: listener, ready: make(chan struct{}),
		lines: make(chan protocol.Line, 100), attached: make(chan string, 1),
	}
	t.Cleanup(func() { listener.Close() })
	go p.accept()
	return p
}

func (p *plane) addr() string { return p.listener.Addr().String() }

func (p *plane) accept() {
	conn, err := p.listener.Accept()
	if err != nil {
		return
	}
	p.connMu.Lock()
	p.conn = conn
	p.connMu.Unlock()
	close(p.ready)
	reader := bufio.NewReader(conn)
	first, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}
	var attach protocol.Request
	if err := json.Unmarshal(first, &attach); err != nil || attach.Op != protocol.OpAttach {
		p.t.Errorf("first line = %s", first)
	}
	p.secret.Store(attach.Secret)
	p.attached <- attach.Agent
	for {
		data, err := reader.ReadBytes('\n')
		if err != nil {
			close(p.lines)
			return
		}
		var line protocol.Line
		if err := json.Unmarshal(data, &line); err != nil {
			p.t.Errorf("bad line %s", data)
			continue
		}
		p.lines <- line
	}
}

// connection is the daemon's connection.
func (p *plane) connection() net.Conn {
	p.t.Helper()
	select {
	case <-p.ready:
	case <-time.After(5 * time.Second):
		p.t.Fatal("no daemon attached")
	}
	p.connMu.Lock()
	defer p.connMu.Unlock()
	return p.conn
}

func (p *plane) push(line protocol.Line) {
	p.t.Helper()
	data, err := protocol.Encode(line)
	if err != nil {
		p.t.Fatal(err)
	}
	if _, err := p.connection().Write(data); err != nil {
		p.t.Fatal(err)
	}
}

// next is the next line the daemon sends.
func (p *plane) next() protocol.Line {
	p.t.Helper()
	select {
	case line, ok := <-p.lines:
		if !ok {
			p.t.Fatal("the daemon hung up")
		}
		return line
	case <-time.After(5 * time.Second):
		p.t.Fatal("timed out waiting for the daemon")
	}
	return protocol.Line{}
}

// untilDone collects the lines up to and including the next done.
func (p *plane) untilDone() []protocol.Line {
	p.t.Helper()
	var lines []protocol.Line
	for {
		line := p.next()
		lines = append(lines, line)
		if line.Type == protocol.LineDone {
			return lines
		}
	}
}

// startDaemon attaches a daemon to the plane and serves it in the background.
func startDaemon(t *testing.T, p *plane) (*Daemon, *Connection, chan struct{}) {
	t.Helper()
	d := NewDaemon(newFakeRunner)
	c := Open("alpha", d.Stop, p.addr())
	served := make(chan struct{})
	go func() { d.Serve(c); close(served) }()
	t.Cleanup(func() { c.conn.Close() })
	if agent := <-p.attached; agent != "alpha" {
		t.Fatalf("attached as %q", agent)
	}
	return d, c, served
}

func eventTypes(lines []protocol.Line) []string {
	var types []string
	for _, l := range lines {
		if l.Type == protocol.LineEvent {
			types = append(types, l.Event.Type())
		} else {
			types = append(types, l.Type)
		}
	}
	return types
}

func TestAPushedMessageRunsATurnWhoseEventsAndEndComeBack(t *testing.T) {
	p := newPlane(t)
	d, _, _ := startDaemon(t, p)
	p.push(protocol.Line{Type: protocol.LineTurn, Text: "hello", New: true})
	lines := p.untilDone()
	if got := strings.Join(eventTypes(lines), " "); got != "message turn_end done" {
		t.Fatalf("lines = %s", got)
	}
	if lines[0].Event["text"] != "on hello" || lines[1].Event["reply"] != "HELLO" {
		t.Errorf("events = %v, %v", lines[0].Event, lines[1].Event)
	}
	done := lines[2]
	if done.Exit == nil || *done.Exit != 0 || done.Stopped {
		t.Errorf("done = %+v", done)
	}
	if got := d.runner.(*fakeRunner).turnsSoFar(); len(got) != 1 || got[0] != (turn{"hello", true}) {
		t.Errorf("turns = %v", got)
	}
}

func TestAStopIsPushedIntoTheRunningTurn(t *testing.T) {
	p := newPlane(t)
	startDaemon(t, p)
	p.push(protocol.Line{Type: protocol.LineTurn, Text: "block"})
	if first := p.next(); first.Type != protocol.LineEvent {
		t.Fatalf("first line = %+v", first)
	}
	p.push(protocol.Line{Type: protocol.LineStop})
	done := p.untilDone()[0]
	if done.Exit == nil || *done.Exit != -9 || !done.Stopped {
		t.Errorf("done = %+v", done)
	}
	// The agent goes on to its next message.
	p.push(protocol.Line{Type: protocol.LineTurn, Text: "next"})
	lines := p.untilDone()
	if lines[1].Event["reply"] != "NEXT" {
		t.Errorf("lines = %+v", lines)
	}
}

func TestATurnThatFailsDoesntEndTheDaemon(t *testing.T) {
	for _, text := range []string{"crash", "panic"} {
		t.Run(text, func(t *testing.T) {
			p := newPlane(t)
			startDaemon(t, p)
			p.push(protocol.Line{Type: protocol.LineTurn, Text: text})
			lines := p.untilDone()
			if got := strings.Join(eventTypes(lines), " "); got != "message error done" {
				t.Fatalf("lines = %s", got)
			}
			if message, _ := lines[1].Event["text"].(string); !strings.Contains(message, "boom") ||
				!strings.HasPrefix(message, "error: ") {
				t.Errorf("error = %q", message)
			}
			if exit := lines[2].Exit; exit == nil || *exit != 1 {
				t.Errorf("done = %+v", lines[2])
			}
			p.push(protocol.Line{Type: protocol.LineTurn, Text: "next"})
			if lines := p.untilDone(); lines[1].Event["reply"] != "NEXT" {
				t.Errorf("lines = %+v", lines)
			}
		})
	}
}

func TestLosingTheConnectionEndsTheRunningTurn(t *testing.T) {
	p := newPlane(t)
	d, _, served := startDaemon(t, p)
	p.push(protocol.Line{Type: protocol.LineTurn, Text: "block"})
	p.next()
	p.connection().Close()
	select {
	case <-d.runner.(*fakeRunner).stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn wasn't stopped")
	}
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon kept serving a dead connection")
	}
}

func TestCallRetriesUntilTheControlPlaneIsReachable(t *testing.T) {
	retryDelay = 0
	attempts := 0
	request = func(protocol.Request) (*protocol.Response, error) {
		attempts++
		switch attempts {
		case 1:
			return nil, &net.OpError{Op: "dial", Err: errors.New("refused")}
		case 2:
			return nil, &net.OpError{Op: "dial", Err: errors.New("no route")}
		}
		return protocol.OK(), nil
	}
	t.Cleanup(func() { retryDelay = time.Second; request = realRequest })
	resp, err := call(protocol.Request{Op: protocol.OpOutput, Agent: "alpha", Text: "x"})
	if err != nil || !resp.OK || attempts != 3 {
		t.Fatalf("call = %v, %v after %d attempts", resp, err, attempts)
	}
}

func TestCallDoesntResendARequestTheControlPlaneTookAndDropped(t *testing.T) {
	attempts := 0
	request = func(protocol.Request) (*protocol.Response, error) {
		attempts++
		return nil, &protocol.RequestError{Message: "control closed the connection without responding", NoResponse: true}
	}
	t.Cleanup(func() { request = realRequest })
	_, err := call(protocol.Request{Op: protocol.OpOutput, Agent: "alpha", Text: "x"})
	if !protocol.IsNoResponse(err) || attempts != 1 {
		t.Fatalf("call = %v after %d attempts", err, attempts)
	}
}

func TestConnectingRetriesUntilTheControlPlaneIsUp(t *testing.T) {
	// Nothing listens yet: take a free port, release it, and listen on it later.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	retryDelay = 10 * time.Millisecond
	t.Cleanup(func() { retryDelay = time.Second })

	attached := make(chan *Connection)
	go func() { attached <- Open("alpha", func() {}, addr) }()
	time.Sleep(50 * time.Millisecond)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	first, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil || string(first) != `{"op":"attach","agent":"alpha"}`+"\n" {
		t.Fatalf("first line = %q, %v", first, err)
	}
	(<-attached).conn.Close()
}

func TestTheDaemonAttachesAndRequestsWithTheAgentsSecret(t *testing.T) {
	t.Setenv(state.SecretVar, "the-secret")
	p := newPlane(t)
	c := Open("alpha", func() {}, p.addr())
	defer c.conn.Close()
	<-p.attached
	if got := p.secret.Load(); got != "the-secret" {
		t.Errorf("attached with %q", got)
	}

	// One-shot requests too, whoever builds them.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan protocol.Request, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		line, _ := bufio.NewReader(conn).ReadBytes('\n')
		var req protocol.Request
		_ = json.Unmarshal(line, &req)
		got <- req
		data, _ := protocol.Encode(protocol.OK())
		conn.Write(data)
	}()
	saved := controlAddr
	controlAddr = ln.Addr().String()
	defer func() { controlAddr = saved }()
	if _, err := request(protocol.Request{Op: protocol.OpOutput, Agent: "alpha", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if req := <-got; req.Secret != "the-secret" {
		t.Errorf("sent %+v", req)
	}
}

// A control plane that refuses every attachment, as it does for a secret that is missing
// or wrong, mustn't be answered by attaching as fast as it can be done.
func TestTheDaemonPausesBetweenAttachmentsThatAreRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var attached atomic.Int64
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			attached.Add(1)
			_, _ = bufio.NewReader(conn).ReadBytes('\n')
			data, _ := protocol.Encode(protocol.Line{Type: protocol.LineError, Error: "unauthorized: the agent secret is missing or wrong"})
			conn.Write(data)
			conn.Close()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	savedDelay := retryDelay
	retryDelay = 100 * time.Millisecond
	t.Cleanup(func() { retryDelay = savedDelay })

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveLoop(NewDaemon(newFakeRunner), "alpha", ln.Addr().String(), stop)
	}()
	time.Sleep(550 * time.Millisecond)
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop didn't end")
	}
	if n := attached.Load(); n < 3 || n > 8 {
		t.Errorf("attached %d times in about half a second, with a 100ms pause", n)
	}
}
