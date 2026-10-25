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
	return filepath.Join(base, "gnat", "v2"), nil
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
	switch s.Harness {
	case "claude", "codex", "opencode":
	default:
		return Settings{}, fmt.Errorf("configuration %s: unsupported orchestrator.harness %q (use claude, codex, or opencode)", path, s.Harness)
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
