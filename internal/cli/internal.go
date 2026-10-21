package cli

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	daemon "github.com/kkestell/gnat/internal/agent"
	"github.com/kkestell/gnat/internal/hook"
)

// The hidden subcommands the containers run, `gnat internal NAME ...`. Each takes the
// arguments after its name as they are, flags included, and returns the exit status.
//
// TODO(control): call control.Main once that package is on main.
var internalMains = map[string]func(args []string) int{
	"control": notYet("control"),
	"agent":   daemon.Main,
	"hook":    hook.Main,
	"send":    hook.SendMain,
}

func notYet(name string) func([]string) int {
	return func([]string) int {
		fmt.Fprintf(stderr, "gnat internal %s: not implemented yet\n", name)
		return 1
	}
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
	go func() { _ = cmd.Wait() }()
	return nil
}
