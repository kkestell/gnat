package cli

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/harness"
)

func codexLoginJSON(mode string, expiry int64) []byte {
	payload, _ := json.Marshal(map[string]any{"exp": expiry})
	token := "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
	data, _ := json.Marshal(map[string]any{"auth_mode": mode, "tokens": map[string]any{
		"access_token": token, "account_id": "account", "refresh_token": "never-save-this",
	}})
	return data
}

func TestCodexExpiredKeyringCredentialFailsBeforeStartingContainers(t *testing.T) {
	useSecrets(t, map[string]string{"codex": `{"access_token":"expired","account_id":"account","expires_at":1}`})
	if _, err := loadCredential(newCodexHarness(t)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatal("expired credential passed preflight")
	}
}

func newCodexHarness(t *testing.T) harness.Harness {
	t.Helper()
	h, err := harness.Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestCodexLoginKeepsOnlyAnAccessSnapshotAndRemovesTemporaryFiles(t *testing.T) {
	secrets := useSecrets(t, nil)
	out, _ := useOutput(t)
	var home string
	useExec(t, func(c command) (result, error) {
		if c.name != "codex" || !strings.Contains(strings.Join(c.args, " "), `forced_login_method="chatgpt"`) {
			t.Fatalf("unexpected login command: %v", c.args)
		}
		home = strings.TrimPrefix(c.env[0], "CODEX_HOME=")
		info, err := os.Stat(home)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatal("login directory is not private")
		}
		return result{}, os.WriteFile(filepath.Join(home, "auth.json"), codexLoginJSON("chatgpt", time.Now().Add(time.Hour).Unix()), 0o600)
	})
	if err := login("codex"); err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal([]byte(secrets["codex"]), &saved); err != nil || len(saved) != 3 || saved["account_id"] != "account" {
		t.Fatal("did not save the access snapshot")
	}
	if strings.Contains(secrets["codex"], "never-save-this") || strings.Contains(out.String(), "signature") {
		t.Fatal("credential exposed")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("temporary login not removed")
	}
	if err := logout("codex"); err != nil || secrets["codex"] != "" {
		t.Fatal("logout did not remove the credential")
	}
}

func TestCodexLoginFailuresPreserveTheSavedCredential(t *testing.T) {
	for _, mode := range []string{"cancelled", "missing-file", "api", "malformed", "expired"} {
		t.Run(mode, func(t *testing.T) {
			secrets := useSecrets(t, map[string]string{"codex": "previous"})
			useOutput(t)
			var home string
			useExec(t, func(c command) (result, error) {
				home = strings.TrimPrefix(c.env[0], "CODEX_HOME=")
				if mode == "cancelled" {
					return result{}, errors.New("cancelled")
				}
				if mode == "missing-file" {
					return result{}, nil
				}
				data := codexLoginJSON("chatgpt", time.Now().Add(time.Hour).Unix())
				switch mode {
				case "api":
					data = codexLoginJSON("apikey", time.Now().Add(time.Hour).Unix())
				case "malformed":
					data = []byte("secret invalid JSON")
				case "expired":
					data = codexLoginJSON("chatgpt", time.Now().Add(-time.Hour).Unix())
				}
				return result{}, os.WriteFile(filepath.Join(home, "auth.json"), data, 0o600)
			})
			if err := login("codex"); err == nil || strings.Contains(err.Error(), "secret invalid JSON") {
				t.Fatal("expected a safe login failure")
			}
			if secrets["codex"] != "previous" {
				t.Fatal("overwrote the saved credential")
			}
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Fatal("temporary login not removed")
			}
		})
	}
}
