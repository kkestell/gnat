package cli

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kkestell/gnat/internal/control"
	"github.com/kkestell/gnat/internal/layout"
	"github.com/kkestell/gnat/internal/state"
)

func names(agents []state.Agent) []string { return agentNames(agents) }

var demoSwarm = &state.Swarm{Name: "demo", Dockerfile: "Dockerfile", Repo: "o/r", Agents: []state.Agent{
	agent("alpha", "claude"), agent("beta", "claude"),
}}

func TestSelectAgentsDefaultsToEveryAgent(t *testing.T) {
	got, err := selectAgents(demoSwarm, nil)
	if err != nil || !reflect.DeepEqual(names(got), []string{"alpha", "beta"}) {
		t.Fatalf("got %v, %v", names(got), err)
	}
}

func TestSelectAgentsKeepsOrderAndDropsRepeats(t *testing.T) {
	got, err := selectAgents(demoSwarm, []string{"beta", "alpha", "beta"})
	if err != nil || !reflect.DeepEqual(names(got), []string{"beta", "alpha"}) {
		t.Fatalf("got %v, %v", names(got), err)
	}
}

func TestSelectAgentsRejectsUnknownNames(t *testing.T) {
	_, err := selectAgents(demoSwarm, []string{"gamma"})
	var stateErr *state.Error
	if !errors.As(err, &stateErr) {
		t.Fatalf("got %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func mkStatus(st string, queued, turns int, exit *int, stopped bool) agentStatus {
	return agentStatus{State: st, Queued: queued, Turns: turns, Exit: exit, Stopped: stopped}
}

func TestStatusLine(t *testing.T) {
	cases := []struct {
		s         agentStatus
		container string
		want      string
	}{
		{mkStatus("busy", 2, 3, ptr(0), false), "running", "alpha\tbusy\t2 queued\t3 turns"},
		{mkStatus("idle", 0, 1, ptr(1), false), "running", "alpha\tidle\t0 queued\t1 turns\tlast turn exited with status 1"},
		{mkStatus("idle", 0, 1, nil, false), "running", "alpha\tidle\t0 queued\t1 turns\tlast turn interrupted (restart or down)"},
		{mkStatus("idle", 0, 1, ptr(-9), true), "running", "alpha\tidle\t0 queued\t1 turns\tlast turn stopped"},
		{mkStatus("busy", 0, 0, nil, false), "restarting", "alpha\trestarting\t0 queued\t0 turns"},
		{mkStatus("busy", 0, 0, nil, false), "", "alpha\tmissing\t0 queued\t0 turns"},
	}
	for _, c := range cases {
		if got := statusLine("alpha", c.s, c.container); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

func TestStatusLineShowsHowLongABusyAgentHasBeenQuietAndItsUsage(t *testing.T) {
	usage := map[string]any{"input_tokens": 10.0, "cache_read_tokens": 1_500_000.0,
		"cache_write_tokens": 20_000.0, "output_tokens": 34_000.0, "cost": 12.345}
	busy := mkStatus("busy", 0, 1, ptr(0), false)
	busy.SinceActive, busy.Usage = ptr(192.5), usage
	want := "alpha\tbusy\t0 queued\t1 turns\tlast event 3m12s ago\t1.5M in (1.5M cached), 34k out, $12.35"
	if got := statusLine("alpha", busy, "running"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	idle := mkStatus("idle", 0, 1, ptr(0), false)
	idle.SinceActive, idle.Usage = ptr(5.0), map[string]any{"cost": 0.0421}
	if got, want := statusLine("alpha", idle, "running"), "alpha\tidle\t0 queued\t1 turns\t$0.0421"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDuration(t *testing.T) {
	var got []string
	for _, s := range []float64{0, 59.9, 60, 3599, 3600, 7322} {
		got = append(got, duration(s))
	}
	want := []string{"0s", "59s", "1m00s", "59m59s", "1h00m", "2h02m"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFormatFloatReadsAsPythonsDoes(t *testing.T) {
	for in, want := range map[float64]string{600: "600.0", 0.5: "0.5", 2.25: "2.25"} {
		if got := formatFloat(in); got != want {
			t.Errorf("%v: got %q, want %q", in, got, want)
		}
	}
}

func TestCpTakesAgentAndPath(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"))
	if err := copyOut("demo", "notes.txt", "."); err == nil || !strings.Contains(err.Error(), "must be AGENT:PATH") {
		t.Errorf("got %v", err)
	}
	if err := copyOut("demo", "gamma:notes.txt", "."); err == nil || !strings.Contains(err.Error(), "no agent 'gamma'") {
		t.Errorf("got %v", err)
	}
}

// answers is a fake docker's replies to listing commands, by the filter's kind.
func listing(running []string, volumeNames []string, extra func(c command) (result, error)) func(command) (result, error) {
	return func(c command) (result, error) {
		args := strings.Join(c.args, " ")
		switch {
		case strings.HasPrefix(args, "ps -a"):
			return result{stdout: strings.Join(running, "\n")}, nil
		case strings.HasPrefix(args, "volume ls"):
			return result{stdout: strings.Join(volumeNames, "\n")}, nil
		}
		if extra != nil {
			return extra(c)
		}
		return result{}, nil
	}
}

func TestCpCopiesFromARunningAgentRelativeToItsClone(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"))
	f := useExec(t, listing([]string{"gnat-demo-alpha"}, nil, nil))
	if err := copyOut("demo", "alpha:out/notes.txt", "here"); err != nil {
		t.Fatal(err)
	}
	if err := copyOut("demo", "alpha:/tmp/x", "-"); err != nil {
		t.Fatal(err)
	}
	var cps []string
	for _, l := range f.lines() {
		if strings.HasPrefix(l, "docker cp") {
			cps = append(cps, l)
		}
	}
	want := []string{"docker cp gnat-demo-alpha:/workspace/out/notes.txt here", "docker cp gnat-demo-alpha:/tmp/x -"}
	if !reflect.DeepEqual(cps, want) {
		t.Errorf("got %v, want %v", cps, want)
	}
}

func TestCpKeepsALastDotForTheDirectorysContents(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"))
	f := useExec(t, listing([]string{"gnat-demo-alpha"}, nil, nil))
	for _, source := range []string{"alpha:.", "alpha:./", "alpha:out/.", "alpha:out/./", "alpha:/workspace/.", "alpha:/.", "alpha:out/"} {
		if err := copyOut("demo", source, "here"); err != nil {
			t.Fatal(err)
		}
	}
	var cps []string
	for _, l := range f.lines() {
		if strings.HasPrefix(l, "docker cp") {
			cps = append(cps, strings.TrimPrefix(l, "docker cp gnat-demo-alpha:"))
		}
	}
	want := []string{"/workspace/. here", "/workspace/. here", "/workspace/out/. here", "/workspace/out/. here",
		"/workspace/. here", "/. here", "/workspace/out here"}
	if !reflect.DeepEqual(cps, want) {
		t.Errorf("got %v, want %v", cps, want)
	}

	// Down, the clone's contents are under its volume and copied from the temporary container.
	f = useExec(t, listing(nil, []string{"gnat-demo-alpha-workspace", "gnat-demo-alpha-claude"},
		func(c command) (result, error) {
			if c.args[0] == "create" {
				return result{stdout: "tmp1\n"}, nil
			}
			return result{}, nil
		}))
	if err := copyOut("demo", "alpha:.", "here"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.lines(), "docker cp tmp1:/workspace/. here") {
		t.Errorf("got %v", f.lines())
	}
}

func TestCpCopiesFromADownAgentsVolumes(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"))
	f := useExec(t, listing(nil, []string{"gnat-demo-alpha-workspace", "gnat-demo-alpha-claude"},
		func(c command) (result, error) {
			if c.args[0] == "create" {
				return result{stdout: "tmp1\n"}, nil
			}
			return result{}, nil
		}))
	if err := copyOut("demo", "alpha:notes.txt", "here"); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range f.lines() {
		if !strings.HasPrefix(l, "docker ps") && !strings.HasPrefix(l, "docker volume") {
			got = append(got, l)
		}
	}
	want := []string{
		"docker create -v gnat-demo-alpha-workspace:/workspace -v gnat-demo-alpha-claude:/home/agent/.claude " + agentImage,
		"docker cp tmp1:/workspace/notes.txt here",
		"docker rm tmp1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	err := copyOut("demo", "alpha:/tmp/x", "here")
	if err == nil || !strings.Contains(err.Error(), "is down, so only paths under /workspace") {
		t.Errorf("got %v", err)
	}
}

func TestCpFromADownAgentWithoutVolumes(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"))
	useExec(t, listing(nil, nil, nil))
	err := copyOut("demo", "alpha:notes.txt", "here")
	if err == nil || !strings.Contains(err.Error(), "has no clone yet") {
		t.Errorf("got %v", err)
	}
}

func TestCpRemovesTheTemporaryContainerWhenTheCopyFails(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"))
	f := useExec(t, listing(nil, []string{"gnat-demo-alpha-workspace", "gnat-demo-alpha-claude"},
		func(c command) (result, error) {
			switch c.args[0] {
			case "create":
				return result{stdout: "tmp1\n"}, nil
			case "cp":
				return result{}, &exitError{Code: 1}
			}
			return result{}, nil
		}))
	if err := copyOut("demo", "alpha:notes.txt", "here"); exitStatus(err) != 1 {
		t.Errorf("got %v", err)
	}
	if last := f.lines()[len(f.lines())-1]; last != "docker rm tmp1" {
		t.Errorf("last command %q", last)
	}
}

func TestCleanPosix(t *testing.T) {
	for in, want := range map[string]string{
		"/workspace/out//notes.txt/": "/workspace/out/notes.txt",
		"/workspace/./a":             "/workspace/a",
		"/workspace/../etc":          "/workspace/../etc",
	} {
		if got := cleanPosix(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestAgentConfigInstallsEachHarnessItsInstructionsAndSettings(t *testing.T) {
	swarm := &state.Swarm{Name: "demo", Repo: "o/r", Agents: []state.Agent{
		{Name: "alpha", Instructions: "Review.", Model: "opus", Harness: "claude"},
		{Name: "beta", Instructions: "Build.", Model: "openrouter:x/y", Effort: "high", Harness: "ox"},
	}}
	files := func(a state.Agent) map[string][]byte {
		data, err := agentConfig(swarm, a)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]byte{}
		tr := tar.NewReader(bytes.NewReader(data))
		for {
			h, err := tr.Next()
			if err == io.EOF {
				return out
			}
			if err != nil {
				t.Fatal(err)
			}
			if h.Typeflag == tar.TypeReg {
				out[h.Name], _ = io.ReadAll(tr)
			}
		}
	}

	claude := files(swarm.Agents[0])
	if len(claude) != 2 {
		t.Fatalf("claude files: %v", claude)
	}
	md := string(claude["home/agent/.claude/CLAUDE.md"])
	for _, want := range []string{"Review.\n", "beta", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if !strings.Contains(md, want) {
			t.Errorf("CLAUDE.md lacks %q", want)
		}
	}
	var settings map[string]any
	if err := json.Unmarshal(claude["home/agent/.claude/settings.json"], &settings); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(settings["attribution"], map[string]any{"commit": "", "pr": ""}) || settings["hooks"] == nil {
		t.Errorf("settings: %v", settings)
	}

	ox := files(swarm.Agents[1])
	if len(ox) != 2 {
		t.Fatalf("ox files: %v", ox)
	}
	md = string(ox["home/agent/.config/ox/AGENTS.md"])
	if !strings.HasPrefix(md, "Build.\n") || !strings.Contains(md, "OPENROUTER_API_KEY") {
		t.Errorf("AGENTS.md: %s", md)
	}
	settings = nil
	if err := json.Unmarshal(ox["home/agent/.config/ox/settings.json"], &settings); err != nil {
		t.Fatal(err)
	}
	if settings["mode"] != "auto" || settings["model"] != "openrouter:x/y" || settings["effort"] != "high" ||
		settings["hooks"] == nil {
		t.Errorf("settings: %v", settings)
	}
}

func TestAgentsAreCreatedWithTheirHarnessAndListed(t *testing.T) {
	useSwarm(t)
	useExec(t, nil) // no containers running
	out, _ := useOutput(t)
	if err := createAgent("demo", "alpha", "x", "claude", "", "max", false); err != nil {
		t.Fatal(err)
	}
	if err := createAgent("demo", "beta", "x", "ox", "", "minimal", false); err != nil {
		t.Fatal(err)
	}
	swarm, err := state.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	var got [][3]string
	for _, a := range swarm.Agents {
		got = append(got, [3]string{a.Name, a.Harness, a.Effort})
	}
	if want := [][3]string{{"alpha", "claude", "max"}, {"beta", "ox", "minimal"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	out.Reset()
	if err := listAgents("demo"); err != nil {
		t.Fatal(err)
	}
	if want := "alpha\tclaude\t-\tmax\t-\nbeta\tox\t-\tminimal\t-\n"; out.String() != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestAnEffortMustBeOneTheHarnessTakes(t *testing.T) {
	useSwarm(t)
	useExec(t, nil)
	useOutput(t)
	err := createAgent("demo", "alpha", "x", "claude", "", "minimal", false)
	if err == nil || !strings.Contains(err.Error(), "claude effort must be one of low, medium") {
		t.Errorf("got %v", err)
	}
	err = createAgent("demo", "alpha", "x", "ox", "", "extreme", false)
	if err == nil || !strings.Contains(err.Error(), "ox effort must be one of default, none") {
		t.Errorf("got %v", err)
	}
}

func TestCreateAgentRefusesReservedDuplicateAndInvalidNames(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"))
	useExec(t, nil)
	useOutput(t)
	for _, c := range []struct{ name, instructions, want string }{
		{"control", "x", "reserved for the control plane"},
		{"alpha", "x", "already has an agent 'alpha'"},
		{"-bad", "x", "agent name"},
	} {
		if err := createAgent("demo", c.name, c.instructions, "claude", "", "", false); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
}

func TestAgentsAndFilesChangeOnlyWhileTheSwarmIsDown(t *testing.T) {
	useSwarm(t)
	useExec(t, listing([]string{"gnat-demo-control"}, nil, nil))
	if err := createAgent("demo", "alpha", "x", "claude", "", "", false); err == nil || !strings.Contains(err.Error(), "is up; run `gnat down demo`") {
		t.Errorf("got %v", err)
	}
}

func TestSendValidatesBeforeTalkingToTheControlPlane(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"))
	f := useExec(t, nil)
	useOutput(t)
	empty := ""
	text := "hi"
	for _, c := range []struct {
		o    sendOptions
		want string
	}{
		{sendOptions{message: &empty}, "message is empty"},
		{sendOptions{message: &text, timeout: ptr(5.0)}, "--timeout needs --wait"},
		{sendOptions{message: &text, now: true, fresh: true}, "--now and --new conflict"},
	} {
		if err := send("demo", nil, c.o); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("got %v, want %q", err, c.want)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("docker ran: %v", f.lines())
	}
}

func TestSendReadsStdinWhenThereIsNoMessage(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"), agent("beta", "claude"))
	f := useExec(t, listing([]string{"gnat-demo-control"}, nil, nil))
	out, _ := useOutput(t)
	savedStdin, savedTerminal := stdin, stdinIsTerminal
	t.Cleanup(func() { stdin, stdinIsTerminal = savedStdin, savedTerminal })
	stdin, stdinIsTerminal = strings.NewReader("from a pipe\n"), func() bool { return false }

	if err := send("demo", []string{"beta"}, sendOptions{now: true}); err != nil {
		t.Fatal(err)
	}
	last := f.lines()[len(f.lines())-1]
	if want := "docker exec gnat-demo-control /opt/gnat/gnat-container control send from a pipe\n beta --now"; last != want {
		t.Errorf("got %q, want %q", last, want)
	}
	if out.String() != "queued message for beta\n" {
		t.Errorf("out: %q", out)
	}

	stdinIsTerminal = func() bool { return true }
	if err := send("demo", nil, sendOptions{}); err == nil || !strings.Contains(err.Error(), "no message") {
		t.Errorf("got %v", err)
	}
}

func TestSendWaitPassesTheControlPlanesStatusThrough(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"))
	f := useExec(t, listing([]string{"gnat-demo-control"}, nil, func(c command) (result, error) {
		return result{}, &exitError{Code: layout.TimeoutStatus}
	}))
	useOutput(t)
	text := "hi"
	err := send("demo", nil, sendOptions{message: &text, wait: true, timeout: ptr(600.0)})
	if exitStatus(err) != layout.TimeoutStatus {
		t.Fatalf("got %v", err)
	}
	last := f.lines()[len(f.lines())-1]
	if want := "docker exec gnat-demo-control /opt/gnat/gnat-container control send hi alpha --wait --timeout 600.0"; last != want {
		t.Errorf("got %q, want %q", last, want)
	}
}

func TestStatusMergesContainerStates(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"), agent("beta", "claude"))
	useExec(t, func(c command) (result, error) {
		args := strings.Join(c.args, " ")
		switch {
		case strings.Contains(args, "--format {{.Names}}\t{{.State}}"):
			return result{stdout: "gnat-demo-control\trunning\ngnat-demo-alpha\trunning\n"}, nil
		case strings.HasPrefix(args, "ps -a"):
			return result{stdout: "gnat-demo-control\ngnat-demo-alpha\n"}, nil
		case strings.Contains(args, "gnat-container control status"):
			return result{stdout: `{"agent": "alpha", "state": "idle", "queued": 1, "turns": 2, "exit": 0, "stopped": false}` + "\n" +
				`{"agent": "beta", "state": "busy", "queued": 0, "turns": 0, "exit": null, "stopped": false}` + "\n"}, nil
		}
		return result{}, nil
	})
	out, _ := useOutput(t)
	if err := status("demo", nil); err != nil {
		t.Fatal(err)
	}
	want := "alpha\tidle\t1 queued\t2 turns\nbeta\tmissing\t0 queued\t0 turns\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestStatusOfADownSwarmReadsWhatTheControlPlaneRecorded(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"), agent("beta", "claude"))
	dir, err := state.LogsDirectory("demo")
	if err != nil {
		t.Fatal(err)
	}
	db, err := control.Prepare(dir, []string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"INSERT INTO statuses (agent, turns, exit) VALUES ('alpha', 3, 0)",
		"INSERT INTO usage (agent, input_tokens, output_tokens, cost) VALUES ('alpha', 1200, 340, 1.23)",
		"INSERT INTO messages (agent, urgent, text, new) VALUES ('alpha', 0, 'next', 0)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	exec := useExec(t, nil)
	out, _ := useOutput(t)
	if err := status("demo", nil); err != nil {
		t.Fatal(err)
	}
	want := "alpha\tdown\t1 queued\t3 turns\t1.2k in, 340 out, $1.23\nbeta\tdown\t0 queued\t0 turns\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out, want)
	}
	for _, line := range exec.lines() {
		if strings.HasPrefix(line, "docker exec") {
			t.Errorf("ran %s", line)
		}
	}
}

func TestAnAgentDeletedAndCreatedAgainWhileDownStartsFresh(t *testing.T) {
	useSwarm(t, agent("beta", "claude"))
	exec := useExec(t, nil) // down, with no volumes
	out, _ := useOutput(t)
	if err := createAgent("demo", "alpha", "x", "claude", "", "", false); err != nil {
		t.Fatal(err)
	}
	swarm, _ := state.Load("demo")
	old, _ := swarm.Agent("alpha")
	// The swarm was up, and alpha worked and wrote to its log.
	dir, _ := state.LogsDirectory("demo")
	db, err := control.Prepare(dir, []string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.New([]string{"alpha", "beta"}, db, map[string]state.Agent{"alpha": old}, dir); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"UPDATE statuses SET turns = 3, exit = 1 WHERE agent = 'alpha'",
		"UPDATE statuses SET turns = 2, exit = 0 WHERE agent = 'beta'",
		"INSERT INTO messages (agent, urgent, text, new) VALUES ('alpha', 0, 'next', 0)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	log := filepath.Join(dir, "alpha.log")
	if err := writeFile(log, "the old alpha's work\n"); err != nil {
		t.Fatal(err)
	}

	if err := deleteAgent("demo", "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := createAgent("demo", "alpha", "x", "claude", "", "", false); err != nil {
		t.Fatal(err)
	}
	swarm, _ = state.Load("demo")
	if a, _ := swarm.Agent("alpha"); a.ID == "" || a.ID == old.ID {
		t.Fatalf("the new alpha's ID is %q, the old one's %q", a.ID, old.ID)
	}
	out.Reset()
	if err := status("demo", nil); err != nil {
		t.Fatal(err)
	}
	if want := "beta\tdown\t0 queued\t2 turns\nalpha\tdown\t0 queued\t0 turns\n"; out.String() != want {
		t.Errorf("got %q, want %q", out, want)
	}
	exec.calls = nil
	if err := logs("demo", []string{"alpha"}, "+1", false); err != nil {
		t.Fatal(err)
	}
	if got := exec.calls[0].args; got[len(got)-1] != log {
		t.Fatalf("tailed %v", got)
	}
	if data, err := os.ReadFile(log); err != nil || len(data) != 0 {
		t.Errorf("alpha's log is %q, %v", data, err)
	}

	// A gnat from before IDs left the log of an agent it deleted.
	gamma := filepath.Join(dir, "gamma.log")
	if err := writeFile(gamma, "the old gamma's work\n"); err != nil {
		t.Fatal(err)
	}
	if err := createAgent("demo", "gamma", "x", "claude", "", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(gamma); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("gamma's old log is still there: %v", err)
	}
}

func TestSwarmFiles(t *testing.T) {
	useSwarm(t)
	useExec(t, nil)
	out, _ := useOutput(t)
	dir := t.TempDir()
	file := dir + "/.env"
	if err := writeFile(file, "K=1\n"); err != nil {
		t.Fatal(err)
	}
	if err := addSwarmFile("demo", file+":config/.env"); err != nil {
		t.Fatal(err)
	}
	if err := addSwarmFile("demo", file+":config/.env"); err == nil || !strings.Contains(err.Error(), "two files go to config/.env") {
		t.Errorf("got %v", err)
	}
	if err := addSwarmFile("demo", dir+"/missing"); err == nil || !strings.Contains(err.Error(), "file not found") {
		t.Errorf("got %v", err)
	}
	out.Reset()
	if err := listFiles("demo"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "config/.env\t") {
		t.Errorf("got %q", out)
	}
	if err := deleteSwarmFile("demo", "other"); err == nil || !strings.Contains(err.Error(), "has no file at other") {
		t.Errorf("got %v", err)
	}
	if err := deleteSwarmFile("demo", "./config//.env"); err != nil {
		t.Fatal(err)
	}
	if swarm, _ := state.Load("demo"); len(swarm.Files) != 0 {
		t.Errorf("files left: %v", swarm.Files)
	}
}

func TestCreateDefinesASwarm(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	out, _ := useOutput(t)
	dir := t.TempDir()
	dockerfile := dir + "/Dockerfile"
	if err := writeFile(dockerfile, "FROM gnat-agent\n"); err != nil {
		t.Fatal(err)
	}
	if err := create("demo", dockerfile, "o/r", nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != "created swarm demo\n" {
		t.Errorf("got %q", out)
	}
	swarm, err := state.Load("demo")
	if err != nil || swarm.Repo != "o/r" || swarm.Dockerfile == "" {
		t.Fatalf("got %+v, %v", swarm, err)
	}
	if err := create("demo", "", "o/r", nil); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("got %v", err)
	}
	if err := create("other", dir+"/nope", "o/r", nil); err == nil || !strings.Contains(err.Error(), "dockerfile not found") {
		t.Errorf("got %v", err)
	}
}

func TestSetAgentChangesModelAndEffortKeepingItsCloneAndHistory(t *testing.T) {
	alpha := state.Agent{Name: "alpha", Instructions: "x", Harness: "claude", Effort: "high", Merge: true}
	useSwarm(t, alpha, agent("beta", "ox"))
	logs, err := state.LogsDirectory("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(logs, "alpha.log"), "history\n"); err != nil {
		t.Fatal(err)
	}
	f := useExec(t, listing(nil, []string{"gnat-demo-alpha-workspace", "gnat-demo-alpha-claude"}, nil))
	status, out, errs := runGnat(t, "agent", "set", "demo", "alpha", "--model", "opus", "--effort", "max")
	if status != 0 {
		t.Fatalf("status %d: %s", status, errs)
	}
	if want := "changed agent alpha in swarm demo: model - -> opus, effort high -> max\n"; out != want {
		t.Errorf("got %q, want %q", out, want)
	}
	swarm, err := state.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	alpha.Model, alpha.Effort = "opus", "max"
	if want := []state.Agent{alpha, agent("beta", "ox")}; !reflect.DeepEqual(swarm.Agents, want) {
		t.Errorf("got %+v, want %+v", swarm.Agents, want)
	}
	for _, l := range f.lines() {
		if strings.HasPrefix(l, "docker volume rm") {
			t.Errorf("removed volumes: %s", l)
		}
	}
	if b, err := os.ReadFile(filepath.Join(logs, "alpha.log")); err != nil || string(b) != "history\n" {
		t.Errorf("log: %q, %v", b, err)
	}

	// Settings no flag gives keep their values; flags given that change nothing say so.
	if status, out, _ := runGnat(t, "agent", "set", "demo", "alpha", "--model", "opus"); status != 0 ||
		out != "agent alpha in swarm demo is unchanged\n" {
		t.Errorf("status %d: %q", status, out)
	}
	if status, out, _ := runGnat(t, "agent", "set", "demo", "alpha", "--no-merge", "--instructions", "y"); status != 0 ||
		out != "changed agent alpha in swarm demo: instructions changed, merge merge -> -\n" {
		t.Errorf("status %d: %q", status, out)
	}
}

func TestSetAgentTakesSettingsFromAProfile(t *testing.T) {
	useSwarm(t, state.Agent{Name: "alpha", Instructions: "x", Harness: "claude", Model: "sonnet", Effort: "high"})
	useExec(t, nil)
	useProfiles(t, map[string]string{"p.yaml": "instructions: from the profile\nmodel: opus\nmerge: true\n"})
	if status, _, errs := runGnat(t, "agent", "set", "demo", "alpha", "--profile", "p", "--effort", "low"); status != 0 {
		t.Fatalf("status %d: %s", status, errs)
	}
	swarm, _ := state.Load("demo")
	want := state.Agent{Name: "alpha", Instructions: "from the profile", Harness: "claude", Model: "opus", Effort: "low", Merge: true}
	if swarm.Agents[0] != want {
		t.Errorf("got %+v, want %+v", swarm.Agents[0], want)
	}
}

func TestSetAgentIsRefusedWhileTheSwarmIsUp(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"))
	useExec(t, listing([]string{"gnat-demo-control"}, nil, nil))
	status, _, errs := runGnat(t, "agent", "set", "demo", "alpha", "--model", "opus")
	if status != 1 || !strings.Contains(errs, "is up; run `gnat down demo`") {
		t.Errorf("status %d: %q", status, errs)
	}
	if swarm, _ := state.Load("demo"); swarm.Agents[0] != agent("alpha", "claude") {
		t.Errorf("changed to %+v", swarm.Agents[0])
	}
}

func TestSetAgentsHarnessDeletesItsSessionsSoItStartsAFreshOne(t *testing.T) {
	useSwarm(t, state.Agent{Name: "alpha", Instructions: "x", Harness: "claude", Effort: "high"})
	// The ox sessions are left from when it was on ox before; they're as stale.
	f := useExec(t, listing(nil, []string{"gnat-demo-alpha-workspace", "gnat-demo-alpha-claude",
		"gnat-demo-alpha-local-share-ox"}, nil))
	status, out, errs := runGnat(t, "agent", "set", "demo", "alpha", "--harness", "ox")
	if status != 0 {
		t.Fatalf("status %d: %s", status, errs)
	}
	if want := "changed agent alpha in swarm demo: harness claude -> ox, sessions deleted; its next turn starts a fresh one\n"; out != want {
		t.Errorf("got %q, want %q", out, want)
	}
	var removed []string
	for _, l := range f.lines() {
		if strings.HasPrefix(l, "docker volume rm") {
			removed = append(removed, l)
		}
	}
	if want := []string{"docker volume rm gnat-demo-alpha-claude gnat-demo-alpha-local-share-ox"}; !reflect.DeepEqual(removed, want) {
		t.Errorf("got %v, want %v", removed, want)
	}
	if swarm, _ := state.Load("demo"); swarm.Agents[0].Harness != "ox" || swarm.Agents[0].Effort != "high" {
		t.Errorf("got %+v", swarm.Agents[0])
	}

	// Until `gnat up` makes the new harness's, cp mounts the clone alone.
	f = useExec(t, listing(nil, []string{"gnat-demo-alpha-workspace"}, func(c command) (result, error) {
		if c.args[0] == "create" {
			return result{stdout: "tmp1\n"}, nil
		}
		return result{}, nil
	}))
	if err := copyOut("demo", "alpha:notes.txt", "here"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.lines(), "docker create -v gnat-demo-alpha-workspace:/workspace "+agentImage) {
		t.Errorf("got %v", f.lines())
	}
}

func TestSetAgentRefusesInvalidSettings(t *testing.T) {
	ox := state.Agent{Name: "alpha", Instructions: "x", Harness: "ox", Effort: "minimal"}
	for _, tc := range []struct {
		args   []string
		status int
		want   string
	}{
		{[]string{"--merge", "--no-merge"}, 2, "none of the others can be"},
		{[]string{"--harness", "nope"}, 2, `invalid --harness "nope"`},
		{[]string{"--effort", "extreme"}, 1, "ox effort must be one of"},
		// Its effort stays, and claude doesn't take it.
		{[]string{"--harness", "claude"}, 1, "claude effort must be one of"},
		{[]string{"--profile", "missing"}, 1, "missing"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			useSwarm(t, ox)
			f := useExec(t, nil)
			useProfiles(t, nil)
			status, _, errs := runGnat(t, append([]string{"agent", "set", "demo", "alpha"}, tc.args...)...)
			if status != tc.status || !strings.Contains(errs, tc.want) {
				t.Errorf("status %d: %q, want %d and %q", status, errs, tc.status, tc.want)
			}
			if swarm, _ := state.Load("demo"); swarm.Agents[0] != ox {
				t.Errorf("changed to %+v", swarm.Agents[0])
			}
			if len(f.calls) > 0 {
				t.Errorf("ran %v", f.lines())
			}
		})
	}
	useSwarm(t, ox)
	useExec(t, nil)
	if status, _, errs := runGnat(t, "agent", "set", "demo", "beta", "--model", "m"); status != 1 || !strings.Contains(errs, "beta") {
		t.Errorf("status %d: %q", status, errs)
	}
}

func TestSetAgentKeepsWhatTheProfileLeavesOut(t *testing.T) {
	ox := state.Agent{Name: "alpha", Instructions: "x", Harness: "ox", Model: "m", Effort: "minimal", Merge: true}
	useSwarm(t, ox)
	f := useExec(t, listing(nil, []string{"gnat-demo-alpha-workspace", "gnat-demo-alpha-local-share-ox"}, nil))
	useProfiles(t, map[string]string{"rev.yaml": "instructions: review\n"})
	status, out, errs := runGnat(t, "agent", "set", "demo", "alpha", "--profile", "rev")
	if status != 0 {
		t.Fatalf("status %d: %s", status, errs)
	}
	if want := "changed agent alpha in swarm demo: instructions changed\n"; out != want {
		t.Errorf("got %q, want %q", out, want)
	}
	ox.Instructions = "review"
	if swarm, _ := state.Load("demo"); swarm.Agents[0] != ox {
		t.Errorf("got %+v, want %+v", swarm.Agents[0], ox)
	}
	for _, l := range f.lines() {
		if strings.HasPrefix(l, "docker volume rm") {
			t.Errorf("removed volumes: %s", l)
		}
	}
}

func TestSetAgentMovesAnAgentOffAHarnessGnatNoLongerHas(t *testing.T) {
	useSwarm(t, state.Agent{Name: "alpha", Instructions: "x", Harness: "gone"})
	f := useExec(t, listing(nil, []string{"gnat-demo-alpha-workspace", "gnat-demo-alpha-gone"}, nil))
	if status, _, errs := runGnat(t, "agent", "set", "demo", "alpha", "--harness", "claude"); status != 0 {
		t.Fatalf("status %d: %s", status, errs)
	}
	if !slices.Contains(f.lines(), "docker volume rm gnat-demo-alpha-gone") {
		t.Errorf("got %v", f.lines())
	}
	if swarm, _ := state.Load("demo"); swarm.Agents[0].Harness != "claude" {
		t.Errorf("got %+v", swarm.Agents[0])
	}
}

func TestSetAgentKeepsItsID(t *testing.T) {
	// One from before IDs must not get one, or the control plane would take it for a new
	// agent and forget its history.
	for _, id := range []string{"abc123", ""} {
		useSwarm(t, state.Agent{Name: "alpha", ID: id, Instructions: "x", Harness: "claude"})
		useExec(t, nil)
		useProfiles(t, map[string]string{"p.yaml": "instructions: y\n"})
		if status, _, errs := runGnat(t, "agent", "set", "demo", "alpha", "--profile", "p", "--harness", "ox", "--model", "m"); status != 0 {
			t.Fatalf("status %d: %s", status, errs)
		}
		swarm, err := state.Load("demo")
		if err != nil {
			t.Fatal(err)
		}
		want := state.Agent{Name: "alpha", ID: id, Instructions: "y", Harness: "ox", Model: "m"}
		if swarm.Agents[0] != want {
			t.Errorf("got %+v, want %+v", swarm.Agents[0], want)
		}
	}
}
