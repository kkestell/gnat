package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kkestell/gnat/internal/protocol"
	"github.com/kkestell/gnat/internal/state"
)

// useWorkspace points the daemon at a workspace directory for the test.
func useWorkspace(t *testing.T, dir string) {
	t.Helper()
	old := workspacePath
	workspacePath = dir
	t.Cleanup(func() { workspacePath = old })
}

// useControl fakes the control plane, which answers a request for who to commit as; the
// requests it got are returned. Git is faked to record its commands.
func useControl(t *testing.T) (requests *[]protocol.Request, commands *[][]string) {
	t.Helper()
	requests, commands = &[]protocol.Request{}, &[][]string{}
	oldRun, oldRequest := runCommand, request
	runCommand = func(name string, args ...string) (string, error) {
		*commands = append(*commands, append([]string{name}, args...))
		return "", nil
	}
	request = func(req protocol.Request) (*protocol.Response, error) {
		*requests = append(*requests, req)
		r := protocol.OK()
		r.Identity = &protocol.GitIdentity{Name: "The User", Email: "1+a@users.noreply.github.com"}
		return r, nil
	}
	t.Cleanup(func() { runCommand, request = oldRun, oldRequest })
	return requests, commands
}

func TestCloneGoesThroughTheControlPlaneAsItsUser(t *testing.T) {
	useWorkspace(t, t.TempDir())
	t.Setenv(state.SecretVar, "the-secret")
	requests, commands := useControl(t)
	if err := clone("o/r"); err != nil {
		t.Fatal(err)
	}
	wantRequest := protocol.Request{Op: protocol.OpGitHub, Secret: "the-secret",
		GitHub: &protocol.GitHubRequest{Action: protocol.GitIdentityAction}}
	if len(*requests) != 1 || !reflect.DeepEqual((*requests)[0], wantRequest) {
		t.Errorf("requests = %+v", *requests)
	}
	want := [][]string{
		{"git", "config", "--global", "user.name", "The User"},
		{"git", "config", "--global", "user.email", "1+a@users.noreply.github.com"},
		{"git", "config", "--global", "credential.http://control:7001.helper", credentialHelper},
		{"git", "clone", "--quiet", "http://control:7001/o/r.git", workspacePath},
	}
	if !reflect.DeepEqual(*commands, want) {
		t.Errorf("commands = %v", *commands)
	}
	// The secret is read from the environment when git asks, never written down.
	if strings.Contains(credentialHelper, "the-secret") || !strings.Contains(credentialHelper, "$GNAT_SECRET") {
		t.Errorf("helper %q", credentialHelper)
	}
	for _, c := range *commands {
		if c[0] == "gh" || strings.Contains(strings.Join(c, " "), "github.com") && c[1] != "config" {
			t.Errorf("command %v", c)
		}
	}
}

func TestCloneRepointsAnExistingClone(t *testing.T) {
	dir := t.TempDir()
	useWorkspace(t, dir)
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, commands := useControl(t)
	if err := clone("o/r"); err != nil {
		t.Fatal(err)
	}
	for _, c := range *commands {
		if c[0] == "git" && c[1] == "clone" {
			t.Fatalf("cloned again over an existing clone: %v", c)
		}
	}
	last := (*commands)[len(*commands)-2:]
	want := [][]string{
		{"git", "-C", dir, "remote", "set-url", "origin", "http://control:7001/o/r.git"},
		{"git", "-C", dir, "config", "--unset-all", "remote.origin.pushurl"},
	}
	if !reflect.DeepEqual(last, want) {
		t.Errorf("last commands = %v", last)
	}
}

func TestCloneRepointsARealClone(t *testing.T) {
	dir := gitClone(t)
	useWorkspace(t, dir)
	git(t, "-C", dir, "remote", "add", "origin", "https://github.com/o/r.git")
	git(t, "-C", dir, "config", "remote.origin.pushurl", "https://github.com/o/r.git")
	oldRequest := request
	request = func(protocol.Request) (*protocol.Response, error) {
		r := protocol.OK()
		r.Identity = &protocol.GitIdentity{Name: "N", Email: "e@example.com"}
		return r, nil
	}
	t.Cleanup(func() { request = oldRequest })
	t.Setenv("HOME", t.TempDir())
	if err := clone("o/r"); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", dir, "config", "--get-all", "remote.origin.url").Output()
	if err != nil || strings.TrimSpace(string(out)) != "http://control:7001/o/r.git" {
		t.Errorf("url %q, %v", out, err)
	}
	if out, err := exec.Command("git", "-C", dir, "config", "--get-all", "remote.origin.pushurl").Output(); err == nil {
		t.Errorf("push url %q", out)
	}
}

func TestCloneWithoutAnOriginAddsOne(t *testing.T) {
	dir := gitClone(t)
	useWorkspace(t, dir)
	t.Setenv("HOME", t.TempDir())
	oldRequest := request
	request = func(protocol.Request) (*protocol.Response, error) {
		r := protocol.OK()
		r.Identity = &protocol.GitIdentity{Name: "N", Email: "e@example.com"}
		return r, nil
	}
	t.Cleanup(func() { request = oldRequest })
	if err := clone("o/r"); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil || strings.TrimSpace(string(out)) != "http://control:7001/o/r.git" {
		t.Errorf("url %q, %v", out, err)
	}
}

func TestCloneFailsWithoutAnIdentity(t *testing.T) {
	useWorkspace(t, t.TempDir())
	_, commands := useControl(t)
	request = func(protocol.Request) (*protocol.Response, error) { return protocol.OK(), nil }
	if err := clone("o/r"); err == nil || len(*commands) != 0 {
		t.Errorf("err %v, commands %v", err, *commands)
	}
	request = func(protocol.Request) (*protocol.Response, error) { return nil, errors.New("unauthorized") }
	if err := clone("o/r"); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("err %v", err)
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

// The helper, run by the real git, gives the secret to the control plane and to nobody else.
func TestCredentialHelperGivesTheSecretToTheControlPlaneOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv(state.SecretVar, "the-secret")
	git(t, "config", "--global", "credential."+strings.TrimSuffix(gitURL("o/r"), "/o/r.git")+".helper", credentialHelper)
	fill := func(input string) string {
		cmd := exec.Command("git", "credential", "fill")
		cmd.Stdin = strings.NewReader(input)
		out, _ := cmd.Output()
		return string(out)
	}
	if out := fill("protocol=http\nhost=control:7001\npath=o/r.git\n\n"); !strings.Contains(out, "password=the-secret\n") {
		t.Errorf("control: %q", out)
	}
	for _, other := range []string{
		"protocol=https\nhost=github.com\n\n", "protocol=http\nhost=control:7002\n\n", "protocol=http\nhost=evil\n\n",
		"protocol=http\nhost=control:7001.evil.example\n\n",
	} {
		if out := fill(other); strings.Contains(out, "the-secret") {
			t.Errorf("%q gave the secret: %q", other, out)
		}
	}
	data, err := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if err != nil || strings.Contains(string(data), "the-secret") {
		t.Errorf("config %q, %v", data, err)
	}
}
