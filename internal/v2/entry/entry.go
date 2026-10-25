// Package entry implements the minimal v2 command surface.
package entry

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/kkestell/gnat/internal/v2/daemon"
	"github.com/kkestell/gnat/internal/v2/store"
	"github.com/kkestell/gnat/internal/v2/workspace"
	"github.com/spf13/cobra"
)

// Version can be supplied with -ldflags at release time.
var Version = "v2-dev"

func Run(args []string, out, errs io.Writer) int {
	root := &cobra.Command{Use: "gnat", Short: "Open a local coding-agent workspace", SilenceUsage: true, SilenceErrors: true, Args: cobra.NoArgs, Version: Version}
	root.CompletionOptions.DisableDefaultCmd = true
	var noOpen bool
	root.Flags().BoolVar(&noOpen, "no-open", false, "print the workspace URL without opening a browser")
	root.RunE = func(_ *cobra.Command, _ []string) error {
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("gnat v2 currently supports macOS only")
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		ws, err := workspace.Discover(cwd)
		if err != nil {
			return err
		}
		dir, err := store.StateDir()
		if err != nil {
			return err
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		desc, err := daemon.Ensure(ctx, dir, executable)
		if err != nil {
			return err
		}
		snapshot, err := daemon.OpenWorkspace(ctx, desc, ws.Root)
		if err != nil {
			return err
		}
		address := desc.URL + "/swarms/" + snapshot.ID
		fmt.Fprintln(out, address)
		if !noOpen {
			if err := exec.Command("open", address).Run(); err != nil {
				fmt.Fprintf(errs, "Could not open browser: %v. Open %s\n", err, address)
			}
		}
		return nil
	}
	stop := &cobra.Command{Use: "stop", Short: "Stop the daemon, preserving saved swarms", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		dir, err := store.StateDir()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return daemon.Stop(ctx, dir)
	}}
	maintenance := &cobra.Command{Use: "daemon", Short: "Daemon maintenance", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	maintenance.AddCommand(stop)
	root.AddCommand(maintenance)
	root.SetOut(out)
	root.SetErr(errs)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		fmt.Fprintf(errs, "gnat: %v\n", err)
		return 1
	}
	return 0
}
