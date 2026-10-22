package cli

import (
	"strings"
	"testing"

	"github.com/kkestell/gnat/internal/harness"
)

func TestLoginSavesTheHarnessCredentialAndLogoutRemovesIt(t *testing.T) {
	secrets := useSecrets(t, nil)
	out, _ := useOutput(t)
	saved := stdin
	t.Cleanup(func() { stdin = saved })

	stdin = strings.NewReader("  or-key  \n")
	if err := login("ox"); err != nil {
		t.Fatal(err)
	}
	ox, _ := harness.Get("ox")
	if secrets["ox"] != "or-key" {
		t.Errorf("saved %v", secrets)
	}
	if got, err := loadCredential(ox); err != nil || got != "or-key" {
		t.Errorf("got %q, %v", got, err)
	}

	if err := logout("ox"); err != nil {
		t.Fatal(err)
	}
	if err := logout("ox"); err != nil {
		t.Fatal(err)
	}
	want := "Saved the OpenRouter API key.\nRemoved the OpenRouter API key.\nNo OpenRouter API key is saved.\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
	_, err := loadCredential(ox)
	if want := "no OpenRouter API key is saved; create an OpenRouter API key, then run `gnat auth login ox`"; err == nil || err.Error() != want {
		t.Errorf("got %v, want %q", err, want)
	}
}

func TestLoginRefusesAnEmptyCredentialOrUnknownHarness(t *testing.T) {
	secrets := useSecrets(t, nil)
	useOutput(t)
	saved := stdin
	t.Cleanup(func() { stdin = saved })

	stdin = strings.NewReader("\n")
	if err := login("claude"); err == nil || err.Error() != "the Claude Code OAuth token is empty" {
		t.Errorf("got %v", err)
	}
	stdin = strings.NewReader("")
	if err := login("claude"); err == nil || err.Error() != "no credential on standard input" {
		t.Errorf("got %v", err)
	}
	if err := login("nope"); err == nil || !strings.Contains(err.Error(), `unknown harness "nope"`) {
		t.Errorf("got %v", err)
	}
	if len(secrets) != 0 {
		t.Errorf("saved %v", secrets)
	}
}
