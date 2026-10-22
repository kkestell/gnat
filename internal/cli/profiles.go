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
}

func configDirectory() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "gnat")
}

func profilesDirectory() string { return filepath.Join(configDirectory(), "profiles") }

func loadProfile(name string) (agentOptions, error) {
	options := agentOptions{Harness: state.DefaultHarness}
	if err := state.CheckName(name, "profile name"); err != nil {
		return options, err
	}
	path := filepath.Join(profilesDirectory(), name+".yaml")
	f, err := os.Open(path)
	if err != nil {
		return options, fmt.Errorf("load profile %q: %w", name, err)
	}
	defer f.Close()
	decoder := yaml.NewDecoder(f)
	decoder.KnownFields(true)
	if err := decoder.Decode(&options); err != nil {
		return options, fmt.Errorf("decode profile %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("expected a single YAML document")
		}
		return options, fmt.Errorf("decode profile %s: %w", path, err)
	}
	return options, nil
}

func profileCommands() *cobra.Command {
	list := &cobra.Command{
		Use: "list", Short: "list available agent profiles", Args: cobra.NoArgs,
		RunE: action(func(*cobra.Command, []string) error { return listProfiles() }).run,
	}
	return group("profiles", "discover agent profiles in gnat's config directory", list)
}

func listProfiles() error {
	entries, err := os.ReadDir(profilesDirectory())
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
		if state.CheckName(name, "profile name") == nil {
			fmt.Fprintln(stdout, name)
		}
	}
	return nil
}
