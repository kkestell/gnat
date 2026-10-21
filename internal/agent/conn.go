package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kkestell/gnat/internal/events"
	"github.com/kkestell/gnat/internal/harness"
	"github.com/kkestell/gnat/internal/protocol"
	"github.com/kkestell/gnat/internal/state"
)

// retryDelay is how long to wait before trying an unreachable control plane again.
var retryDelay = time.Second

// controlAddr is where the control plane listens.
var controlAddr = net.JoinHostPort(state.Control, strconv.Itoa(protocol.Port))

// request makes a one-shot request to the control plane; a variable so tests can fake it.
var request = func(req protocol.Request) (*protocol.Response, error) {
	return protocol.CallAddr(controlAddr, req, 0)
}

// call makes a request to the control plane, retrying until it's reachable.
//
// A request it took and didn't answer isn't retried: resending it would fail the same
// way forever.
func call(req protocol.Request) (*protocol.Response, error) {
	for {
		resp, err := request(req)
		var netErr net.Error
		if err == nil || !errors.As(err, &netErr) {
			return resp, err
		}
		time.Sleep(retryDelay)
	}
}

// turn is one message the control plane pushed: its text, and whether it starts a new session.
type turn struct {
	text string
	new  bool
}

// Connection is the daemon's connection to the control plane (see package control).
//
// A goroutine reads it: turns go into the turns channel, which is closed once the
// connection is gone, and stops go straight to onStop, so a running turn ends without
// waiting on anything.
type Connection struct {
	conn   net.Conn
	onStop func()
	turns  chan turn
	// closed is closed, before onStop is called, once the connection is gone.
	closed chan struct{}
	// writeMu serializes writes: events come from the turn's goroutine and from
	// goroutines the harness starts.
	writeMu sync.Mutex
}

// Open attaches to the control plane at addr as the agent, retrying until it's reachable.
func Open(name string, onStop func(), addr string) *Connection {
	for {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			time.Sleep(retryDelay)
			continue
		}
		c := &Connection{
			conn: conn, onStop: onStop, turns: make(chan turn, 16), closed: make(chan struct{}),
		}
		c.send(protocol.Request{Op: protocol.OpAttach, Agent: name})
		go c.read()
		return c
	}
}

// send sends a message; one that can't be sent is dropped, since the reader sees the
// connection is gone.
func (c *Connection) send(message any) {
	data, err := protocol.Encode(message)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gnat-agent: can't encode a message: %v\n", err)
		return
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, _ = c.conn.Write(data)
}

func (c *Connection) read() {
	defer func() {
		// A turn whose events can't reach the control plane is ended; the control plane
		// reports it interrupted when the daemon attaches again.
		close(c.closed)
		c.onStop()
		close(c.turns)
		c.conn.Close()
	}()
	reader := bufio.NewReader(c.conn)
	for {
		data, err := reader.ReadBytes('\n')
		if len(data) > 0 {
			c.handle(data)
		}
		if err != nil {
			return
		}
	}
}

func (c *Connection) handle(data []byte) {
	var line protocol.Line
	if err := json.Unmarshal(data, &line); err != nil {
		fmt.Fprintf(os.Stderr, "gnat-agent: bad message from the control plane: %v\n", err)
		return
	}
	switch line.Type {
	case protocol.LineTurn:
		c.turns <- turn{line.Text, line.New}
	case protocol.LineStop:
		c.onStop()
	case protocol.LineError:
		fmt.Fprintf(os.Stderr, "gnat-agent: the control plane says: %s\n", line.Error)
	}
}

// Daemon runs the agent's turns, one at a time, on what the control plane pushes.
type Daemon struct {
	runner harness.Runner

	// connection is the one the daemon is serving, for the events the runner emits from
	// whatever goroutines it starts.
	connection atomic.Pointer[Connection]

	// mu guards stopping, which a stop sets from the connection's reader goroutine.
	mu sync.Mutex
	// stopping is closed to end the running turn; nil between turns.
	stopping chan struct{}
	stopSent bool
}

// NewDaemon makes a daemon whose runner takes the events it emits from newRunner's argument.
func NewDaemon(newRunner func(harness.Emit) harness.Runner) *Daemon {
	d := &Daemon{}
	d.runner = newRunner(d.emit)
	return d
}

func (d *Daemon) emit(event events.Event) {
	if c := d.connection.Load(); c != nil {
		c.send(protocol.Line{Type: protocol.LineEvent, Event: event})
	}
}

// Stop ends the running turn, if there is one.
func (d *Daemon) Stop() {
	// Held while stopping, so the turn can't end and the next start in between.
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopping == nil || d.stopSent {
		return
	}
	d.stopSent = true
	close(d.stopping)
	d.runner.Stop()
}

// Serve runs each turn pushed on the connection until it's gone.
func (d *Daemon) Serve(c *Connection) {
	d.connection.Store(c)
	for t := range c.turns {
		code, stopped := d.turn(t)
		c.send(protocol.Line{Type: protocol.LineDone, Exit: &code, Stopped: stopped})
	}
}

// turn runs one turn; its exit status, and whether it was stopped on request.
func (d *Daemon) turn(t turn) (code int, stopped bool) {
	stopping := make(chan struct{})
	d.mu.Lock()
	d.stopping, d.stopSent = stopping, false
	if c := d.connection.Load(); c != nil {
		select {
		case <-c.closed:
			d.stopSent = true
			close(stopping)
		default:
		}
	}
	d.mu.Unlock()

	code = d.run(t, stopping)

	d.mu.Lock()
	d.stopping = nil
	stopped = d.stopSent
	d.mu.Unlock()
	return code, stopped
}

// run runs the runner, failing this message rather than the agent if it breaks.
func (d *Daemon) run(t turn, stopping <-chan struct{}) (code int) {
	defer func() {
		if r := recover(); r != nil {
			d.emit(events.NewError(fmt.Sprintf("error: panic: %v", r)))
			code = 1
		}
	}()
	code, err := d.runner.Run(t.text, t.new, stopping)
	if err != nil {
		d.emit(events.NewError(fmt.Sprintf("error: %v", err)))
		return 1
	}
	return code
}
