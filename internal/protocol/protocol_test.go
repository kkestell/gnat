package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// serve answers each connection with handler(request line) and closes it.
func serve(t *testing.T, handler func(line string) string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, _ := bufio.NewReader(conn).ReadString('\n')
				if reply := handler(line); reply != "" {
					conn.Write([]byte(reply))
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func TestEncodeIsOneLineWithoutHTMLEscapes(t *testing.T) {
	b, err := Encode(Request{Op: OpSend, Agent: "a", Text: "<b>&</b>"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"op":"send","agent":"a","text":"<b>&</b>"}`+"\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCallReturnsTheResponse(t *testing.T) {
	var got Request
	addr := serve(t, func(line string) string {
		json.Unmarshal([]byte(line), &got)
		return `{"ok": true, "stopping": true}` + "\n"
	})
	resp, err := CallAddr(addr, Request{Op: OpStop, Agent: "alpha"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got.Op != "stop" || got.Agent != "alpha" {
		t.Fatalf("server got %+v", got)
	}
	if resp.Stopping == nil || !*resp.Stopping {
		t.Fatalf("response %+v", resp)
	}
}

func TestCallFailureIsARequestError(t *testing.T) {
	addr := serve(t, func(string) string { return `{"ok": false, "error": "no agent 'x'"}` + "\n" })
	_, err := CallAddr(addr, Request{Op: OpStatus, Agent: "x"}, time.Second)
	var re *RequestError
	if !errors.As(err, &re) || re.Message != "no agent 'x'" || IsNoResponse(err) {
		t.Fatalf("got %v", err)
	}
}

func TestCallWithoutAResponse(t *testing.T) {
	addr := serve(t, func(string) string { return "" })
	_, err := CallAddr(addr, Request{Op: OpStatus, Agent: "x"}, time.Second)
	if !IsNoResponse(err) || !strings.Contains(err.Error(), "without responding") {
		t.Fatalf("got %v", err)
	}
}

func TestCallTimesOutWaitingForAnAnswer(t *testing.T) {
	addr := serve(t, func(string) string { time.Sleep(time.Second); return "" })
	_, err := CallAddr(addr, Request{Op: OpWait, Agent: "x"}, 50*time.Millisecond)
	if !IsNoResponse(err) {
		t.Fatalf("got %v", err)
	}
}

func TestCallUnreachableIsNotARequestError(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	_, err := CallAddr(addr, Request{Op: OpStatus}, time.Second)
	var re *RequestError
	if err == nil || errors.As(err, &re) {
		t.Fatalf("got %v", err)
	}
}

func TestResponseStatusFields(t *testing.T) {
	var resp Response
	line := `{"ok": true, "state": "idle", "queued": 0, "turns": 2, "exit": 0, "stopped": false,
		"reply": "Done.", "error": null, "usage": {"input_tokens": 5, "cache_read_tokens": null,
		"cache_write_tokens": null, "output_tokens": 7, "cost": 0.5}, "since_active": 1.5, "idle": true}`
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.State != "idle" || resp.Turns != 2 || *resp.Exit != 0 || *resp.Reply != "Done." ||
		*resp.Usage.OutputTokens != 7 || resp.Usage.CacheReadTokens != nil || !*resp.Idle {
		t.Fatalf("got %+v", resp)
	}
}

func TestLineRoundTrip(t *testing.T) {
	b, err := Encode(Line{Type: LineDone, Exit: new(int), Stopped: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"type":"done","exit":0,"stopped":true}`+"\n" {
		t.Fatalf("got %q", got)
	}
}
