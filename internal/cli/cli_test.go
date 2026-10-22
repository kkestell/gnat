package cli

import (
	"bytes"
	"github.com/kkestell/gnat/internal/layout"
	"os"
	"reflect"
	"strings"
	"testing"
)

// runGnat runs gnat with the arguments and returns its status and output.
func runGnat(t *testing.T, args ...string) (status int, out, errs string) {
	t.Helper()
	var o, e bytes.Buffer
	savedOut, savedErr := stdout, stderr
	t.Cleanup(func() { stdout, stderr = savedOut, savedErr })
	status = run(args, &o, &e)
	return status, o.String(), e.String()
}

func TestUsageErrorsExit2(t *testing.T) {
	useSwarm(t)
	for _, args := range [][]string{
		{},
		{"bogus"},
		{"agent"},
		{"file", "nope"},
		{"create", "demo"},           // --repo is required
		{"create", "demo", "--repo"}, // a flag without its value
		{"send"},                     // a swarm is required
		{"up", "a", "b"},             // too many
		{"agent", "create", "demo", "a", "--instructions", "x", "--harness", "nope"},
		{"send", "demo", "--unknown"},
	} {
		if status, _, errs := runGnat(t, args...); status != 2 || !strings.Contains(errs, "error") {
			t.Errorf("%v: status %d, stderr %q", args, status, errs)
		}
	}
}

func TestFailuresExit1WithTheMessage(t *testing.T) {
	useSwarm(t)
	status, _, errs := runGnat(t, "up", "missing")
	if status != 1 || errs != "error: no swarm named 'missing'; create it with `gnat create missing`\n" {
		t.Errorf("status %d, stderr %q", status, errs)
	}
}

func TestACommandsOwnStatusIsPassedThrough(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"))
	useExec(t, listing([]string{"gnat-demo-control"}, nil, func(c command) (result, error) {
		return result{}, &exitError{Code: layout.TimeoutStatus}
	}))
	status, _, errs := runGnat(t, "wait", "demo", "--timeout", "5")
	if status != layout.TimeoutStatus || errs != "" {
		t.Errorf("status %d, stderr %q", status, errs)
	}
}

func TestWaitAnyPassesItOn(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"), agent("beta", "claude"))
	f := useExec(t, listing([]string{"gnat-demo-control"}, nil, nil))
	if status, _, errs := runGnat(t, "wait", "demo", "--any", "--timeout", "5"); status != 0 {
		t.Fatalf("status %d, stderr %q", status, errs)
	}
	want := "docker exec gnat-demo-control /opt/gnat/gnat-container control wait alpha beta --any --timeout 5.0"
	if last := f.lines()[len(f.lines())-1]; last != want {
		t.Errorf("got %q, want %q", last, want)
	}
}

func TestFlagsMayFollowPositionals(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"), agent("beta", "claude"))
	f := useExec(t, listing([]string{"gnat-demo-control"}, nil, nil))
	status, out, errs := runGnat(t, "send", "demo", "alpha", "beta", "-m", "hello world", "--now")
	if want := "message for alpha, beta will be delivered into each agent's running turn at its next tool call, or start its next turn if it's idle or the turn ends first\n"; status != 0 || out != want {
		t.Fatalf("status %d, stdout %q, stderr %q", status, out, errs)
	}
	want := "docker exec gnat-demo-control /opt/gnat/gnat-container control send hello world alpha beta --now"
	if last := f.lines()[len(f.lines())-1]; last != want {
		t.Errorf("got %q, want %q", last, want)
	}

	status, _, errs = runGnat(t, "send", "demo", "-m", "x", "--timeout", "5")
	if status != 1 || !strings.Contains(errs, "--timeout needs --wait") {
		t.Errorf("status %d, stderr %q", status, errs)
	}
	// -m "" is a message, though an empty one.
	status, _, errs = runGnat(t, "send", "demo", "-m", "")
	if status != 1 || !strings.Contains(errs, "message is empty") {
		t.Errorf("status %d, stderr %q", status, errs)
	}
}

func TestLogsTailsTheHostLogs(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"), agent("beta", "claude"))
	f := useExec(t, nil)
	if status, _, errs := runGnat(t, "logs", "demo", "beta", "-n", "20", "-f"); status != 0 {
		t.Fatalf("status %d: %s", status, errs)
	}
	c := f.calls[0]
	if c.name != "tail" || !reflect.DeepEqual(c.args[:3], []string{"-n", "20", "-f"}) ||
		len(c.args) != 4 || !strings.HasSuffix(c.args[3], "/logs/demo/beta.log") {
		t.Errorf("got %v", f.lines())
	}
	f.calls = nil
	runGnat(t, "logs", "demo")
	if got := f.calls[0].args; got[0] != "-n" || got[1] != "+1" || len(got) != 4 {
		t.Errorf("got %v", got)
	}
}

func TestHelpListsOnlyUserCommands(t *testing.T) {
	_, out, _ := runGnat(t, "--help")
	if strings.Contains(out, "internal") {
		t.Errorf("help lists internal:\n%s", out)
	}
	for _, want := range []string{"create", "agent", "send", "logs", "web"} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q", want)
		}
	}
}

func TestDownAndListReportWhatDockerHas(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"))
	f := useExec(t, func(c command) (result, error) {
		switch c.args[0] {
		case "ps":
			return result{stdout: "gnat-demo-control\ngnat-demo-alpha\n"}, nil
		case "network":
			if c.args[1] == "ls" {
				return result{stdout: "gnat-demo\n"}, nil
			}
		}
		return result{}, nil
	})
	status, out, errs := runGnat(t, "down", "demo")
	if status != 0 || out != "removed gnat-demo-control\nremoved gnat-demo-alpha\n" {
		t.Fatalf("status %d, stdout %q, stderr %q", status, out, errs)
	}
	lines := f.lines()
	if !contains(lines, "docker rm -f gnat-demo-control gnat-demo-alpha") || !contains(lines, "docker network rm gnat-demo") {
		t.Errorf("got %v", lines)
	}
	_, out, _ = runGnat(t, "list")
	if out != "demo\tup\t1 agents\to/r\n" {
		t.Errorf("got %q", out)
	}
}

func contains(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func TestDeleteRemovesTheSwarmItsVolumesAndImage(t *testing.T) {
	useSwarm(t)
	f := useExec(t, func(c command) (result, error) {
		if c.args[0] == "volume" {
			return result{stdout: "gnat-demo-alpha-workspace\n"}, nil
		}
		return result{}, nil
	})
	if status, out, errs := runGnat(t, "delete", "demo"); status != 0 || out != "deleted swarm demo\n" {
		t.Fatalf("status %d, stdout %q, stderr %q", status, out, errs)
	}
	if !contains(f.lines(), "docker volume rm gnat-demo-alpha-workspace") {
		t.Errorf("got %v", f.lines())
	}
	if status, _, _ := runGnat(t, "list"); status != 0 {
		t.Error("list failed")
	}
	if _, out, _ := runGnat(t, "list"); out != "" {
		t.Errorf("the swarm is still listed: %q", out)
	}
}

func TestThereIsNoHelpSubcommand(t *testing.T) {
	useSwarm(t)
	if status, _, errs := runGnat(t, "help", "create"); status != 2 || !strings.Contains(errs, "unknown command") {
		t.Errorf("status %d, stderr %q", status, errs)
	}
	status, out, _ := runGnat(t, "--help")
	if status != 0 || strings.Contains(out, "Help about any command") {
		t.Errorf("status %d, help:\n%s", status, out)
	}
	if status, out, _ := runGnat(t, "create", "-h"); status != 0 || !strings.Contains(out, "--repo") {
		t.Errorf("create -h: status %d:\n%s", status, out)
	}
}

func TestAnEmptyStdinIsAMessageNotATerminal(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	saved := os.Stdin
	os.Stdin = devnull
	t.Cleanup(func() { os.Stdin = saved })
	if stdinIsTerminal() {
		t.Fatal("/dev/null is a terminal")
	}
	savedReader := stdin
	stdin = devnull
	t.Cleanup(func() { stdin = savedReader })
	if _, err := readMessage(nil); err == nil || err.Error() != "message is empty" {
		t.Errorf("got %v", err)
	}
}
