package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kkestell/gnat/internal/v2/store"
)

// Opt in with GNAT_LIVE_TEST=1 go test ./internal/v2/daemon -run TestLiveLocalIntegration -v -timeout 12m.
// GNAT_LIVE_HARNESS and GNAT_LIVE_WORKER select providers; both default to claude.
func TestLiveLocalIntegration(t *testing.T) {
	if os.Getenv("GNAT_LIVE_TEST") != "1" {
		t.Skip("requires authenticated local harnesses")
	}
	settings := func(name string) store.Settings {
		switch name {
		case "", "claude":
			return store.Settings{Harness: "claude", Model: "sonnet", Effort: "low"}
		case "codex":
			return store.Settings{Harness: "codex", Model: "gpt-6.1-sol", Effort: "low"}
		case "opencode":
			return store.Settings{Harness: "opencode", Model: "deepseek/deepseek-flash", Effort: "default"}
		default:
			t.Fatalf("unsupported live harness %q", name)
			return store.Settings{}
		}
	}
	orchestrator, worker := settings(os.Getenv("GNAT_LIVE_HARNESS")), settings(os.Getenv("GNAT_LIVE_WORKER"))
	orchestrator.Workers = &worker
	team := newLiveTeam(t, orchestrator)
	root := team.root
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module integration.test/math\n\ngo 1.26\n")
	write("add.go", "package math\nfunc Add(a, b int) int { return 0 }\n")
	write("multiply.go", "package math\nfunc Multiply(a, b int) int { return 0 }\n")
	write("math_test.go", `package math
import "testing"
func TestAdd(t *testing.T) { if Add(2, 3) != 5 { t.Fatal("add") } }
func TestMultiply(t *testing.T) { if Multiply(2, 3) != 6 { t.Fatal("multiply") } }
`)
	write("notes.txt", "committed\n")
	git("add", ".")
	git("commit", "-m", "math fixture")
	base := git("rev-parse", "HEAD")
	write("notes.txt", "staged\n")
	git("add", "notes.txt")
	write("notes.txt", "unstaged\n")
	write("local.txt", "untracked local work\n")
	index, working, status := git("diff", "--cached"), git("diff"), git("status", "--porcelain")

	team.send(fmt.Sprintf(`Exercise local Git integration in this disposable repository. Use gnat MCP to create exactly two Gnat workers named add and multiply, both with harness %s, model %s, effort %s. Do not use native subagents. Worker add must implement Add in add.go as a+b, run go test -run '^TestAdd$', commit only add.go on its assigned branch, and send you its full commit SHA using gnat send_message. Worker multiply must implement Multiply in multiply.go as a*b, run go test -run '^TestMultiply$', commit only multiply.go on its assigned branch, and send you its full commit SHA using gnat send_message. Pass each worker your orchestrator ID and these instructions. Workers must leave notes.txt unchanged and must not integrate or remove agents. Both workers start from the current HEAD; do not merge until BOTH have been created. End your turn while awaiting their ordinary messages; do not poll or sleep waiting for them.
When the worker commit messages arrive, integrate both commits into this original checkout using ordinary git merge --no-edit with their branch or SHA. Preserve ALL existing staged, unstaged, and untracked local work: stash notes.txt and local.txt with --include-untracked before merging, and restore the stash with git stash pop --index after both merges. Keep branch main and do not commit the local notes. Run go test ./... here after both merges. On success say LOCAL-INTEGRATION-VERIFIED in your final response. Leave both workers present until I ask for removal. Do not push, use GitHub, change remotes, or create more workers.`, worker.Harness, worker.Model, worker.Effort))
	current := team.wait("worker commits integrated and verified", func(v store.Snapshot) bool {
		text := map[string]string{}
		for _, e := range v.Events {
			if e.AgentID == v.Orchestrator.ID && (e.Payload["type"] == "text_delta" || e.Payload["type"] == "message") {
				text[e.TurnID] += fmt.Sprint(e.Payload["text"])
			}
		}
		for _, turn := range text {
			if strings.Contains(turn, "LOCAL-INTEGRATION-VERIFIED") {
				return len(v.Workers) == 2 && settled(v)
			}
		}
		return false
	})
	if git("rev-parse", "HEAD") == base || git("branch", "--show-current") != "main" || git("remote") != "" || git("diff", "--cached") != index || git("diff") != working || git("status", "--porcelain") != status {
		t.Fatal("integration did not preserve original checkout state")
	}
	if data, err := os.ReadFile(filepath.Join(root, "local.txt")); err != nil || string(data) != "untracked local work\n" {
		t.Fatal("lost untracked file", err)
	}
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("integrated tests: %s %v", out, err)
	}
	for _, a := range current.Workers {
		if a.Settings != worker || a.BaseCommit != base || a.WorkingDir == root || a.Branch == nil {
			t.Fatal("incorrect worker checkout/settings", a)
		}
		commit := git("rev-parse", *a.Branch)
		if commit == base {
			t.Fatal("worker did not commit", a.Name)
		}
		git("merge-base", "--is-ancestor", commit, "HEAD")
		found := false
		for _, m := range current.Messages {
			if m.Sender == a.ID && m.AgentID == current.Orchestrator.ID && strings.Contains(m.Text, commit) {
				found = true
			}
		}
		if !found {
			t.Fatal("missing worker commit message", a.Name)
		}
		t.Logf("%s worker %s committed %s; integrated in %s checkout", a.Settings.Harness, a.Name, commit, orchestrator.Harness)
	}
	team.send("Remove both workers using gnat remove_agent now that their commits are integrated. Do not change any files or Git state. End after the requests are accepted.")
	final := team.wait("integrated workers removed with history retained", func(v store.Snapshot) bool {
		return len(v.Workers) == 0 && len(v.RemovedWorkers) == 2 && settled(v)
	})
	if len(final.Messages) < len(current.Messages)+1 || git("branch", "--format=%(refname:short)") != "main" {
		t.Fatal("cleanup lost history or left worker branches")
	}
	for _, a := range final.RemovedWorkers {
		if _, err := os.Stat(a.WorkingDir); !os.IsNotExist(err) {
			t.Fatal("removed worktree remains", err)
		}
	}
	if git("diff", "--cached") != index || git("diff") != working || git("status", "--porcelain") != status {
		t.Fatal("removal changed original local work")
	}
}
