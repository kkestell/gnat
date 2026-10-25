// Command gnat opens a local workspace through the per-user daemon.
package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/kkestell/gnat/internal/daemon"
	"github.com/kkestell/gnat/internal/entry"
	"github.com/kkestell/gnat/internal/store"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--internal-daemon-run" {
		if runtime.GOOS != "darwin" {
			fmt.Fprintln(os.Stderr, "Gnat currently supports macOS only")
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
