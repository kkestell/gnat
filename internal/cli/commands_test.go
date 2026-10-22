package cli

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
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
	if err := createAgent("demo", "alpha", "x", "claude", "", "max"); err != nil {
		t.Fatal(err)
	}
	if err := createAgent("demo", "beta", "x", "ox", "", "minimal"); err != nil {
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
	if want := "alpha\tclaude\t-\tmax\nbeta\tox\t-\tminimal\n"; out.String() != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestAnEffortMustBeOneTheHarnessTakes(t *testing.T) {
	useSwarm(t)
	useExec(t, nil)
	useOutput(t)
	err := createAgent("demo", "alpha", "x", "claude", "", "minimal")
	if err == nil || !strings.Contains(err.Error(), "claude effort must be one of low, medium") {
		t.Errorf("got %v", err)
	}
	err = createAgent("demo", "alpha", "x", "ox", "", "extreme")
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
		if err := createAgent("demo", c.name, c.instructions, "claude", "", ""); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
}

func TestAgentsAndFilesChangeOnlyWhileTheSwarmIsDown(t *testing.T) {
	useSwarm(t)
	useExec(t, listing([]string{"gnat-demo-control"}, nil, nil))
	if err := createAgent("demo", "alpha", "x", "claude", "", ""); err == nil || !strings.Contains(err.Error(), "is up; run `gnat down demo`") {
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
