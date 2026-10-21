// Package cli is gnat's command line: the host commands and the hidden subcommands the
// containers run.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// Run executes gnat with the given arguments (without the program name) and returns its
// exit status.
func Run(args []string) int {
	return run(args, os.Stdout, os.Stderr)
}

func run(args []string, out, errs io.Writer) int {
	stdout, stderr = out, errs
	root := newRoot()
	root.SetArgs(args)
	root.SetOut(out)
	root.SetErr(errs)
	err := root.Execute()
	if err == nil {
		return 0
	}
	var failed *failure
	if errors.As(err, &failed) {
		if msg := failed.err.Error(); msg != "" {
			fmt.Fprintf(errs, "error: %s\n", msg)
		}
		return exitStatus(failed.err)
	}
	// What's left is cobra's: the arguments or flags weren't usable.
	fmt.Fprintf(errs, "gnat: error: %s\nRun `gnat --help` for usage.\n", err)
	return 2
}

// A failure is a command that ran and failed, as opposed to arguments that didn't parse,
// which exit 2.
type failure struct{ err error }

func (f *failure) Error() string { return f.err.Error() }
func (f *failure) Unwrap() error { return f.err }

// A usageError is arguments that parsed but don't make sense, found by a command.
type usageError struct{ message string }

func (e *usageError) Error() string { return e.message }

func usagef(format string, args ...any) error {
	return &usageError{fmt.Sprintf(format, args...)}
}
