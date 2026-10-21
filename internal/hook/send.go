package hook

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kkestell/gnat/internal/protocol"
	"github.com/kkestell/gnat/internal/state"
)

// SendMain is `gnat internal send AGENT... [-m TEXT] [--now]`, which an agent runs inside
// its container to message other agents in the swarm; args are those after `send`. It
// returns an exit status: 2 for a usage error, 1 for any other failure.
func SendMain(args []string) int {
	return runSend(args, os.Getenv(state.AgentVar), os.Stdin, os.Stdout, os.Stderr)
}

func runSend(args []string, sender string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gnat internal send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	message := fs.String("m", "", "the message; read from stdin if omitted")
	fs.StringVar(message, "message", "", "the message; read from stdin if omitted")
	now := fs.Bool("now", false, "deliver into each agent's running turn at its next tool call")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: gnat internal send AGENT... [-m TEXT] [--now]")
		fs.PrintDefaults()
	}
	agents, err := parseInterleaved(fs, args)
	if err == flag.ErrHelp {
		return 0
	}
	if err != nil {
		return 2
	}
	if len(agents) == 0 {
		fmt.Fprintln(stderr, "gnat internal send: expected at least one agent to message")
		fs.Usage()
		return 2
	}
	if sender == "" {
		fmt.Fprintf(stderr, "error: %s is not set; this only works inside an agent's turn\n", state.AgentVar)
		return 1
	}
	text := *message
	if !flagGiven(fs, "m", "message") {
		data, err := io.ReadAll(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		text = string(data)
	}
	if strings.TrimSpace(text) == "" {
		fmt.Fprintln(stderr, "error: message is empty")
		return 1
	}
	for _, agent := range agents {
		_, err := call(protocol.Request{
			Op: protocol.OpSend, Agent: agent, Text: text, Now: *now, Sender: sender,
		}, 0)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "sent to %s\n", agent)
	}
	return 0
}

// flagGiven reports whether any of the flags was set on the command line.
func flagGiven(fs *flag.FlagSet, names ...string) bool {
	given := false
	fs.Visit(func(f *flag.Flag) {
		for _, name := range names {
			given = given || f.Name == name
		}
	})
	return given
}

// parseInterleaved parses args with fs like argparse does: flags may come after the
// positional arguments. It returns the positional arguments.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			return append(positional, rest...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}
