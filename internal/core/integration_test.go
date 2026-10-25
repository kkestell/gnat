package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kkestell/gnat/internal/store"
)

func writeIntegrationFile(t *testing.T, root, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

// Controlled adapter turns exercise Git and the real scheduler without a provider.
func TestLocalIntegration(t *testing.T) {
	root := repository(t)
	writeIntegrationFile(t, root, "go.mod", "module integration.test/math\n\ngo 1.26\n")
	writeIntegrationFile(t, root, "add.go", "package math\nfunc Add(a, b int) int { return 0 }\n")
	writeIntegrationFile(t, root, "multiply.go", "package math\nfunc Multiply(a, b int) int { return 0 }\n")
	writeIntegrationFile(t, root, "math_test.go", `package math
import "testing"
func TestAdd(t *testing.T) { if Add(2, 3) != 5 { t.Fatal("add") } }
func TestMultiply(t *testing.T) { if Multiply(2, 3) != 6 { t.Fatal("multiply") } }
`)
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "math fixture")
	base := git(t, root, "rev-parse", "HEAD")
	// Preserve both layers of tracked local work and an untracked file.
	writeIntegrationFile(t, root, "file", "staged\n")
	git(t, root, "add", "file")
	writeIntegrationFile(t, root, "file", "unstaged\n")
	writeIntegrationFile(t, root, "untracked", "local\n")
	index := git(t, root, "diff", "--cached")
	working := git(t, root, "diff")
	status := git(t, root, "status", "--porcelain")

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v := newWorkspace(t, db, root)
	f := &fake{runs: make(chan *run, 20)}
	s, err := New(db, f)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ready(t, db, v.ID, "ready")
	b := Binding{v.ID, v.Orchestrator.ID}
	for _, name := range []string{"add", "multiply"} {
		coordinate(t, s, b, "create_agent", store.Command{Name: name, Prompt: "implement " + name})
	}
	workers := []*run{next(t, f), next(t, f)}
	commits := map[string]string{}
	for _, r := range workers {
		a := r.claim.Agent
		if a.WorkingDir == root || a.BaseCommit != base {
			t.Fatal("incorrect worker checkout", a)
		}
		if got := git(t, a.WorkingDir, "show", "HEAD:file"); got != "committed" {
			t.Fatal("worker inherited local changes", got)
		}
		expression := "a + b"
		function := "Add"
		if a.Name == "multiply" {
			expression, function = "a * b", "Multiply"
		}
		writeIntegrationFile(t, a.WorkingDir, a.Name+".go", "package math\nfunc "+function+"(a, b int) int { return "+expression+" }\n")
		git(t, a.WorkingDir, "add", a.Name+".go")
		git(t, a.WorkingDir, "commit", "-m", "implement "+a.Name)
		commits[a.ID] = git(t, a.WorkingDir, "rev-parse", "HEAD")
		r.done <- nil
		workerState(t, db, v.ID, a.ID, func(a store.Agent) bool { return a.Lifecycle == "ready" })
	}
	if got := git(t, root, "rev-parse", "HEAD"); got != base {
		t.Fatal("worker commit moved original HEAD", got)
	}
	if git(t, root, "diff", "--cached") != index || git(t, root, "diff") != working {
		t.Fatal("worker execution changed local work")
	}
	git(t, root, "stash", "push", "--include-untracked", "-m", "local integration fixture", "--", "file", "untracked")
	for _, r := range workers {
		a := r.claim.Agent
		coordinate(t, s, Binding{v.ID, a.ID}, "send_message", store.Command{AgentID: b.AgentID, Text: "commit " + commits[a.ID]})
		integration := next(t, f)
		if integration.claim.Agent.WorkingDir != root || integration.claim.Sender != a.ID || !strings.Contains(integration.claim.Text, commits[a.ID]) {
			t.Fatal("result did not reach original-checkout orchestrator", integration.claim)
		}
		git(t, root, "merge", "--no-edit", *a.Branch)
		integration.done <- nil
		ready(t, db, v.ID, "ready")
	}
	git(t, root, "stash", "pop", "--index")
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("integrated functional tests: %s %v", out, err)
	}
	if git(t, root, "branch", "--show-current") != "main" || git(t, root, "remote") != "" || git(t, root, "diff", "--cached") != index || git(t, root, "diff") != working || git(t, root, "status", "--porcelain") != status {
		t.Fatal("integration changed unrelated local state")
	}
	if data, err := os.ReadFile(filepath.Join(root, "untracked")); err != nil || string(data) != "local\n" {
		t.Fatal("lost untracked local work", err)
	}
	for _, r := range workers {
		a := r.claim.Agent
		git(t, root, "merge-base", "--is-ancestor", commits[a.ID], "HEAD")
		coordinate(t, s, b, "remove_agent", store.Command{AgentID: a.ID})
		workerState(t, db, v.ID, a.ID, func(a store.Agent) bool { return a.Membership == "removed" })
		if _, err := os.Stat(a.WorkingDir); !os.IsNotExist(err) {
			t.Fatal("integrated worktree still exists", err)
		}
	}
	got, err := db.Snapshot(v.ID)
	if err != nil || len(got.RemovedWorkers) != 2 || len(got.Messages) != 4 {
		t.Fatal("lost integration history", err)
	}
	if branches := git(t, root, "branch", "--format=%(refname:short)"); branches != "main" {
		t.Fatal("integrated worker branches remain", branches)
	}
}

func TestLocalIntegrationConflictCanAbort(t *testing.T) {
	root := repository(t)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v := newWorkspace(t, db, root)
	f := &fake{runs: make(chan *run, 10)}
	s, err := New(db, f)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b := Binding{v.ID, v.Orchestrator.ID}
	coordinate(t, s, b, "create_agent", store.Command{Name: "conflict", Prompt: "change file"})
	r := next(t, f)
	a := r.claim.Agent
	writeIntegrationFile(t, a.WorkingDir, "file", "worker\n")
	git(t, a.WorkingDir, "add", "file")
	git(t, a.WorkingDir, "commit", "-m", "worker change")
	commit := git(t, a.WorkingDir, "rev-parse", "HEAD")
	r.done <- nil
	workerState(t, db, v.ID, a.ID, func(a store.Agent) bool { return a.Lifecycle == "ready" })
	writeIntegrationFile(t, root, "file", "original\n")
	git(t, root, "add", "file")
	git(t, root, "commit", "-m", "original change")
	head := git(t, root, "rev-parse", "HEAD")
	if out, err := exec.Command("git", "-C", root, "merge", "--no-edit", *a.Branch).CombinedOutput(); err == nil {
		t.Fatalf("expected merge conflict: %s", out)
	}
	if git(t, root, "diff", "--name-only", "--diff-filter=U") != "file" {
		t.Fatal("missing conflicted path")
	}
	git(t, root, "merge", "--abort")
	if git(t, root, "rev-parse", "HEAD") != head || git(t, root, "status", "--porcelain") != "" || git(t, root, "show", "HEAD:file") != "original" || git(t, a.WorkingDir, "rev-parse", "HEAD") != commit {
		t.Fatal("merge abort lost work")
	}
	remove := store.Command{RequestID: store.NewID(), AgentID: a.ID}
	coordinate(t, s, b, "remove_agent", remove)
	failed := workerState(t, db, v.ID, a.ID, func(a store.Agent) bool { return a.Membership == "removing" && a.Error != "" })
	if failed.Stage != "removing_branch" || git(t, root, "show", commit+":file") != "worker" || git(t, root, "rev-parse", *a.Branch) != commit {
		t.Fatal("unmerged removal lost worker commit", failed)
	}
	// Resolve with ordinary Git, then retry the original removal operation.
	git(t, root, "merge", "--no-edit", "-X", "theirs", *a.Branch)
	git(t, root, "merge-base", "--is-ancestor", commit, "HEAD")
	coordinate(t, s, b, "remove_agent", remove)
	workerState(t, db, v.ID, a.ID, func(a store.Agent) bool { return a.Membership == "removed" })
	if git(t, root, "show", "HEAD:file") != "worker" {
		t.Fatal("conflict resolution missing")
	}
}
