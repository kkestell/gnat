// Command gnat opens a local v2 workspace through the per-user daemon.
package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/kkestell/gnat/internal/v2/daemon"
	"github.com/kkestell/gnat/internal/v2/entry"
	"github.com/kkestell/gnat/internal/v2/store"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--internal-daemon-run" {
		if runtime.GOOS != "darwin" {
			fmt.Fprintln(os.Stderr, "Gnat v2 currently supports macOS only")
			os.Exit(1)
		}
		dir, err := store.StateDir()
		if err == nil {
			err = daemon.Run(dir)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(entry.Run(os.Args[1:], os.Stdout, os.Stderr))
}
