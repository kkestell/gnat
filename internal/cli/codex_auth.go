package cli

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kkestell/gnat/internal/harness"
)

func loginCodex() error {
	home, err := os.MkdirTemp("", "gnat-codex-login-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	_, err = execute(command{
		name: "codex",
		args: []string{"-c", `cli_auth_credentials_store="file"`, "-c", `forced_login_method="chatgpt"`, "login"},
		env:  []string{"CODEX_HOME=" + home},
	})
	if err != nil {
		return errors.New("codex login failed; install the Codex CLI and complete ChatGPT sign-in with `gnat auth login codex`")
	}
	data, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		return errors.New("codex login did not save a ChatGPT credential")
	}
	credential, err := codexCredential(data)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	if err := setSecret("codex", string(encoded)); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Saved the Codex ChatGPT credential (expires %s).\n", time.Unix(credential.ExpiresAt, 0).UTC().Format(time.RFC3339))
	return nil
}

// codexCredential deliberately leaves the refresh token in the temporary login directory.
func codexCredential(data []byte) (harness.CodexCredential, error) {
	var login struct {
		Mode   string `json:"auth_mode"`
		Tokens struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	var credential harness.CodexCredential
	if json.Unmarshal(data, &login) != nil || login.Mode != "chatgpt" {
		return credential, errors.New("codex must sign in with a ChatGPT subscription")
	}
	parts := strings.Split(login.Tokens.AccessToken, ".")
	if len(parts) != 3 {
		return credential, errors.New("codex saved an invalid ChatGPT access token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		ExpiresAt int64 `json:"exp"`
	}
	if err != nil || json.Unmarshal(payload, &claims) != nil {
		return credential, errors.New("codex saved an invalid ChatGPT access token")
	}
	credential = harness.CodexCredential{AccessToken: login.Tokens.AccessToken, AccountID: login.Tokens.AccountID, ExpiresAt: claims.ExpiresAt}
	return credential, credential.Validate()
}
