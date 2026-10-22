// Package protocol is the wire format between gnat's processes.
//
// A one-shot request is one JSON object per line, one request per connection, answered
// by one line {"ok": ...}. The exception is an agent daemon's long-lived connection,
// opened with a Request whose Op is OpAttach, on which both ends then send Lines.
package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/kkestell/gnat/internal/events"
)

// Port is the control plane's TCP port.
const Port = 7000

// The ops of a Request.
const (
	OpAttach = "attach"
	OpSend   = "send"
	OpTake   = "take"
	OpStatus = "status"
	OpWait   = "wait"
	OpStop   = "stop"
	OpOutput = "output"
)

// The types of a Line.
const (
	LineTurn  = "turn"
	LineStop  = "stop"
	LineEvent = "event"
	LineDone  = "done"
	LineError = "error"
)

// A Request is what a client sends the control plane; each op uses some of the fields.
type Request struct {
	Op    string `json:"op"`
	Agent string `json:"agent,omitempty"`
	// Text is a message (send) or output (output).
	Text string `json:"text,omitempty"`
	// Sender is the sending agent; empty when the operator sends.
	Sender string `json:"sender,omitempty"`
	// Now delivers a message into the running turn; New starts a fresh session with it.
	Now bool `json:"now,omitempty"`
	New bool `json:"new,omitempty"`
	// Timeout (wait) is in seconds; nil waits as long as it takes.
	Timeout *float64 `json:"timeout,omitempty"`
}

// A TakenMessage is one message `take` hands the hook.
type TakenMessage struct {
	Text   string  `json:"text"`
	Sender *string `json:"sender"`
}

// A Response answers a Request. Only the fields of the request's op are set, and zero
// values of the status fields are left out. Error is the failure message when OK is
// false, and otherwise (in a status) the failure of the agent's last turn.
type Response struct {
	OK    bool    `json:"ok"`
	Error *string `json:"error,omitempty"`

	// stop
	Stopping *bool `json:"stopping,omitempty"`
	// take
	Messages []TakenMessage `json:"messages,omitempty"`
	// wait: whether the agent is idle, as well as its status.
	Idle *bool `json:"idle,omitempty"`

	// status and wait: the agent's status.
	State       string              `json:"state,omitempty"`
	Queued      int                 `json:"queued,omitempty"`
	Turns       int                 `json:"turns,omitempty"`
	Exit        *int                `json:"exit,omitempty"`
	Stopped     bool                `json:"stopped,omitempty"`
	Reply       *string             `json:"reply,omitempty"`
	Usage       *events.UsageTotals `json:"usage,omitempty"`
	SinceActive *float64            `json:"since_active,omitempty"`
}

// A Line is one message on an attached daemon's connection. The control plane pushes
// LineTurn (Text, New) to start a turn, LineStop to end the running one, and LineError
// (Error) for a line it can't use; the daemon sends LineEvent (Event) for each event of
// a turn and LineDone (Exit, Stopped) when it ends.
type Line struct {
	Type    string       `json:"type"`
	Text    string       `json:"text,omitempty"`
	New     bool         `json:"new,omitempty"`
	Event   events.Event `json:"event,omitempty"`
	Exit    *int         `json:"exit,omitempty"`
	Stopped bool         `json:"stopped,omitempty"`
	Error   string       `json:"error,omitempty"`
}

// A RequestError is a request that failed: the control plane refused it, or took it and
// didn't answer (NoResponse). Failing to connect is not one; it's the net error, so
// callers can retry an unreachable host without resending a request that was taken.
type RequestError struct {
	Message    string
	NoResponse bool
}

func (e *RequestError) Error() string { return e.Message }

// IsNoResponse reports whether err is the control plane taking a request and closing the
// connection without answering it.
func IsNoResponse(err error) bool {
	var re *RequestError
	return errors.As(err, &re) && re.NoResponse
}

// Encode is v as one JSON line, as Python's json.dumps wrote it: HTML is not escaped.
func Encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil // Encode ends the line.
}

// OK is a successful Response.
func OK() *Response { return &Response{OK: true} }

// Fail is a failed Response.
func Fail(message string) *Response { return &Response{Error: &message} }

// Call sends one request to the control plane on host and waits for its response, by
// default (timeout 0) however long it takes.
func Call(host string, req Request, timeout time.Duration) (*Response, error) {
	return CallAddr(net.JoinHostPort(host, strconv.Itoa(Port)), req, timeout)
}

// CallAddr is Call to a "host:port" address.
func CallAddr(addr string, req Request, timeout time.Duration) (*Response, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	host, _, _ := net.SplitHostPort(addr)

	line, err := Encode(req)
	if err != nil {
		return nil, err
	}
	var reply []byte
	if _, err = conn.Write(line); err == nil {
		reply, err = bufio.NewReader(conn).ReadBytes('\n')
		if err == io.EOF {
			err = nil // The last line may lack its newline.
		}
	}
	if err != nil {
		return nil, &RequestError{Message: fmt.Sprintf("%s didn't respond: %v", host, err), NoResponse: true}
	}
	if len(reply) == 0 {
		return nil, &RequestError{Message: fmt.Sprintf("%s closed the connection without responding", host), NoResponse: true}
	}
	var resp Response
	if err := json.Unmarshal(reply, &resp); err != nil {
		return nil, fmt.Errorf("bad response from %s: %w", host, err)
	}
	if !resp.OK {
		message := "request failed"
		if resp.Error != nil {
			message = *resp.Error
		}
		return nil, &RequestError{Message: message}
	}
	return &resp, nil
}

// StatusLine is an agent's status as `status` prints it: every field, as the response
// carries them all.
func StatusLine(agent string, r *Response) any {
	return struct {
		Agent       string   `json:"agent"`
		OK          bool     `json:"ok"`
		State       string   `json:"state"`
		Queued      int      `json:"queued"`
		Turns       int      `json:"turns"`
		Exit        *int     `json:"exit"`
		Stopped     bool     `json:"stopped"`
		Reply       *string  `json:"reply"`
		Error       *string  `json:"error"`
		Usage       any      `json:"usage"`
		SinceActive *float64 `json:"since_active"`
	}{agent, true, r.State, r.Queued, r.Turns, r.Exit, r.Stopped, r.Reply, r.Error, r.Usage, r.SinceActive}
}
