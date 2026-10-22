package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if got, want := profilesDirectory(), filepath.Join(os.Getenv("HOME"), ".config", "gnat", "profiles"); got != want {
		t.Errorf("profiles directory = %q, want %q", got, want)
	}
}

func TestBundledProfilesCreateAgents(t *testing.T) {
	for _, name := range []string{"coder", "reviewer", "researcher"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "profiles", name+".yaml"))
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
