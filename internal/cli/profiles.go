package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/kkestell/gnat/internal/state"
)

// agentOptions are the settings a profile can supply before explicit flags override them.
type agentOptions struct {
	Instructions string `yaml:"instructions"`
	Harness      string `yaml:"harness"`
	Model        string `yaml:"model"`
	Effort       string `yaml:"effort"`
	// Merge is whether the agent may merge pull requests.
	Merge bool `yaml:"merge"`
}

func configDirectory() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "gnat")
}

func profilesDirectory() string { return filepath.Join(configDirectory(), "profiles", "agents") }

func loadProfile(name string) (agentOptions, error) {
	options := agentOptions{Harness: state.DefaultHarness}
	if err := state.CheckName(name, "profile name"); err != nil {
		return options, err
	}
	err := decodeProfile(filepath.Join(profilesDirectory(), name+".yaml"), "profile", name, &options)
	return options, err
}

// swarmProfile is a swarm's agents, each from an agent profile and its own settings.
type swarmProfile struct {
	Agents []swarmProfileAgent `yaml:"agents"`
}

// swarmProfileAgent is one agent, or with Count, that many named NAME-1 through NAME-COUNT.
// Settings that are present, even if empty, replace the agent profile's.
type swarmProfileAgent struct {
	Name         string  `yaml:"name"`
	Count        int     `yaml:"count"`
	Profile      string  `yaml:"profile"`
	Instructions *string `yaml:"instructions"`
	Harness      *string `yaml:"harness"`
	Model        *string `yaml:"model"`
	Effort       *string `yaml:"effort"`
	Merge        *bool   `yaml:"merge"`
}

func swarmProfilesDirectory() string { return filepath.Join(configDirectory(), "profiles", "swarms") }

// loadSwarmProfile is the agents the named swarm profile describes, in order.
func loadSwarmProfile(name string) ([]state.Agent, error) {
	if err := state.CheckName(name, "swarm profile name"); err != nil {
		return nil, err
	}
	path := filepath.Join(swarmProfilesDirectory(), name+".yaml")
	var profile swarmProfile
	if err := decodeProfile(path, "swarm profile", name, &profile); err != nil {
		return nil, err
	}
	if len(profile.Agents) == 0 {
		return nil, fmt.Errorf("swarm profile %s has no agents", path)
	}
	var agents []state.Agent
	for _, entry := range profile.Agents {
		if entry.Count < 0 {
			return nil, fmt.Errorf("swarm profile %s: agent %q has a negative count", path, entry.Name)
		}
		options := agentOptions{Harness: state.DefaultHarness}
		if entry.Profile != "" {
			var err error
			if options, err = loadProfile(entry.Profile); err != nil {
				return nil, err
			}
		}
		if entry.Merge != nil {
			options.Merge = *entry.Merge
		}
		for _, setting := range []struct {
			value  *string
			target *string
		}{
			{entry.Instructions, &options.Instructions},
			{entry.Harness, &options.Harness},
			{entry.Model, &options.Model},
			{entry.Effort, &options.Effort},
		} {
			if setting.value != nil {
				*setting.target = *setting.value
			}
		}
		names := []string{entry.Name}
		if entry.Count > 0 {
			names = names[:0]
			for i := 1; i <= entry.Count; i++ {
				names = append(names, fmt.Sprintf("%s-%d", entry.Name, i))
			}
		}
		for _, agentName := range names {
			agents = append(agents, state.Agent{
				Name: agentName, Instructions: options.Instructions, Harness: options.Harness,
				Model: options.Model, Effort: options.Effort, Merge: options.Merge,
			})
		}
	}
	return agents, nil
}

// decodeProfile decodes the single YAML document at path into v, rejecting unknown settings.
// what and name describe it when it's missing.
func decodeProfile(path, what, name string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("load %s %q: %w", what, name, err)
	}
	defer f.Close()
	decoder := yaml.NewDecoder(f)
	decoder.KnownFields(true)
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("decode %s %s: %w", what, path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("expected a single YAML document")
		}
		return fmt.Errorf("decode %s %s: %w", what, path, err)
	}
	return nil
}

func profileCommands() *cobra.Command {
	var swarms bool
	list := &cobra.Command{
		Use: "list", Short: "list available agent profiles, or swarm profiles with --swarm", Args: cobra.NoArgs,
		RunE: action(func(*cobra.Command, []string) error {
			if swarms {
				return listProfiles(swarmProfilesDirectory(), "swarm profile name")
			}
			return listProfiles(profilesDirectory(), "profile name")
		}).run,
	}
	list.Flags().BoolVar(&swarms, "swarm", false, "list swarm profiles instead of agent profiles")
	return group("profiles", "discover agent and swarm profiles in gnat's config directory", list)
}

func listProfiles(directory, what string) error {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		if state.CheckName(name, what) == nil {
			fmt.Fprintln(stdout, name)
		}
	}
	return nil
}
