package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Settings struct {
	Workers *Settings `json:"-" yaml:"-"`
	Harness string    `json:"harness" yaml:"harness"`
	Model   string    `json:"model" yaml:"model"`
	Effort  string    `json:"effort" yaml:"effort"`
}

func StateDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("XDG_STATE_HOME must be absolute")
	}
	return filepath.Join(base, "gnat"), nil
}

func LoadSettings() (Settings, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Settings{}, err
		}
		base = filepath.Join(home, ".config")
	}
	if !filepath.IsAbs(base) {
		return Settings{}, fmt.Errorf("XDG_CONFIG_HOME must be absolute")
	}
	path := filepath.Join(base, "gnat", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, fmt.Errorf("orchestrator configuration %s: %w (set orchestrator.harness, orchestrator.model, and orchestrator.effort)", path, err)
	}
	var config struct {
		Orchestrator Settings  `yaml:"orchestrator"`
		Workers      *Settings `yaml:"workers"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return Settings{}, fmt.Errorf("configuration %s: %w", path, err)
	}
	s := config.Orchestrator
	s.Workers = config.Workers
	for _, field := range []struct{ name, value string }{{"harness", s.Harness}, {"model", s.Model}, {"effort", s.Effort}} {
		if strings.TrimSpace(field.value) == "" {
			return Settings{}, fmt.Errorf("configuration %s: missing orchestrator.%s", path, field.name)
		}
	}
	if err := s.Validate(); err != nil {
		return Settings{}, fmt.Errorf("configuration %s: %w", path, err)
	}
	return s, nil
}

func ResolveSettings(base, override Settings) Settings {
	if override.Harness != "" {
		base.Harness = override.Harness
	}
	if override.Model != "" {
		base.Model = override.Model
	}
	if override.Effort != "" {
		base.Effort = override.Effort
	}
	base.Workers = nil
	return base
}

// Validate checks neutral required settings; adapters validate provider choices.
func (s Settings) Validate() error {
	switch s.Harness {
	case "claude", "codex", "opencode":
	default:
		return fmt.Errorf("unsupported harness %q (use claude, codex, or opencode)", s.Harness)
	}
	if strings.TrimSpace(s.Model) == "" || strings.TrimSpace(s.Effort) == "" {
		return fmt.Errorf("harness model and effort are required")
	}
	return nil
}
