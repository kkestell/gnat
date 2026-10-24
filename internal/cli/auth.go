package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kkestell/gnat/internal/harness"
	"github.com/kkestell/gnat/internal/keyring"
)

// The system keyring, where `gnat auth login` saves each harness's credential under the
// harness's name and `gnat up` reads it, unless a test says otherwise.
var (
	getSecret    = keyring.Get
	setSecret    = keyring.Set
	deleteSecret = keyring.Delete
)

func authCommands() *cobra.Command {
	harnesses := strings.Join(harness.Names(), "|")
	return group("auth", "save and remove the credentials agents use",
		&cobra.Command{
			Use: "login " + harnesses, Short: "save a harness's credential in the system keyring", Args: cobra.ExactArgs(1),
			RunE: action(func(_ *cobra.Command, args []string) error { return login(args[0]) }).run,
		},
		&cobra.Command{
			Use: "logout " + harnesses, Short: "remove a harness's credential from the system keyring", Args: cobra.ExactArgs(1),
			RunE: action(func(_ *cobra.Command, args []string) error { return logout(args[0]) }).run,
		},
	)
}

func login(name string) error {
	h, err := harness.Get(name)
	if err != nil {
		return err
	}
	if name == "codex" {
		return loginCodex()
	}
	secret, err := readSecret(h.CredentialName())
	if err != nil {
		return err
	}
	if secret == "" {
		return fmt.Errorf("the %s is empty", h.CredentialName())
	}
	if err := setSecret(h.Name(), secret); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Saved the %s.\n", h.CredentialName())
	return nil
}

// readSecret reads a secret without echo from a terminal, or as one line from other input.
func readSecret(prompt string) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(stderr, "%s: ", prompt)
		secret, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		return strings.TrimSpace(string(secret)), err
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if line == "" && err != nil {
		return "", errors.New("no credential on standard input")
	}
	return strings.TrimSpace(line), nil
}

func logout(name string) error {
	h, err := harness.Get(name)
	if err != nil {
		return err
	}
	removed, err := deleteSecret(h.Name())
	if err != nil {
		return err
	}
	if removed {
		fmt.Fprintf(stdout, "Removed the %s.\n", h.CredentialName())
	} else {
		fmt.Fprintf(stdout, "No %s is saved.\n", h.CredentialName())
	}
	return nil
}

// loadCredential is the harness's credential, such as a Claude Code OAuth token, which
// `gnat auth login` saved.
func loadCredential(h harness.Harness) (string, error) {
	value, err := getSecret(h.Name())
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("no %s is saved; %s, then run `gnat auth login %s`",
			h.CredentialName(), h.CredentialSource(), h.Name())
	}
	if h.Name() == "codex" {
		var credential harness.CodexCredential
		if json.Unmarshal([]byte(value), &credential) != nil {
			return "", fmt.Errorf("invalid Codex ChatGPT credential; run `gnat auth login codex`")
		}
		if err := credential.Validate(); err != nil {
			return "", err
		}
	}
	return value, nil
}
