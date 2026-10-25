package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init")
	git(t, root, "commit", "--allow-empty", "-m", "initial")
	if err := os.Mkdir(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	original, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "nested"), alias} {
		got, err := Discover(path)
		if err != nil || got != original {
			t.Fatalf("%s: %+v %v", path, got, err)
		}
	}
	// Discovery leaves tracked/index state, branch and remotes untouched.
	if err := os.WriteFile(filepath.Join(root, "dirty"), []byte("local"), 0600); err != nil {
		t.Fatal(err)
	}
	before := git(t, root, "status", "--porcelain=v1") + git(t, root, "symbolic-ref", "HEAD") + git(t, root, "remote", "-v")
	if _, err := Discover(root); err != nil {
		t.Fatal(err)
	}
	after := git(t, root, "status", "--porcelain=v1") + git(t, root, "symbolic-ref", "HEAD") + git(t, root, "remote", "-v")
	if before != after {
		t.Fatal("discovery changed checkout")
	}
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, root, "worktree", "add", "-b", "linked", linked)
	other, err := Discover(linked)
	if err != nil {
		t.Fatal(err)
	}
	if other.Root == original.Root || other.CommonDir != original.CommonDir {
		t.Fatalf("linked checkout identity: %+v %+v", original, other)
	}
}

func TestRejectInvalidRepositories(t *testing.T) {
	for _, bare := range []bool{false, true} {
		root := t.TempDir()
		args := []string{"init"}
		if bare {
			args = append(args, "--bare")
		}
		git(t, root, args...)
		_, err := Discover(root)
		if err == nil {
			t.Fatal("accepted invalid workspace")
		}
		expected := "initial commit"
		if bare {
			expected = "bare"
		}
		if !strings.Contains(err.Error(), expected) {
			t.Fatal(err)
		}
	}
	if _, err := Discover(t.TempDir()); err == nil {
		t.Fatal("accepted non-repository")
	}
}
