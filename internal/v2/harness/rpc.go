package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params map[string]any  `json:"params"`
	Result map[string]any  `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
type rpcRead struct {
	message rpcMessage
	err     error
}

// rpcProcess owns one process group and its pipes. Only the turn goroutine writes.
type rpcProcess struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *os.File
	lines  chan rpcRead
	closed chan struct{}
	done   chan struct{}
	next   int
}

func startRPC(ctx context.Context, program, dir string, args ...string) (*rpcProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command(program, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, writer, err := os.Pipe()
	if err != nil {
		in.Close()
		return nil, err
	}
	cmd.Stdout = writer
	if err = cmd.Start(); err != nil {
		in.Close()
		out.Close()
		writer.Close()
		return nil, fmt.Errorf("start %s: %w", program, err)
	}
	writer.Close()
	p := &rpcProcess{cmd: cmd, in: in, out: out, lines: make(chan rpcRead, 32), closed: make(chan struct{}), done: make(chan struct{})}
	go func() { _ = cmd.Wait(); _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); close(p.done) }()
	go func() {
		defer close(p.lines)
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 64<<10), 16<<20)
		for scanner.Scan() {
			var m rpcMessage
			err := json.Unmarshal(scanner.Bytes(), &m)
			if err != nil {
				err = fmt.Errorf("invalid %s JSON-RPC output: %w", program, err)
			}
			select {
			case p.lines <- rpcRead{m, err}:
			case <-p.closed:
				return
			}
			if err != nil {
				return
			}
		}
		err := scanner.Err()
		if err == nil {
			err = fmt.Errorf("%s exited before its protocol operation completed", program)
		}
		select {
		case p.lines <- rpcRead{err: err}:
		case <-p.closed:
		}
	}()
	return p, nil
}
func (p *rpcProcess) close() {
	close(p.closed)
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	p.in.Close()
	p.out.Close()
	<-p.done
}
func (p *rpcProcess) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if f, ok := p.in.(*os.File); ok {
		_ = f.SetWriteDeadline(time.Now().Add(2 * time.Second))
	}
	_, err = p.in.Write(append(b, '\n'))
	return err
}
func (p *rpcProcess) notify(method string, params map[string]any) error {
	return p.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}
func (p *rpcProcess) receive(ctx context.Context) (rpcMessage, error) {
	select {
	case <-ctx.Done():
		return rpcMessage{}, ctx.Err()
	case r, ok := <-p.lines:
		if !ok {
			return rpcMessage{}, fmt.Errorf("harness protocol stream closed")
		}
		return r.message, r.err
	}
}
func (p *rpcProcess) request(ctx context.Context, method string, params map[string]any, update func(rpcMessage) error) (map[string]any, error) {
	p.next++
	id := p.next
	if err := p.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		m, err := p.receive(ctx)
		if err != nil {
			return nil, err
		}
		if m.Method == "" && string(m.ID) == fmt.Sprint(id) {
			if m.Error != nil {
				return nil, fmt.Errorf("%s: %s (code %d)", method, m.Error.Message, m.Error.Code)
			}
			return m.Result, nil
		}
		if update != nil {
			if err = update(m); err != nil {
				return nil, err
			}
		} else if err = p.reject(m); err != nil {
			return nil, err
		}
	}
}
func (p *rpcProcess) reject(m rpcMessage) error {
	if m.Method == "" || len(m.ID) == 0 {
		return nil
	}
	reason := fmt.Sprintf("harness requested interactive input (%s) in an unattended agent", m.Method)
	_ = p.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": reason}})
	return fmt.Errorf("%s", reason)
}
func (p *rpcProcess) setup(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return p.request(ctx, method, params, nil)
}

// cancel gives the provider a bounded opportunity to settle the identified turn.
// close always reaps the process group, including unresponsive tool children.
func (p *rpcProcess) cancel(method string, params map[string]any, request bool, finished func(rpcMessage) bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	message := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if request {
		p.next++
		message["id"] = p.next
	}
	if err := p.send(message); err != nil {
		return
	}
	for {
		m, err := p.receive(ctx)
		if err != nil || finished(m) {
			return
		}
	}
}
