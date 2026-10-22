// Package hook is the PostToolUse hook every harness runs, and the command agents message
// their teammates with.
//
// The hook adds messages sent with `gnat send --now` to the running turn: Claude Code and
// Ox run it after each tool call and add what it prints to the turn. Both run in
// containers as `gnat-container hook` and `gnat-container send`.
package hook

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kkestell/gnat/internal/layout"
	"github.com/kkestell/gnat/internal/protocol"
	"github.com/kkestell/gnat/internal/state"
)

// call makes a request to the control plane; a variable so tests can fake it.
var call = func(req protocol.Request, timeout time.Duration) (*protocol.Response, error) {
	return protocol.Call(state.Control, req, timeout)
}

// Main is `gnat-container hook`; args are those after it. It returns an exit status.
func Main(args []string) int {
	return runHook(os.Getenv(state.AgentVar), os.Stdout, os.Stderr)
}

func runHook(agent string, stdout, stderr io.Writer) int {
	if agent == "" {
		fmt.Fprintf(stderr, "gnat-container hook: %s is not set\n", state.AgentVar)
		return 1
	}
	resp, err := call(protocol.Request{Op: protocol.OpTake, Agent: agent}, layout.HookTimeoutSeconds*time.Second)
	if err != nil {
		// Never break the turn over a message; it waits for the next one instead.
		fmt.Fprintf(stderr, "gnat-container hook: %v\n", err)
		return 0
	}
	if len(resp.Messages) == 0 {
		return 0
	}
	parts := make([]string, len(resp.Messages))
	for i, m := range resp.Messages {
		who := "the swarm operator"
		if m.Sender != nil && *m.Sender != "" {
			who = "agent " + *m.Sender
		}
		parts[i] = fmt.Sprintf("Message from %s, received while you were working:\n\n%s", who, m.Text)
	}
	out, err := protocol.Encode(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":     "PostToolUse",
		"additionalContext": strings.Join(parts, "\n\n"),
	}})
	if err != nil {
		fmt.Fprintf(stderr, "gnat-container hook: %v\n", err)
		return 0
	}
	// The newline protocol.Encode ends with is harmless to the harness.
	_, _ = stdout.Write(out)
	return 0
}
