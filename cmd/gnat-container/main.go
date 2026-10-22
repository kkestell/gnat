// Command gnat-container is what runs inside gnat's containers: the control plane, the
// agent daemon, the PostToolUse hook, and the commands agents message each other and work
// with pull requests by.
package main

import (
	"fmt"
	"os"

	"github.com/kkestell/gnat/internal/agent"
	"github.com/kkestell/gnat/internal/control"
	"github.com/kkestell/gnat/internal/hook"
	"github.com/kkestell/gnat/internal/pr"
)

// The subcommands. Each takes the arguments after its name as they are, flags included,
// and returns the exit status.
var commands = map[string]func(args []string) int{
	"control": control.Main,
	"agent":   agent.Main,
	"hook":    hook.Main,
	"send":    hook.SendMain,
	"pr":      pr.Main,
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: gnat-container control|agent|hook|send|pr [ARGS...]")
		return 2
	}
	command, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "gnat-container: unknown command '%s': use control, agent, hook, send, or pr\n", args[0])
		return 2
	}
	return command(args[1:])
}
