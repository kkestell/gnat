// Package cli is gnat's command line: the host commands and the hidden subcommands the
// containers run.
package cli

import (
	"fmt"
	"os"
)

// Run executes gnat with the given arguments (without the program name) and returns its
// exit status.
func Run(args []string) int {
	fmt.Fprintln(os.Stderr, "gnat: not implemented yet")
	return 1
}
