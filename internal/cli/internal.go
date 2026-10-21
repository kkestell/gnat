package cli

import (
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	daemon "github.com/kkestell/gnat/internal/agent"
	"github.com/kkestell/gnat/internal/control"
	"github.com/kkestell/gnat/internal/hook"
)

// The hidden subcommands the containers run, `gnat internal NAME ...`. Each takes the
// arguments after its name as they are, flags included, and returns the exit status.
var internalMains = map[string]func(args []string) int{
	"control": control.Main,
	"agent":   daemon.Main,
	"hook":    hook.Main,
	"send":    hook.SendMain,
}

// internalCommand is `gnat internal`, which `gnat --help` doesn't list.
func internalCommand() *cobra.Command {
	c := &cobra.Command{
		Use: "internal", Short: "run what runs inside containers", Hidden: true,
		RunE: needSubcommand, SilenceUsage: true, SilenceErrors: true,
	}
	for _, name := range []string{"control", "agent", "hook", "send"} {
		c.AddCommand(&cobra.Command{
			Use: name, Hidden: true, DisableFlagParsing: true, SilenceUsage: true, SilenceErrors: true,
			RunE: func(_ *cobra.Command, args []string) error {
				if code := internalMains[name](args); code != 0 {
					return &failure{&exitError{Code: code}}
				}
				return nil
			},
		})
	}
	return c
}

// openBrowser opens a URL in the user's default browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// An opener that finds no browser fails at once; one that has started one may run
	// for as long as it stays open, so a few seconds is long enough to wait.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(openerPatience):
		return nil
	}
}

const openerPatience = 3 * time.Second
