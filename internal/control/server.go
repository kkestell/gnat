package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/kkestell/gnat/internal/events"
	"github.com/kkestell/gnat/internal/protocol"
)

// writeTimeout bounds a write to a connection, so a peer that stopped reading can't
// hold the control plane up.
const writeTimeout = 30 * time.Second

// A Daemon is an agent daemon's attached connection.
type Daemon struct {
	conn net.Conn
	wmu  sync.Mutex
	// turnOver is signalled when the daemon reports its turn done; detached is closed
	// when its connection is finished with.
	turnOver chan struct{}
	detached chan struct{}
}

func newDaemon(conn net.Conn) *Daemon {
	return &Daemon{conn: conn, turnOver: make(chan struct{}, 1), detached: make(chan struct{})}
}

// push sends the daemon a line.
func (d *Daemon) push(line protocol.Line) error {
	data, err := protocol.Encode(line)
	if err != nil || d.conn == nil {
		return err
	}
	d.wmu.Lock()
	defer d.wmu.Unlock()
	_ = d.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err = d.conn.Write(data)
	return err
}

// Serve accepts the operator's connections on ln until ctx is cancelled, then closes them
// all. ln must be one only the operator can reach, such as the control container's own
// socket, since what comes in on it is trusted: the commands `gnat` runs there, which
// send the agents messages, stop them, and ask how they are.
func (c *Control) Serve(ctx context.Context, ln net.Listener) error {
	return c.serve(ctx, ln, false)
}

// ServeAgents is Serve for ln the agents can reach, on the swarm's network. What comes in
// on it is believed only as far as the secret in each request goes: it says which agent
// is asking, and the agent is allowed to attach, to send messages as itself, to take its
// own, to report its own output, and to ask for what GitHub operations it may have.
func (c *Control) ServeAgents(ctx context.Context, ln net.Listener) error {
	return c.serve(ctx, ln, true)
}

func (c *Control) serve(ctx context.Context, ln net.Listener, fromAgents bool) error {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		conns = map[net.Conn]bool{}
	)
	stop := context.AfterFunc(ctx, func() {
		ln.Close()
		mu.Lock()
		for conn := range conns {
			conn.Close()
		}
		mu.Unlock()
	})
	defer stop()
	defer wg.Wait()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		mu.Lock()
		conns[conn] = true
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.handle(conn, fromAgents)
			mu.Lock()
			delete(conns, conn)
			mu.Unlock()
		}()
	}
}

// handle serves one connection: a request and its response, or an agent daemon's
// attachment.
func (c *Control) handle(conn net.Conn, fromAgents bool) {
	defer conn.Close()
	br := bufio.NewReaderSize(conn, 64*1024)
	var req protocol.Request
	line, err := readLine(br, c.MaxRequestBytes)
	var tooLong tooLongError
	switch {
	case errors.As(err, &tooLong):
	case err != nil && !errors.Is(err, io.EOF):
		return
	default:
		// Not a line the control plane can use is answered like any bad request, since
		// a dropped connection looks to the client like an outage.
		if err = json.Unmarshal(line, &req); err == nil && req.Op == "" {
			err = errors.New("missing op")
		}
	}
	if err != nil {
		respond(conn, protocol.Fail(fmt.Sprintf("bad request: %v", err)))
		return
	}
	if fromAgents {
		c.handleAgent(req, br, conn)
		return
	}
	// The operator's: what the host runs, which names its agents and has no secret.
	switch req.Op {
	case protocol.OpSend:
		if req.Sender != "" {
			// Senders are known by their secrets, so here there is only the operator.
			respond(conn, protocol.Fail("a message's sender is the agent whose secret it comes with"))
			return
		}
	case protocol.OpStop, protocol.OpStatus, protocol.OpWait:
	default:
		respond(conn, protocol.Fail(fmt.Sprintf("'%s' is only for agents", req.Op)))
		return
	}
	if resp := c.dispatch(req, br); resp != nil {
		respond(conn, resp)
	}
}

// errUnauthorized is what a request from an agent gets without one of their secrets.
const errUnauthorized = "unauthorized: the agent secret is missing or wrong"

// handleAgent serves a request that came in over the network. Who sent it is whoever's
// secret it has, and that overrides anything it says about itself: the agent attaching,
// taking messages, or reporting output is that agent, and a message is from it.
func (c *Control) handleAgent(req protocol.Request, br *bufio.Reader, conn net.Conn) {
	agent := c.whoIs(req.Secret)
	req.Secret = ""
	if agent == "" {
		if req.Op == protocol.OpAttach {
			// An attachment is answered in lines, as the daemon reads.
			_ = newDaemon(conn).push(protocol.Line{Type: protocol.LineError, Error: errUnauthorized})
			return
		}
		respond(conn, protocol.Fail(errUnauthorized))
		return
	}
	switch req.Op {
	case protocol.OpAttach:
		c.attach(agent, br, conn)
		return
	case protocol.OpGitHub:
		respond(conn, c.githubRequest(agent, req))
		return
	case protocol.OpSend:
		// Whoever the message is to is the agent's to say, but not who it is from.
		req.Sender = agent
	case protocol.OpTake, protocol.OpOutput:
		req.Agent = agent
	default:
		respond(conn, protocol.Fail(fmt.Sprintf("'%s' is only for the operator", req.Op)))
		return
	}
	if resp := c.dispatch(req, br); resp != nil {
		respond(conn, resp)
	}
}

func respond(conn net.Conn, resp *protocol.Response) {
	if data, err := protocol.Encode(resp); err == nil {
		_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		_, _ = conn.Write(data)
	}
}

// dispatch answers a request; nil when the caller hung up before the answer.
func (c *Control) dispatch(req protocol.Request, r io.Reader) *protocol.Response {
	if req.Agent == "" {
		return protocol.Fail("bad request: missing agent")
	}
	mailbox := c.mailboxes[req.Agent]
	if mailbox == nil {
		return protocol.Fail(fmt.Sprintf("no agent '%s'", req.Agent))
	}
	switch req.Op {
	case protocol.OpSend:
		if req.Sender != "" && c.mailboxes[req.Sender] == nil {
			return protocol.Fail(fmt.Sprintf("no agent '%s'", req.Sender))
		}
		if req.Sender == req.Agent {
			return protocol.Fail(fmt.Sprintf("'%s' can't send a message to itself", req.Agent))
		}
		if req.Now && req.New {
			return protocol.Fail("a message can't both go into the running turn and start a new session")
		}
		if err := mailbox.Put(Message{req.Text, req.Sender, req.New}, req.Now, false); err != nil {
			return protocol.Fail(err.Error())
		}
		return protocol.OK()
	case protocol.OpStop:
		// Only a running turn can be stopped. The daemon reads the stop before any later
		// turn, so it can't linger to kill the next.
		c.mu.Lock()
		daemon := c.daemons[req.Agent]
		stopping := c.statuses[req.Agent].Busy && daemon != nil
		c.mu.Unlock()
		if stopping {
			_ = daemon.push(protocol.Line{Type: protocol.LineStop})
		}
		r := protocol.OK()
		r.Stopping = &stopping
		return r
	case protocol.OpTake:
		taken, err := mailbox.TakeUrgent()
		if err != nil {
			return protocol.Fail(err.Error())
		}
		r := protocol.OK()
		for _, m := range taken {
			c.log(req.Agent, m.LogLine(true))
			c.record(req.Agent, m.AsEvent(true))
			t := protocol.TakenMessage{Text: m.Text}
			if m.Sender != "" {
				t.Sender = &m.Sender
			}
			r.Messages = append(r.Messages, t)
		}
		return r
	case protocol.OpOutput:
		c.log(req.Agent, req.Text)
		c.record(req.Agent, events.NewError(trimNewlines(req.Text)))
		return protocol.OK()
	case protocol.OpStatus:
		return c.describe(req.Agent)
	case protocol.OpWait:
		return c.wait(req.Agent, req.Timeout, r)
	}
	return protocol.Fail(fmt.Sprintf("unknown op '%s'", req.Op))
}

func trimNewlines(s string) string {
	for len(s) > 0 && s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	return s
}

// wait is the agent's status once it has finished every message sent to it, or when the
// timeout (seconds; nil for none) runs out first; nil if the caller hangs up.
func (c *Control) wait(agent string, timeout *float64, caller io.Reader) *protocol.Response {
	var expired <-chan time.Time
	if timeout != nil {
		timer := time.NewTimer(time.Duration(max(*timeout, 0) * float64(time.Second)))
		defer timer.Stop()
		expired = timer.C
	}
	var gone chan struct{}
	// Whether the agent was idle when the wait ended. It is what the wait saw, not
	// looked up again afterwards: a message can reach the agent in between, and the
	// caller (see WaitFor) passes again for those.
	var idle bool
	for {
		// Take the channel before looking, so an end of turn in between isn't missed.
		ended := c.turnEnded.wait()
		if idle = c.idle(agent); idle {
			break
		}
		if gone == nil {
			gone = make(chan struct{})
			go func() {
				_, _ = io.Copy(io.Discard, caller)
				close(gone)
			}()
		}
		select {
		case <-ended:
			continue
		case <-expired:
			idle = c.idle(agent)
		case <-gone:
			return nil
		}
		break
	}
	r := c.describe(agent)
	r.Idle = &idle
	return r
}

// attach serves an agent daemon's connection until it hangs up: pushes it a turn
// whenever it's idle and has a message, and takes its events and turn ends.
func (c *Control) attach(agent string, br *bufio.Reader, conn net.Conn) {
	if c.mailboxes[agent] == nil {
		_ = newDaemon(conn).push(protocol.Line{Type: protocol.LineError, Error: fmt.Sprintf("no agent '%s'", agent)})
		return
	}
	c.mu.Lock()
	previous := c.daemons[agent]
	c.mu.Unlock()
	if previous != nil {
		// The daemon is back, so its old connection is dead; let that finish first.
		previous.conn.Close()
		<-previous.detached
	}
	c.mu.Lock()
	busy := c.statuses[agent].Busy
	c.mu.Unlock()
	if busy {
		// A daemon attaches only between turns, so it restarted, or the swarm went down
		// and came back up, mid-turn.
		c.log(agent, "[agent restarted; its turn was interrupted]\n\n")
		c.endTurn(agent, nil, false)
	}
	daemon := newDaemon(conn)
	c.mu.Lock()
	c.statuses[agent].Started = true
	c.daemons[agent] = daemon
	c.mu.Unlock()

	reading := make(chan struct{})
	go func() {
		defer close(reading)
		c.read(agent, daemon, br)
	}()
	defer func() {
		conn.Close()
		<-reading
		c.mu.Lock()
		if c.daemons[agent] == daemon {
			delete(c.daemons, agent)
		}
		c.mu.Unlock()
		close(daemon.detached)
	}()
	c.pushTurns(agent, daemon, reading)
}

// pushTurns pushes the agent each message as a turn, one at a time, until reading ends.
func (c *Control) pushTurns(agent string, daemon *Daemon, reading <-chan struct{}) {
	mailbox := c.mailboxes[agent]
	type got struct {
		msg    Message
		urgent bool
		err    error
	}
	for {
		// The turn starts the moment the mailbox gives the message up, so that no
		// wait finds the agent idle with its message nowhere.
		var before Status
		start := func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			s := c.statuses[agent]
			before = Status{Busy: s.Busy, Reply: s.Reply, Error: s.Error}
			s.Busy, s.Reply, s.Error = true, nil, nil
			logErr(s.save())
			c.active[agent] = time.Now()
		}
		// putBack keeps a message the daemon hung up on for the next, and undoes start.
		putBack := func(g got) {
			logErr(mailbox.Put(g.msg, g.urgent, true))
			c.mu.Lock()
			defer c.mu.Unlock()
			s := c.statuses[agent]
			s.Busy, s.Reply, s.Error = before.Busy, before.Reply, before.Error
			logErr(s.save())
		}
		cancel := make(chan struct{})
		result := make(chan got, 1)
		go func() {
			msg, urgent, err := mailbox.Get(cancel, start)
			result <- got{msg, urgent, err}
		}()
		var g got
		select {
		case g = <-result:
		case <-reading:
			close(cancel)
			g = <-result
			if g.err == nil {
				// The daemon hung up just as its message came.
				putBack(g)
			}
			return
		}
		if g.err != nil {
			logErr(g.err)
			return
		}
		select {
		case <-reading:
			putBack(g)
			return
		default:
		}

		select { // Forget a turn end left over from the last turn.
		case <-daemon.turnOver:
		default:
		}
		c.log(agent, g.msg.LogLine(false))
		c.record(agent, g.msg.AsEvent(false))
		if err := daemon.push(protocol.Line{Type: protocol.LineTurn, Text: g.msg.ForAgent(), New: g.msg.New}); err != nil {
			// Gone: the turn is reported interrupted when the daemon is back.
			return
		}
		select {
		case <-daemon.turnOver:
		case <-reading:
			// Gone mid-turn: the turn is reported interrupted when the daemon is back.
			return
		}
	}
}

// read takes the daemon's events and turn ends until it hangs up.
func (c *Control) read(agent string, daemon *Daemon, br *bufio.Reader) {
	for {
		line, err := readLine(br, c.MaxRequestBytes)
		var tooLong tooLongError
		if err != nil && !errors.As(err, &tooLong) && (!errors.Is(err, io.EOF) || len(line) == 0) {
			return
		}
		if err == nil || errors.Is(err, io.EOF) {
			err = c.handleLine(agent, daemon, line)
		}
		if err != nil {
			// Answer it, and note it in the log, rather than drop it unseen.
			c.log(agent, fmt.Sprintf("[bad message from the agent daemon: %v]\n", err))
			_ = daemon.push(protocol.Line{Type: protocol.LineError, Error: fmt.Sprintf("bad message: %v", err)})
		}
	}
}

// handleLine acts on one line from the daemon; the error says why it can't.
func (c *Control) handleLine(agent string, daemon *Daemon, line []byte) error {
	var l protocol.Line
	if err := json.Unmarshal(line, &l); err != nil {
		return err
	}
	switch l.Type {
	case protocol.LineEvent:
		if l.Event.Type() == "" {
			return errors.New("an event must be an object with a type")
		}
		c.event(agent, l.Event)
	case protocol.LineDone:
		c.mu.Lock()
		busy := c.statuses[agent].Busy
		c.mu.Unlock()
		if !busy {
			return errors.New("no turn is running")
		}
		c.done(agent, l.Exit, l.Stopped)
		select {
		case daemon.turnOver <- struct{}{}:
		default:
		}
	default:
		return fmt.Errorf("unknown message type '%s'", l.Type)
	}
	return nil
}

func (c *Control) done(agent string, code *int, stopped bool) {
	switch {
	case stopped:
		c.log(agent, "[turn stopped]\n\n")
	case code != nil && *code != 0:
		c.log(agent, fmt.Sprintf("[turn failed with status %d]\n\n", *code))
	default:
		c.log(agent, "\n")
	}
	c.endTurn(agent, code, stopped)
}

func (c *Control) endTurn(agent string, code *int, stopped bool) {
	c.mu.Lock()
	s := c.statuses[agent]
	s.Busy, s.Exit, s.Stopped = false, code, stopped
	s.Turns++
	logErr(s.save())
	c.mu.Unlock()
	var exit any
	if code != nil {
		exit = *code
	}
	c.record(agent, events.Event{"type": "swarm_done", "exit": exit, "stopped": stopped})
	c.turnEnded.notify()
}
