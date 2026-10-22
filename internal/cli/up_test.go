package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/kkestell/gnat/internal/state"
)

// useUp sets up what `up` needs besides docker: credentials, a binary to embed, and a
// docker that answers like a working one.
func useUp(t *testing.T, respond func(c command) (result, error)) *fakeExec {
	t.Helper()
	useSecrets(t, map[string]string{"claude": "claude-secret", "ox": "ox-secret"})
	t.Setenv("GH_TOKEN", "gh-secret")
	// Secrets that read as nothing but the order they were made in.
	n := 0
	savedSecret := newSecret
	newSecret = func() (string, error) { n++; return "pw-" + strconv.Itoa(n), nil }
	t.Cleanup(func() { newSecret = savedSecret })
	saved := binaryFor
	binaryFor = func(arch string) ([]byte, error) { return []byte("ELF-" + arch), nil }
	t.Cleanup(func() { binaryFor = saved })
	return useExec(t, func(c command) (result, error) {
		args := strings.Join(c.args, " ")
		switch {
		case c.name == "docker" && args == "info --format {{.Architecture}}":
			return result{stdout: "aarch64\n"}, nil
		case c.name == "docker" && c.args[0] == "port":
			return result{stdout: "127.0.0.1:49153\n"}, nil
		}
		if respond != nil {
			return respond(c)
		}
		return result{}, nil
	})
}

func TestUpBuildsImagesAndStartsTheControlPlaneThenEachAgent(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"), state.Agent{Name: "beta", ID: "b1", Instructions: "x", Harness: "ox", Model: "m", Effort: "high", Merge: true})
	host := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(host, []byte("K=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	swarm, _ := state.Load("demo")
	swarm.Files = []state.File{{Host: host, Path: "config/.env"}}
	if err := state.Save(swarm); err != nil {
		t.Fatal(err)
	}
	f := useUp(t, func(c command) (result, error) {
		if c.name == "docker" && c.args[0] == "ps" {
			return result{}, nil // down
		}
		return result{}, nil
	})
	out, _ := useOutput(t)
	if err := up("demo"); err != nil {
		t.Fatal(err)
	}
	if want := "started gnat-demo-control\nstarted gnat-demo-alpha\nstarted gnat-demo-beta\nweb UI at http://127.0.0.1:49153\n"; out.String() != want {
		t.Errorf("got %q, want %q", out, want)
	}

	var docker []string
	for _, l := range f.lines() {
		if strings.HasPrefix(l, "docker ") && !strings.HasPrefix(l, "docker ps") {
			docker = append(docker, strings.TrimPrefix(l, "docker "))
		}
	}
	logs, _ := state.LogsDirectory("demo")
	want := []string{
		"info --format {{.Architecture}}",
		"build -q -t gnat-control:latest -",
		"build -q -t gnat-agent:latest -",
		"network create --label gnat.name=demo gnat-demo",
		"create --name gnat-demo-control --label gnat.name=demo --user " + strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()) +
			" -v " + logs + ":/var/log/gnat --network gnat-demo --network-alias control -p 127.0.0.1::8080 -e GH_TOKEN -e GNAT_SECRETS gnat-control:latest" +
			` /opt/gnat/gnat-container control serve alpha beta --setups {"alpha":{"harness":"claude","model":null,"effort":null,"merge":false},` +
			`"beta":{"id":"b1","harness":"ox","model":"m","effort":"high","merge":true}} --swarm demo --repo o/r`,
		"cp -a - gnat-demo-control:/",
		"start gnat-demo-control",
		"exec gnat-demo-control /opt/gnat/gnat-container control status alpha",
		"volume create --label gnat.name=demo --label gnat.agent=alpha gnat-demo-alpha-workspace",
		"volume create --label gnat.name=demo --label gnat.agent=alpha gnat-demo-alpha-claude",
		"create --name gnat-demo-alpha --label gnat.name=demo --restart on-failure --user 1000 --workdir /workspace " +
			"--network gnat-demo --network-alias alpha -v gnat-demo-alpha-workspace:/workspace " +
			"-v gnat-demo-alpha-claude:/home/agent/.claude -e CLAUDE_CODE_OAUTH_TOKEN -e GNAT_SECRET " +
			"--entrypoint /opt/gnat/gnat-container gnat-agent:latest agent alpha --repo o/r --harness claude " +
			"--file /opt/gnat-files/0:config/.env",
		"cp -a - gnat-demo-alpha:/",
		"cp -a - gnat-demo-alpha:/",
		"cp -a - gnat-demo-alpha:/",
		"start gnat-demo-alpha",
		"volume create --label gnat.name=demo --label gnat.agent=beta gnat-demo-beta-workspace",
		"volume create --label gnat.name=demo --label gnat.agent=beta gnat-demo-beta-local-share-ox",
		"volume create --label gnat.name=demo --label gnat.agent=beta gnat-demo-beta-local-state-gnat",
		"create --name gnat-demo-beta --label gnat.name=demo --restart on-failure --user 1000 --workdir /workspace " +
			"--network gnat-demo --network-alias beta -v gnat-demo-beta-workspace:/workspace " +
			"-v gnat-demo-beta-local-share-ox:/home/agent/.local/share/ox " +
			"-v gnat-demo-beta-local-state-gnat:/home/agent/.local/state/gnat -e OPENROUTER_API_KEY -e GNAT_SECRET " +
			"--entrypoint /opt/gnat/gnat-container gnat-agent:latest agent beta --repo o/r --harness ox " +
			"--file /opt/gnat-files/0:config/.env --model m --effort high",
		"cp -a - gnat-demo-beta:/",
		"cp -a - gnat-demo-beta:/",
		"cp -a - gnat-demo-beta:/",
		"start gnat-demo-beta",
		"port gnat-demo-control 8080",
	}
	if strings.Join(docker, "\n") != strings.Join(want, "\n") {
		t.Errorf("docker commands:\n%s\nwant:\n%s", strings.Join(docker, "\n"), strings.Join(want, "\n"))
	}

	// Each agent gets only its own harness's credential, by name; the values stay out of argv.
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c.args, " "), "secret") {
			t.Errorf("a secret is in argv: %v", c.args)
		}
		if c.args != nil && c.args[0] == "create" && strings.Contains(c.args[2], "gnat-demo-alpha") {
			if !contains2(c.env, "CLAUDE_CODE_OAUTH_TOKEN=claude-secret") || contains2(c.env, "OPENROUTER_API_KEY=ox-secret") {
				t.Errorf("alpha's env: %v", c.env)
			}
		}
	}
}

// Each agent gets its own secret, and only the control plane has them all.
func TestUpGivesEachAgentItsOwnSecretAndControlAllOfThem(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"), agent("beta", "claude"))
	f := useUp(t, nil)
	useOutput(t)
	if err := up("demo"); err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, c := range f.calls {
		if len(c.args) < 3 || c.args[0] != "create" {
			continue
		}
		var secrets []string
		for _, e := range c.env {
			if name, _, _ := strings.Cut(e, "="); strings.HasPrefix(name, "GNAT_SECRET") {
				secrets = append(secrets, e)
			}
		}
		seen[c.args[2]] = strings.Join(secrets, "\n")
	}
	want := map[string]string{
		"gnat-demo-control": `GNAT_SECRETS={"alpha":"pw-1","beta":"pw-2"}`,
		"gnat-demo-alpha":   "GNAT_SECRET=pw-1",
		"gnat-demo-beta":    "GNAT_SECRET=pw-2",
	}
	if !reflect.DeepEqual(seen, want) {
		t.Errorf("got %v, want %v", seen, want)
	}
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c.args, " "), "pw-") {
			t.Errorf("a secret is in argv: %v", c.args)
		}
	}
}

func TestUpRefusesAgentsWhoseNamesMakeInvalidBranches(t *testing.T) {
	for _, name := range []string{"a..b", "x.lock"} {
		useSwarm(t, agent(name, "claude"))
		f := useUp(t, nil)
		useOutput(t)
		if err := up("demo"); err == nil || !strings.Contains(err.Error(), "can't name branches") {
			t.Errorf("%s: got %v", name, err)
		}
		for _, l := range f.lines() {
			if strings.HasPrefix(l, "docker network create") {
				t.Errorf("%s: started a swarm", name)
			}
		}
	}
}

// Only the control container has the GitHub token.
func TestUpGivesTheGitHubTokenToTheControlContainerAlone(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"), agent("beta", "ox"))
	f := useUp(t, nil)
	useOutput(t)
	if err := up("demo"); err != nil {
		t.Fatal(err)
	}
	creates := 0
	for _, c := range f.calls {
		if len(c.args) < 3 || c.args[0] != "create" {
			continue
		}
		creates++
		holds := contains2(c.env, "GH_TOKEN=gh-secret")
		withName := contains2(c.args, "GH_TOKEN")
		if control := c.args[2] == "gnat-demo-control"; holds != control || withName != control {
			t.Errorf("%s: GH_TOKEN in env %v, in args %v", c.args[2], holds, withName)
		}
		for _, e := range c.env {
			if strings.HasPrefix(e, "GH_TOKEN=") && c.args[2] != "gnat-demo-control" {
				t.Errorf("%s has %s", c.args[2], e)
			}
		}
	}
	if creates != 3 {
		t.Errorf("created %d containers", creates)
	}
}

func contains2(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestUpTearsDownWhatItStartedWhenAnAgentFails(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"))
	running := false
	f := useUp(t, func(c command) (result, error) {
		args := strings.Join(c.args, " ")
		switch {
		case c.name != "docker":
		case c.args[0] == "ps" && running:
			return result{stdout: "gnat-demo-control\n"}, nil
		case c.args[0] == "start" && c.args[1] == "gnat-demo-control":
			running = true
		case c.args[0] == "volume" && c.args[1] == "create":
			return result{}, &exitError{Code: 1}
		case args == "network ls --filter label=gnat.name=demo --format {{.Name}}":
			return result{stdout: "gnat-demo\n"}, nil
		}
		return result{}, nil
	})
	useOutput(t)
	if err := up("demo"); exitStatus(err) != 1 {
		t.Fatalf("got %v", err)
	}
	lines := f.lines()
	if !contains(lines, "docker rm -f gnat-demo-control") || lines[len(lines)-1] != "docker network rm gnat-demo" {
		t.Errorf("got %v", lines)
	}
}

func TestUpChecksWhatAnImageNeeds(t *testing.T) {
	useSwarm(t, agent("alpha", "claude"), agent("beta", "ox"))
	swarm, _ := state.Load("demo")
	swarm.Dockerfile = filepath.Join(t.TempDir(), "Dockerfile")
	if err := os.WriteFile(swarm.Dockerfile, []byte("FROM debian\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(swarm); err != nil {
		t.Fatal(err)
	}
	f := useUp(t, func(c command) (result, error) {
		if c.name == "docker" && c.args[0] == "run" {
			return result{stdout: "ox-server\n"}, nil
		}
		return result{}, nil
	})
	useOutput(t)
	err := up("demo")
	if err == nil || !strings.Contains(err.Error(), "lacks ox-server; start it with `FROM gnat-agent`") {
		t.Fatalf("got %v", err)
	}
	var check string
	for _, l := range f.lines() {
		if strings.HasPrefix(l, "docker run") {
			check = l
		}
	}
	if !strings.HasSuffix(check, " git claude ox-server") {
		t.Errorf("checked %q", check)
	}
	for _, l := range f.lines() {
		if strings.HasPrefix(l, "docker network create") {
			t.Error("started a swarm on a bad image")
		}
	}
}

func TestUpRefusesWithoutAgentsCredentialsOrBinary(t *testing.T) {
	useSwarm(t)
	useUp(t, nil)
	useOutput(t)
	if err := up("demo"); err == nil || !strings.Contains(err.Error(), "has no agents; add one with `gnat agent create demo NAME") {
		t.Errorf("got %v", err)
	}

	useSwarm(t, agent("alpha", "claude"))
	secrets := useSecrets(t, nil)
	if err := up("demo"); err == nil || !strings.Contains(err.Error(),
		"no Claude Code OAuth token is saved; run `claude setup-token`, then run `gnat auth login claude`") {
		t.Errorf("got %v", err)
	}

	secrets["claude"] = "x"
	binaryFor = func(string) ([]byte, error) { return nil, errNoBinary }
	if err := up("demo"); err != errNoBinary {
		t.Errorf("got %v", err)
	}
}

var errNoBinary = errors.New("no binary")
