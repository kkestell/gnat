package cli

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/kkestell/gnat/internal/state"
)

func useProfiles(t *testing.T, files map[string]string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(profilesDirectory(), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := writeFile(filepath.Join(profilesDirectory(), name), content); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentProfileAndExplicitOverrides(t *testing.T) {
	const profile = "instructions: |\n  Write code.\n  Add useful tests.\nharness: ox\nmodel: example:model\neffort: high\n"
	for _, tc := range []struct {
		name  string
		yaml  string
		flags []string
		want  state.Agent
	}{
		{
			name: "profile settings", yaml: profile,
			want: state.Agent{Instructions: "Write code.\nAdd useful tests.\n", Harness: "ox", Model: "example:model", Effort: "high"},
		},
		{
			name: "individual override", yaml: profile, flags: []string{"--effort", "max"},
			want: state.Agent{Instructions: "Write code.\nAdd useful tests.\n", Harness: "ox", Model: "example:model", Effort: "max"},
		},
		{
			name: "all settings overridden", yaml: profile,
			flags: []string{"--instructions", "Review code.", "--harness", "claude", "--model", "sonnet", "--effort", "low"},
			want:  state.Agent{Instructions: "Review code.", Harness: "claude", Model: "sonnet", Effort: "low"},
		},
		{
			name: "empty flags restore defaults", yaml: profile, flags: []string{"--model=", "--effort="},
			want: state.Agent{Instructions: "Write code.\nAdd useful tests.\n", Harness: "ox"},
		},
		{
			name: "omitted settings use defaults", yaml: "instructions: Be brief.\n",
			want: state.Agent{Instructions: "Be brief.", Harness: state.DefaultHarness},
		},
		{
			name: "partial profile with instructions flag", yaml: "effort: max\n", flags: []string{"--instructions", "Be brief."},
			want: state.Agent{Instructions: "Be brief.", Harness: state.DefaultHarness, Effort: "max"},
		},
		{
			name: "profile without additional instructions", yaml: "effort: high\n",
			want: state.Agent{Harness: state.DefaultHarness, Effort: "high"},
		},
		{
			name: "clear profile instructions", yaml: profile, flags: []string{"--instructions="},
			want: state.Agent{Harness: "ox", Model: "example:model", Effort: "high"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useSwarm(t)
			useExec(t, nil)
			useProfiles(t, map[string]string{"coder.yaml": tc.yaml})
			// Put explicit flags before --profile too: precedence must not depend on order.
			args := append([]string{"agent", "create", "demo", "alpha"}, tc.flags...)
			args = append(args, "--profile", "coder")
			if status, _, errs := runGnat(t, args...); status != 0 {
				t.Fatalf("status %d: %s", status, errs)
			}
			// Settings are copied into state, independent of the profile after creation.
			if err := os.Remove(filepath.Join(profilesDirectory(), "coder.yaml")); err != nil {
				t.Fatal(err)
			}
			swarm, err := state.Load("demo")
			if err != nil {
				t.Fatal(err)
			}
			tc.want.Name = "alpha"
			if len(swarm.Agents) != 1 || swarm.Agents[0] != tc.want {
				t.Errorf("agents = %+v, want %+v", swarm.Agents, tc.want)
			}
		})
	}
}

func TestAgentProfileErrorsDoNotCreateAgents(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, want string
	}{
		{name: "missing", want: "load profile"},
		{name: "../coder", want: "profile name"},
		{name: "", want: "profile name"},
		{name: "coder", yaml: "instructions: [broken", want: "decode profile"},
		{name: "coder", yaml: "instructions: x\nefort: max\n", want: "field efort not found"},
		{name: "coder", yaml: "instructions: x\ninstructions: y\n", want: "already defined"},
		{name: "coder", yaml: "instructions: x\n---\neffort: max\n", want: "single YAML document"},
		{name: "coder", yaml: "instructions: [x, y]\n", want: "cannot unmarshal"},
		{name: "coder", yaml: "instructions: x\nharness: nope\n", want: "invalid --harness"},
		{name: "coder", yaml: "instructions: x\neffort: extreme\n", want: "claude effort must be one of"},
	} {
		t.Run(tc.name+"/"+tc.want, func(t *testing.T) {
			useSwarm(t)
			f := useExec(t, nil)
			files := map[string]string{}
			if tc.yaml != "" {
				files["coder.yaml"] = tc.yaml
			}
			useProfiles(t, files)
			args := []string{"agent", "create", "demo", "alpha", "--profile", tc.name}
			if status, _, errs := runGnat(t, args...); status == 0 || !strings.Contains(errs, tc.want) {
				t.Fatalf("status %d, stderr %q; want %q", status, errs, tc.want)
			}
			swarm, err := state.Load("demo")
			if err != nil || len(swarm.Agents) != 0 || len(f.calls) != 0 {
				t.Fatalf("swarm = %+v, err = %v, commands = %v", swarm, err, f.lines())
			}
		})
	}
}

func TestCreateAgentWithoutProfileKeepsDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, instructions string
		flags              []string
	}{
		{name: "omitted"},
		{name: "empty", flags: []string{"--instructions="}},
		{name: "whitespace", instructions: "  ", flags: []string{"--instructions", "  "}},
		{name: "additional", instructions: "Be brief.", flags: []string{"--instructions", "Be brief."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useSwarm(t)
			useExec(t, nil)
			useProfiles(t, nil)
			args := append([]string{"agent", "create", "demo", "alpha"}, tc.flags...)
			if status, _, errs := runGnat(t, args...); status != 0 {
				t.Fatalf("status %d: %s", status, errs)
			}
			swarm, err := state.Load("demo")
			if err != nil {
				t.Fatal(err)
			}
			want := state.Agent{Name: "alpha", Instructions: tc.instructions, Harness: state.DefaultHarness}
			if len(swarm.Agents) != 1 || swarm.Agents[0] != want {
				t.Errorf("agents = %+v, want %+v", swarm.Agents, want)
			}
		})
	}
}

func TestProfilesList(t *testing.T) {
	useProfiles(t, map[string]string{
		"reviewer.yaml": "instructions: Review code.\n",
		"coder.yaml":    "instructions: [broken", // Listing still helps find a broken file.
		"notes.txt":     "ignored",
		".hidden.yaml":  "ignored",
	})
	if err := os.Mkdir(filepath.Join(profilesDirectory(), "directory.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if status, out, errs := runGnat(t, "profiles", "list"); status != 0 || out != "coder\nreviewer\n" {
		t.Fatalf("status %d, stdout %q, stderr %q", status, out, errs)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if status, out, errs := runGnat(t, "profiles", "list"); status != 0 || out != "" || errs != "" {
		t.Fatalf("missing directory: status %d, stdout %q, stderr %q", status, out, errs)
	}
}

func TestProfilesUseHomeConfigByDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", t.TempDir())
	profiles := filepath.Join(os.Getenv("HOME"), ".config", "gnat", "profiles")
	if got, want := profilesDirectory(), filepath.Join(profiles, "agents"); got != want {
		t.Errorf("profiles directory = %q, want %q", got, want)
	}
	if got, want := swarmProfilesDirectory(), filepath.Join(profiles, "swarms"); got != want {
		t.Errorf("swarm profiles directory = %q, want %q", got, want)
	}
}

func TestBundledProfilesCreateAgents(t *testing.T) {
	for _, name := range []string{"coder", "reviewer", "researcher", "orchestrator"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "profiles", "agents", name+".yaml"))
			if err != nil {
				t.Fatal(err)
			}
			useSwarm(t)
			useExec(t, nil)
			useProfiles(t, map[string]string{name + ".yaml": string(data)})
			if status, _, errs := runGnat(t, "agent", "create", "demo", "alpha", "--profile", name); status != 0 {
				t.Fatalf("status %d: %s", status, errs)
			}
		})
	}
}

func TestBundledSwarmProfilesCreateSwarms(t *testing.T) {
	bundled := func(kind string) map[string]string {
		paths, err := filepath.Glob(filepath.Join("..", "..", "profiles", kind, "*.yaml"))
		if err != nil || len(paths) == 0 {
			t.Fatalf("bundled %s profiles: %v, %v", kind, paths, err)
		}
		files := map[string]string{}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			files[filepath.Base(path)] = string(data)
		}
		return files
	}
	swarms := bundled("swarms")
	for file := range swarms {
		name := strings.TrimSuffix(file, ".yaml")
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			useExec(t, nil)
			useProfiles(t, bundled("agents"))
			useSwarmProfiles(t, swarms)
			if status, _, errs := runGnat(t, "create", "demo", "--repo", "o/r", "--profile", name); status != 0 {
				t.Fatalf("status %d: %s", status, errs)
			}
		})
	}
}

func useSwarmProfiles(t *testing.T, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(swarmProfilesDirectory(), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := writeFile(filepath.Join(swarmProfilesDirectory(), name), content); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCreateSwarmFromProfile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	useExec(t, nil)
	useProfiles(t, map[string]string{
		"orchestrator.yaml": "instructions: Coordinate.\n",
		"coder.yaml":        "instructions: Write code.\neffort: low\n",
	})
	useSwarmProfiles(t, map[string]string{"team.yaml": `agents:
  - name: lead
    profile: orchestrator
    model: opus
    effort: high
  - name: sonnet
    count: 2
    profile: coder
    model: sonnet
  - name: flash
    count: 1
    profile: coder
    harness: ox
    model: openrouter:deepseek/deepseek-v4.1-flash
    effort: high
  - name: plain
    instructions: ""
`})
	status, out, errs := runGnat(t, "create", "demo", "--repo", "o/r", "--profile", "team")
	if status != 0 || out != "created swarm demo with agents lead, sonnet-1, sonnet-2, flash-1, plain\n" {
		t.Fatalf("status %d, stdout %q, stderr %q", status, out, errs)
	}
	swarm, err := state.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	want := []state.Agent{
		{Name: "lead", Instructions: "Coordinate.", Harness: "claude", Model: "opus", Effort: "high"},
		{Name: "sonnet-1", Instructions: "Write code.", Harness: "claude", Model: "sonnet", Effort: "low"},
		{Name: "sonnet-2", Instructions: "Write code.", Harness: "claude", Model: "sonnet", Effort: "low"},
		{Name: "flash-1", Instructions: "Write code.", Harness: "ox", Model: "openrouter:deepseek/deepseek-v4.1-flash", Effort: "high"},
		{Name: "plain", Harness: state.DefaultHarness},
	}
	if swarm.Repo != "o/r" || !slices.Equal(swarm.Agents, want) {
		t.Errorf("swarm = %+v, want agents %+v", swarm, want)
	}
}

func TestSwarmProfileErrorsDoNotCreateSwarms(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, want string
	}{
		{name: "missing", want: "load swarm profile"},
		{name: "../team", want: "swarm profile name"},
		{name: "team", yaml: "agents: []\n", want: "has no agents"},
		{name: "team", yaml: "agents:\n  - name: a\n    count: -1\n", want: "negative count"},
		{name: "team", yaml: "agents:\n  - name: a\n    efort: max\n", want: "field efort not found"},
		{name: "team", yaml: "agents:\n  - name: a\n---\nagents: []\n", want: "single YAML document"},
		{name: "team", yaml: "agents:\n  - name: a\n    profile: missing\n", want: "load profile"},
		{name: "team", yaml: "agents:\n  - name: a\n    effort: extreme\n", want: "claude effort must be one of"},
		{name: "team", yaml: "agents:\n  - name: a\n    harness: nope\n", want: "unknown harness"},
		{name: "team", yaml: "agents:\n  - count: 2\n", want: "agent name"},
		{name: "team", yaml: "agents:\n  - name: control\n", want: "reserved"},
		{name: "team", yaml: "agents:\n  - name: a-1\n  - name: a\n    count: 1\n", want: "already has an agent 'a-1'"},
	} {
		t.Run(tc.name+"/"+tc.want, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			useExec(t, nil)
			useProfiles(t, nil)
			files := map[string]string{}
			if tc.yaml != "" {
				files["team.yaml"] = tc.yaml
			}
			useSwarmProfiles(t, files)
			status, _, errs := runGnat(t, "create", "demo", "--repo", "o/r", "--profile", tc.name)
			if status == 0 || !strings.Contains(errs, tc.want) {
				t.Fatalf("status %d, stderr %q; want %q", status, errs, tc.want)
			}
			if exists, err := state.Exists("demo"); err != nil || exists {
				t.Fatalf("swarm exists = %v, err = %v", exists, err)
			}
		})
	}
}

func TestSwarmProfilesList(t *testing.T) {
	useProfiles(t, map[string]string{"coder.yaml": "instructions: x\n"})
	useSwarmProfiles(t, map[string]string{"team.yaml": "agents: []\n", "big.yaml": "", "notes.txt": "ignored"})
	if status, out, errs := runGnat(t, "profiles", "list", "--swarm"); status != 0 || out != "big\nteam\n" {
		t.Fatalf("status %d, stdout %q, stderr %q", status, out, errs)
	}
	if status, out, errs := runGnat(t, "profiles", "list"); status != 0 || out != "coder\n" {
		t.Fatalf("status %d, stdout %q, stderr %q", status, out, errs)
	}
}

func TestMergePermissionComesFromTheProfileAndTheFlag(t *testing.T) {
	for _, tc := range []struct {
		name  string
		yaml  string
		flags []string
		want  bool
	}{
		{"none by default", "instructions: x\n", nil, false},
		{"from the profile", "merge: true\n", nil, true},
		{"the flag grants it", "instructions: x\n", []string{"--merge"}, true},
		{"the flag takes it away", "merge: true\n", []string{"--merge=false"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useSwarm(t)
			useExec(t, nil)
			useProfiles(t, map[string]string{"p.yaml": tc.yaml})
			args := append([]string{"agent", "create", "demo", "alpha", "--profile", "p"}, tc.flags...)
			if status, _, errs := runGnat(t, args...); status != 0 {
				t.Fatalf("status %d: %s", status, errs)
			}
			swarm, err := state.Load("demo")
			if err != nil || swarm.Agents[0].Merge != tc.want {
				t.Fatalf("merge = %v, want %v (%v)", swarm.Agents[0].Merge, tc.want, err)
			}
			_, out, _ := runGnat(t, "agent", "list", "demo")
			if got := strings.HasSuffix(strings.TrimSpace(out), "\tmerge"); got != tc.want {
				t.Errorf("list shows merge = %v: %q", got, out)
			}
		})
	}
	useSwarm(t)
	useExec(t, nil)
	if status, _, errs := runGnat(t, "agent", "create", "demo", "beta", "--merge"); status != 0 {
		t.Fatalf("status %d: %s", status, errs)
	}
	swarm, _ := state.Load("demo")
	if !swarm.Agents[0].Merge {
		t.Error("--merge without a profile didn't grant it")
	}
}

func TestSwarmProfilesSetTheMergePermission(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	useExec(t, nil)
	useProfiles(t, map[string]string{"orchestrator.yaml": "merge: true\n", "coder.yaml": "instructions: x\n"})
	useSwarmProfiles(t, map[string]string{"team.yaml": `agents:
  - name: lead
    profile: orchestrator
  - name: demoted
    profile: orchestrator
    merge: false
  - name: promoted
    profile: coder
    merge: true
  - name: coder
    profile: coder
`})
	if status, _, errs := runGnat(t, "create", "demo", "--repo", "o/r", "--profile", "team"); status != 0 {
		t.Fatalf("status %d: %s", status, errs)
	}
	swarm, _ := state.Load("demo")
	got := map[string]bool{}
	for _, a := range swarm.Agents {
		got[a.Name] = a.Merge
	}
	if want := map[string]bool{"lead": true, "demoted": false, "promoted": true, "coder": false}; !maps.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestOnlyTheBundledOrchestratorMerges(t *testing.T) {
	for _, name := range []string{"coder", "reviewer", "researcher", "orchestrator"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "profiles", "agents", name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var options agentOptions
		if err := yaml.Unmarshal(data, &options); err != nil {
			t.Fatal(err)
		}
		if options.Merge != (name == "orchestrator") {
			t.Errorf("%s: merge = %v", name, options.Merge)
		}
	}
}

func TestAgentsWhoseNamesMakeInvalidBranchesAreRefused(t *testing.T) {
	useSwarm(t)
	useExec(t, nil)
	for _, name := range []string{"a..b", "x.lock", "dot."} {
		if status, _, errs := runGnat(t, "agent", "create", "demo", name); status == 0 || !strings.Contains(errs, "can't name branches") {
			t.Errorf("%s: status %d, %q", name, status, errs)
		}
	}
}
