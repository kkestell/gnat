package cli

import (
	"os/exec"
	"runtime"
	"time"
)

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
