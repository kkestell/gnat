package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// useWorkspace points the daemon at a workspace directory for the test.
func useWorkspace(t *testing.T, dir string) {
	t.Helper()
	old := workspacePath
	workspacePath = dir
	t.Cleanup(func() { workspacePath = old })
}

func TestCloneKeepsAnExistingClone(t *testing.T) {
	dir := t.TempDir()
	useWorkspace(t, dir)
	var commands [][]string
	oldRun := runCommand
	runCommand = func(name string, args ...string) (string, error) {
		commands = append(commands, append([]string{name}, args...))
		return `{"name": "A", "login": "a", "id": 1}`, nil
	}
	t.Cleanup(func() { runCommand = oldRun })

	cloned := func() bool {
		for _, c := range commands {
			if reflect.DeepEqual(c[:3], []string{"gh", "repo", "clone"}) {
				return true
			}
		}
		return false
	}
	if err := clone("o/r"); err != nil {
		t.Fatal(err)
	}
	if !cloned() {
		t.Fatal("no clone")
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	commands = nil
	if err := clone("o/r"); err != nil {
		t.Fatal(err)
	}
	if cloned() {
		t.Fatal("cloned again over an existing clone")
	}
	// Git is set up as the token's user either way.
	want := []string{"git", "config", "--global", "user.email", "1+a@users.noreply.github.com"}
	found := false
	for _, c := range commands {
		found = found || reflect.DeepEqual(c, want)
	}
	if !found {
		t.Errorf("commands = %v", commands)
	}
}

func TestCloneNamesTheUserByLoginWithoutAName(t *testing.T) {
	useWorkspace(t, t.TempDir())
	var commands [][]string
	oldRun := runCommand
	runCommand = func(name string, args ...string) (string, error) {
		commands = append(commands, append([]string{name}, args...))
		return `{"name": null, "login": "bot", "id": 7}`, nil
	}
	t.Cleanup(func() { runCommand = oldRun })
	if err := clone("o/r"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"git", "config", "--global", "user.name", "bot"}; !reflect.DeepEqual(commands[1], want) {
		t.Errorf("command = %v", commands[1])
	}
}

func TestRunCommandFailureSaysWhatFailed(t *testing.T) {
	_, err := runCommand("sh", "-c", "echo oops >&2; exit 3")
	if err == nil || !strings.Contains(err.Error(), "failed: oops") {
		t.Errorf("error = %v", err)
	}
}

func git(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// gitClone makes a workspace that's a repository tracking tracked.txt.
func gitClone(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, "init", "-q", root)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", root, "add", ".")
	useWorkspace(t, root)
	return root
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInstallFilesCopiesAndExcludes(t *testing.T) {
	root := gitClone(t)
	staged := filepath.Join(t.TempDir(), "staged")
	write(t, staged, "KEY=1\n")
	if err := installFiles([]stagedFile{{staged, "config/.env"}}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "config/.env")
	if got := read(t, target); got != "KEY=1\n" {
		t.Errorf("content = %q", got)
	}
	if info, _ := os.Stat(target); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
	if !strings.Contains("\n"+read(t, filepath.Join(root, ".git/info/exclude")), "\n/config/.env\n") {
		t.Error("the file isn't excluded")
	}
	out, err := exec.Command("git", "-C", root, "status", "--porcelain", "-uall").Output()
	if err != nil || strings.Contains(string(out), "config") {
		t.Errorf("git status = %q, %v", out, err)
	}
}

func TestInstallFilesInstallsEachStagedFileOnce(t *testing.T) {
	root := gitClone(t)
	staged := filepath.Join(t.TempDir(), "staged")
	write(t, staged, "new")
	write(t, filepath.Join(root, ".env"), "from the last up")
	files := []stagedFile{{staged, ".env"}}
	if err := installFiles(files); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(root, ".env")); got != "new" {
		t.Errorf("content = %q", got)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Error("the staged file is still there")
	}
	write(t, filepath.Join(root, ".env"), "edited by agent")
	if err := installFiles(files); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(root, ".env")); got != "edited by agent" {
		t.Errorf("content = %q", got)
	}
	if n := strings.Count(read(t, filepath.Join(root, ".git/info/exclude")), "/.env"); n != 1 {
		t.Errorf("excluded %d times", n)
	}
}

func TestInstallFilesEscapesPatternSyntax(t *testing.T) {
	root := gitClone(t)
	if err := installFiles([]stagedFile{{"/nonexistent", "a*b[1]?.txt"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, filepath.Join(root, ".git/info/exclude")), "/a\\*b\\[1]\\?.txt\n") {
		t.Errorf("exclude = %q", read(t, filepath.Join(root, ".git/info/exclude")))
	}
}

func TestInstallFilesRejectsTrackedAndEscapingPaths(t *testing.T) {
	root := gitClone(t)
	staged := filepath.Join(t.TempDir(), "staged")
	write(t, staged, "x")
	err := installFiles([]stagedFile{{staged, "tracked.txt"}})
	if err == nil || !strings.Contains(err.Error(), "tracked") {
		t.Errorf("tracked: %v", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	err = installFiles([]stagedFile{{staged, "link/x"}})
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("escaping: %v", err)
	}
	err = installFiles([]stagedFile{{staged, "../x"}})
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("parent: %v", err)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Error("a rejected file was removed")
	}
}
