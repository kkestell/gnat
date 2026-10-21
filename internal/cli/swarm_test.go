package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/harness"
	"github.com/kkestell/gnat/internal/state"
)

func TestAgentImageIsTheBaseUnlessTheSwarmHasADockerfile(t *testing.T) {
	if got := agentImageFor(&state.Swarm{Name: "demo", Repo: "o/r"}); got != agentImage {
		t.Errorf("got %s", got)
	}
	if got := agentImageFor(demoSwarm); got != "gnat-demo:latest" {
		t.Errorf("got %s", got)
	}
}

func TestBaseDockerfileShipsWithThePackage(t *testing.T) {
	text := string(agentDockerfile)
	// Built from stdin, with no context: an ox-server build stage, then the image itself.
	if got := strings.Count(text, "\nFROM ") + btoi(strings.HasPrefix(text, "FROM ")); got != 2 {
		t.Errorf("%d FROM lines", got)
	}
	for _, needed := range []string{"ripgrep", "build-essential", "uv", "claude.ai/install.sh", "--uid 1000",
		"procps", "jq", "fd-find", "python3",
		"ADD https://github.com/kkestell/ox.git#main", "COPY --from=ox /out/ox-server"} {
		if !strings.Contains(text, needed) {
			t.Errorf("missing %q", needed)
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestMissingCommands(t *testing.T) {
	f := useExec(t, func(command) (result, error) { return result{stdout: "gh\ngit\n"}, nil })
	got, err := missingCommands("img", []string{"git", "gh", "ox-server"})
	if err != nil || !reflect.DeepEqual(got, []string{"gh", "git"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	args := f.calls[0].args
	value := func(flag string) string {
		for i, a := range args {
			if a == flag {
				return args[i+1]
			}
		}
		return ""
	}
	if value("--user") != "1000" || value("--workdir") != "/workspace" {
		t.Errorf("args %v", args)
	}
	if tail := args[len(args)-3:]; !reflect.DeepEqual(tail, []string{"git", "gh", "ox-server"}) {
		t.Errorf("tail %v", tail)
	}
}

func TestMissingCommandsReportsAFailedCheck(t *testing.T) {
	useExec(t, func(command) (result, error) { return result{stderr: "no sh\n"}, &exitError{Code: 126} })
	_, err := missingCommands("img", []string{"git"})
	if err == nil || !strings.Contains(err.Error(), "no sh") {
		t.Errorf("got %v", err)
	}
}

func TestAgentVolumesHoldTheCloneAndTheHarnessSessions(t *testing.T) {
	got, err := agentVolumes("demo", agent("alpha", "claude"))
	want := []mount{{"/workspace", "gnat-demo-alpha-workspace"}, {"/home/agent/.claude", "gnat-demo-alpha-claude"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, %v", got, err)
	}
	got, err = agentVolumes("demo", agent("beta", "ox"))
	want = []mount{
		{"/workspace", "gnat-demo-beta-workspace"},
		{"/home/agent/.local/share/ox", "gnat-demo-beta-local-share-ox"},
		{"/home/agent/.local/state/gnat", "gnat-demo-beta-local-state-gnat"},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestUsedHarnessesAreEachHarnessOnceInOrder(t *testing.T) {
	swarm := &state.Swarm{Name: "demo", Repo: "o/r", Agents: []state.Agent{
		agent("a", "ox"), agent("b", "claude"), agent("c", "ox")}}
	used, err := usedHarnesses(swarm)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range used {
		got = append(got, h.Name())
	}
	if !reflect.DeepEqual(got, []string{"ox", "claude"}) {
		t.Errorf("got %v", got)
	}
}

func TestWaitUntilReadyRetriesUntilTheControlPlaneAnswers(t *testing.T) {
	failures := 2
	f := useExec(t, func(command) (result, error) {
		if failures > 0 {
			failures--
			return result{stderr: "refused"}, &exitError{Code: 1}
		}
		return result{}, nil
	})
	savedSleep := sleep
	sleep = func(time.Duration) {}
	t.Cleanup(func() { sleep = savedSleep })
	if err := waitUntilReady("gnat-demo-control", "alpha"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 3 {
		t.Errorf("%d calls", len(f.calls))
	}
	want := "docker exec gnat-demo-control /opt/gnat/gnat internal control status alpha"
	if f.lines()[0] != want {
		t.Errorf("got %q, want %q", f.lines()[0], want)
	}
}

func TestWaitUntilReadyGivesUpWithTheLastError(t *testing.T) {
	useExec(t, func(command) (result, error) { return result{stderr: "refused"}, &exitError{Code: 1} })
	clock := time.Unix(0, 0)
	savedNow, savedSleep := now, sleep
	now = func() time.Time { clock = clock.Add(10 * time.Second); return clock }
	sleep = func(time.Duration) {}
	t.Cleanup(func() { now, sleep = savedNow, savedSleep })
	err := waitUntilReady("gnat-demo-control", "alpha")
	if err == nil || !strings.Contains(err.Error(), "didn't answer within 30 seconds: refused") {
		t.Errorf("got %v", err)
	}
}

func TestLoadCredentialNamesHowToGetOne(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("OPENROUTER_API_KEY", "")
	ox, _ := harness.Get("ox")
	_, err := loadCredential(ox)
	want := "OPENROUTER_API_KEY is not set; create an OpenRouter API key and put it in " + dir + "/gnat/.env"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("got %v, want %q", err, want)
	}
	t.Setenv("OPENROUTER_API_KEY", "k")
	if got, err := loadCredential(ox); err != nil || got != "k" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestCredentialsComeFromGnatsOwnEnvFileNotTheCurrentDirectorys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("OPENROUTER_API_KEY", "")
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(".env", "OPENROUTER_API_KEY=project\n"); err != nil {
		t.Fatal(err)
	}
	ox, _ := harness.Get("ox")
	if _, err := loadCredential(ox); err == nil || !strings.Contains(err.Error(), "is not set") {
		t.Errorf("got %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "config", "gnat"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(dir, "config", "gnat", ".env"), "OPENROUTER_API_KEY=gnat\n"); err != nil {
		t.Fatal(err)
	}
	if got, err := loadCredential(ox); err != nil || got != "gnat" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestDotenv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	text := `# comment
A=plain
export B = spaced  
C="double # not a comment\n\"quoted\""
D='single \n literal'
E=unquoted # a comment
F=
G=a#b
NOVALUE
`
	if err := writeFile(path, text); err != nil {
		t.Fatal(err)
	}
	got, err := dotenv(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"A": "plain", "B": "spaced", "C": "double # not a comment\n\"quoted\"",
		"D": `single \n literal`, "E": "unquoted", "F": "", "G": "a#b",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got, err := dotenv(path + ".missing"); err != nil || len(got) != 0 {
		t.Errorf("a missing file: %v, %v", got, err)
	}
}

func TestDockerArch(t *testing.T) {
	for in, want := range map[string]string{"x86_64\n": "amd64", "aarch64\n": "arm64", "arm64": "arm64"} {
		useExec(t, func(command) (result, error) { return result{stdout: in}, nil })
		if got, err := dockerArch(); err != nil || got != want {
			t.Errorf("%q: got %q, %v", in, got, err)
		}
	}
	useExec(t, func(command) (result, error) { return result{stdout: "s390x\n"}, nil })
	if _, err := dockerArch(); err == nil || !strings.Contains(err.Error(), "s390x") {
		t.Errorf("got %v", err)
	}
}

func TestExitStatus(t *testing.T) {
	if exitStatus(nil) != 0 || exitStatus(errors.New("x")) != 1 || exitStatus(&exitError{Code: 124}) != 124 {
		t.Error("wrong statuses")
	}
}
