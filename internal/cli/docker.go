package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
)

// stdin, stdout, and stderr are the process's, unless a test says otherwise.
var (
	stdin  io.Reader = os.Stdin
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

// An exitError is a command that failed with a status gnat exits with too. A command
// that wrote its own error to the terminal has no message.
type exitError struct {
	Code    int
	Message string
}

func (e *exitError) Error() string { return e.Message }

// interruptedStatus is what a shell reports for a process ended by Ctrl-C.
const interruptedStatus = 130

// interrupted is set when Ctrl-C arrives during `gnat up`, which then tears down what
// it started instead of dying with it.
var interrupted atomic.Bool

// A command is an external program to run.
type command struct {
	name string
	args []string
	// stdin is what the program reads, if anything.
	stdin []byte
	// env is added to the environment, as NAME=value.
	env []string
	// capture returns the program's output instead of showing it, and quiet discards
	// its standard output; otherwise both go to the terminal.
	capture, quiet bool
	// inOrder shows the program's standard output and then its standard error once it
	// exits, since docker exec doesn't keep the order between them.
	inOrder bool
}

type result struct{ stdout, stderr string }

// execute runs a command. Tests replace it.
var execute = func(c command) (result, error) {
	if interrupted.Load() {
		return result{}, &exitError{Code: interruptedStatus}
	}
	cmd := exec.Command(c.name, c.args...)
	cmd.Env = append(os.Environ(), c.env...)
	if c.stdin != nil {
		cmd.Stdin = bytes.NewReader(c.stdin)
	}
	var out, errs bytes.Buffer
	switch {
	case c.capture, c.inOrder:
		cmd.Stdout, cmd.Stderr = &out, &errs
	case c.quiet:
		cmd.Stderr = stderr
	default:
		cmd.Stdout, cmd.Stderr = stdout, stderr
	}
	err := cmd.Run()
	if c.inOrder {
		stdout.Write(out.Bytes())
		stderr.Write(errs.Bytes())
	}
	res := result{out.String(), errs.String()}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return res, err
	}
	code := exit.ExitCode()
	if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		code = 128 + int(status.Signal())
	}
	message := ""
	if c.capture {
		message = fmt.Sprintf("`%s` failed: %s", strings.Join(append([]string{c.name}, c.args...), " "),
			firstNonEmpty(strings.TrimSpace(res.stderr), exit.Error()))
	}
	return res, &exitError{Code: code, Message: message}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// docker runs docker, showing its output.
func docker(args ...string) error {
	_, err := execute(command{name: "docker", args: args})
	return err
}

// dockerInOrder runs docker, showing its standard output and then its standard error
// once it exits.
func dockerInOrder(args ...string) error {
	_, err := execute(command{name: "docker", args: args, inOrder: true})
	return err
}

// dockerQuiet runs docker, hiding its standard output.
func dockerQuiet(args ...string) error {
	_, err := execute(command{name: "docker", args: args, quiet: true})
	return err
}

// dockerInput runs docker with the input on its standard input, hiding its output.
func dockerInput(input []byte, args ...string) error {
	_, err := execute(command{name: "docker", args: args, stdin: input, quiet: true})
	return err
}

// dockerOutput runs docker and returns what it wrote to standard output.
func dockerOutput(args ...string) (string, error) {
	res, err := execute(command{name: "docker", args: args, capture: true})
	return res.stdout, err
}

// exitStatus is the status an error ends gnat with: 0 for none, the command's own for an
// exitError, and 1 otherwise.
func exitStatus(err error) int {
	var exit *exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.Code
	}
	return 1
}
