// Package agent is the agent daemon, each agent container's main process: it clones the
// repository, installs the swarm's files, and runs the agent's harness on each message
// the control plane pushes to it.
package agent

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kkestell/gnat/internal/harness"
	"github.com/kkestell/gnat/internal/protocol"
	"github.com/kkestell/gnat/internal/state"
)

// fileFlags collects each --file STAGED:PATH.
type fileFlags []stagedFile

func (f *fileFlags) String() string { return "" }

func (f *fileFlags) Set(value string) error {
	staged, path, ok := strings.Cut(value, ":")
	if !ok {
		return fmt.Errorf("%q isn't STAGED:PATH", value)
	}
	*f = append(*f, stagedFile{staged, path})
	return nil
}

// Main is `gnat internal agent NAME --repo REPO ...`; args exclude those words. It runs
// until the process is killed, and returns only an exit status for a failure to start.
func Main(args []string) int {
	return run(args, os.Stderr, serve)
}

// run parses args and sets the workspace up; serve then runs the daemon.
func run(args []string, stderr io.Writer, serve func(name string, h harness.Harness, model, effort string)) int {
	fs := flag.NewFlagSet("gnat internal agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "GitHub repository to work in")
	var files fileFlags
	fs.Var(&files, "file", "a staged file to put in the workspace at PATH, as STAGED:PATH (repeatable)")
	harnessName := fs.String("harness", state.DefaultHarness, "the harness: "+strings.Join(harness.Names(), ", "))
	model := fs.String("model", "", "the model")
	effort := fs.String("effort", "", "the effort")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: gnat internal agent NAME --repo REPO [--file STAGED:PATH]... [--harness H] [--model M] [--effort E]")
		fs.PrintDefaults()
	}
	names, err := parseInterleaved(fs, args)
	if err == flag.ErrHelp {
		return 0
	}
	if err != nil {
		return 2
	}
	h, harnessErr := harness.Get(*harnessName)
	problem := ""
	switch {
	case len(names) != 1:
		problem = "expected one agent name"
	case *repo == "":
		problem = "--repo is required"
	case harnessErr != nil:
		problem = harnessErr.Error()
	}
	if problem != "" {
		fmt.Fprintf(stderr, "gnat internal agent: %s\n", problem)
		fs.Usage()
		return 2
	}
	name := names[0]

	// The harness, its hooks, and its tools inherit this; the hook and `internal send`
	// need it.
	os.Setenv(state.AgentVar, name)

	// Clone before attaching, so the harness never runs in an empty workspace.
	err = clone(*repo)
	if err == nil {
		err = installFiles(files)
	}
	if err != nil {
		_, _ = call(protocol.Request{Op: protocol.OpOutput, Agent: name, Text: fmt.Sprintf("error: %v\n", err)})
		return 1
	}
	serve(name, h, *model, *effort)
	return 0
}

// serve runs the daemon, attaching to the control plane again whenever the connection drops.
func serve(name string, h harness.Harness, model, effort string) {
	daemon := NewDaemon(func(emit harness.Emit) harness.Runner { return h.Runner(model, effort, emit) })
	for {
		daemon.Serve(Open(name, daemon.Stop, controlAddr))
	}
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
