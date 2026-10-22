package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/kkestell/gnat/internal/state"
)

// fakeExec replaces execute for a test, recording each command it's given and answering
// with respond (which may be nil to succeed with no output).
type fakeExec struct {
	calls   []command
	respond func(c command) (result, error)
}

func useExec(t *testing.T, respond func(c command) (result, error)) *fakeExec {
	t.Helper()
	f := &fakeExec{respond: respond}
	saved := execute
	execute = func(c command) (result, error) {
		f.calls = append(f.calls, c)
		if f.respond == nil {
			return result{}, nil
		}
		return f.respond(c)
	}
	t.Cleanup(func() { execute = saved })
	return f
}

// lines are the calls' command lines, one string each.
func (f *fakeExec) lines() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, strings.Join(append([]string{c.name}, c.args...), " "))
	}
	return out
}

// useOutput captures what the command writes to stdout and stderr.
func useOutput(t *testing.T) (out, errs *bytes.Buffer) {
	t.Helper()
	out, errs = &bytes.Buffer{}, &bytes.Buffer{}
	savedOut, savedErr := stdout, stderr
	stdout, stderr = out, errs
	t.Cleanup(func() { stdout, stderr = savedOut, savedErr })
	return out, errs
}

// useSwarm points the state at a fresh directory, with a swarm "demo" in it.
func useSwarm(t *testing.T, agents ...state.Agent) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := state.Save(&state.Swarm{Name: "demo", Repo: "o/r", Agents: agents}); err != nil {
		t.Fatal(err)
	}
}

func agent(name, harness string) state.Agent {
	return state.Agent{Name: name, Instructions: "x", Harness: harness}
}

// withoutIDs are the agents with their IDs cleared, after checking each has one of its own.
func withoutIDs(t *testing.T, agents []state.Agent) []state.Agent {
	t.Helper()
	seen := map[string]bool{}
	out := make([]state.Agent, len(agents))
	for i, a := range agents {
		if a.ID == "" || seen[a.ID] {
			t.Errorf("agent %s has ID %q", a.Name, a.ID)
		}
		seen[a.ID] = true
		a.ID = ""
		out[i] = a
	}
	return out
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o644) }

// useSecrets replaces the system keyring with a map, which it returns, starting with
// secrets.
func useSecrets(t *testing.T, secrets map[string]string) map[string]string {
	t.Helper()
	if secrets == nil {
		secrets = map[string]string{}
	}
	savedGet, savedSet, savedDelete := getSecret, setSecret, deleteSecret
	getSecret = func(account string) (string, error) { return secrets[account], nil }
	setSecret = func(account, secret string) error { secrets[account] = secret; return nil }
	deleteSecret = func(account string) (bool, error) {
		_, ok := secrets[account]
		delete(secrets, account)
		return ok, nil
	}
	t.Cleanup(func() { getSecret, setSecret, deleteSecret = savedGet, savedSet, savedDelete })
	return secrets
}
