// Package state stores each swarm's definition (Dockerfile, GitHub repo, agents, files)
// as JSON under $XDG_STATE_HOME/gnat/swarms, on the host.
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// NamePattern is what swarm and agent names must match.
const NamePattern = `[a-zA-Z0-9][a-zA-Z0-9_.-]*`

var nameRE = regexp.MustCompile(`^` + NamePattern + `\z`)

const (
	// Control is the name of the control plane's container, so no agent can have it.
	Control = "control"
	// AgentVar is the agent's name, in the environment of its daemon and everything the
	// daemon runs.
	AgentVar = "GNAT_AGENT"
	// SecretVar is an agent's secret, which proves who it is to the control plane, in the
	// environment of its daemon and everything the daemon runs.
	SecretVar = "GNAT_SECRET"
	// SecretsVar is every agent's secret, by name, as a JSON object, in the control
	// plane's environment.
	SecretsVar = "GNAT_SECRETS"
	// DefaultHarness runs the turns of an agent saved without one.
	DefaultHarness = "claude"
)

// Error is a problem with the swarms' definitions or the names and paths in them.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func errorf(format string, args ...any) error { return &Error{fmt.Sprintf(format, args...)} }

// An Agent is one member of a swarm. Empty Model and Effort mean the harness's default;
// they are null on disk.
type Agent struct {
	Name string
	// ID tells the agent from an earlier one of the same name, so that one deleted and
	// created again starts without its predecessor's history. It is given once, when the
	// agent is created, and isn't a setting. Agents saved before IDs have none.
	ID           string
	Instructions string
	Model        string
	Effort       string
	// Harness is the program it runs its turns with; see the harness package.
	Harness string
	// Merge is whether the agent may merge pull requests. A definition saved without it
	// has none.
	Merge bool
}

type agentJSON struct {
	Name         string  `json:"name"`
	ID           string  `json:"id,omitempty"`
	Instructions string  `json:"instructions"`
	Model        *string `json:"model"`
	Effort       *string `json:"effort"`
	Harness      string  `json:"harness"`
	Merge        bool    `json:"merge"`
}

func (a Agent) MarshalJSON() ([]byte, error) {
	return json.Marshal(agentJSON{a.Name, a.ID, a.Instructions, optional(a.Model), optional(a.Effort), a.Harness, a.Merge})
}

func (a *Agent) UnmarshalJSON(data []byte) error {
	var j agentJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}
	*a = Agent{Name: j.Name, ID: j.ID, Instructions: j.Instructions, Harness: j.Harness, Merge: j.Merge}
	if j.Model != nil {
		a.Model = *j.Model
	}
	if j.Effort != nil {
		a.Effort = *j.Effort
	}
	if a.Harness == "" {
		a.Harness = DefaultHarness
	}
	return nil
}

// A File is a file outside source control that every agent gets in its clone.
type File struct {
	Host string `json:"host"`
	// Path is relative to /workspace.
	Path string `json:"path"`
}

// A Swarm is a named set of agents on one repository. An empty Dockerfile means none.
type Swarm struct {
	Name       string
	Dockerfile string
	Repo       string
	Agents     []Agent
	Files      []File
}

// Agent is the swarm's agent with that name.
func (s *Swarm) Agent(name string) (Agent, error) {
	for _, a := range s.Agents {
		if a.Name == name {
			return a, nil
		}
	}
	return Agent{}, errorf("no agent '%s' in swarm '%s'", name, s.Name)
}

// swarmJSON is a swarm's file; its name is the file's.
type swarmJSON struct {
	Dockerfile *string `json:"dockerfile"`
	Repo       string  `json:"repo"`
	Agents     []Agent `json:"agents"`
	Files      []File  `json:"files"`
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ParseFile is a File from HOST_PATH[:WORKSPACE_PATH]; the workspace path defaults to the
// host file's name.
func ParseFile(spec string) (File, error) {
	host, path := spec, ""
	if i := strings.LastIndex(spec, ":"); i >= 0 {
		host, path = spec[:i], spec[i+1:]
	}
	if host == "" {
		return File{}, errorf("file '%s' needs a host path", spec)
	}
	host, err := resolve(host)
	if err != nil {
		return File{}, err
	}
	if path == "" {
		path = filepath.Base(host)
	}
	path, err = CheckWorkspacePath(path)
	if err != nil {
		return File{}, err
	}
	return File{Host: host, Path: path}, nil
}

// resolve makes path absolute, expands a leading ~, and resolves symlinks in the part of
// it that exists.
func resolve(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = home + path[1:]
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// Resolve the longest existing prefix, then put back the rest.
	rest := ""
	for p := path; ; p = filepath.Dir(p) {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(real, rest), nil
		}
		if filepath.Dir(p) == p {
			return path, nil
		}
		rest = filepath.Join(filepath.Base(p), rest)
	}
}

// CheckWorkspacePath is the path normalized, if it names a place inside the workspace
// that isn't git's.
func CheckWorkspacePath(path string) (string, error) {
	var parts []string
	for _, p := range strings.Split(path, "/") {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	for _, p := range parts {
		if p == ".." {
			parts = nil
			break
		}
	}
	if strings.HasPrefix(path, "/") || len(parts) == 0 || parts[0] == ".git" {
		return "", errorf("workspace path '%s' must be relative to /workspace, inside it, and outside .git", path)
	}
	return strings.Join(parts, "/"), nil
}

func stateHome() string {
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return base
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state")
}

// Directory is where swarm definitions live, one JSON file per swarm.
func Directory() string { return filepath.Join(stateHome(), "gnat", "swarms") }

// LogsDirectory is where the swarm's agent logs and events.jsonl are kept, across `up`
// and `down`.
func LogsDirectory(swarm string) (string, error) {
	if err := CheckName(swarm, "swarm name"); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(Directory()), "logs", swarm), nil
}

// CheckName returns an error if value isn't a valid name.
func CheckName(value, what string) error {
	if !nameRE.MatchString(value) {
		return errorf("%s '%s' must match %s", what, value, NamePattern)
	}
	return nil
}

func path(name string) (string, error) {
	if err := CheckName(name, "swarm name"); err != nil {
		return "", err
	}
	return filepath.Join(Directory(), name+".json"), nil
}

// Exists reports whether a swarm with that name is defined.
func Exists(name string) (bool, error) {
	p, err := path(name)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular(), nil
}

// Names are the defined swarms', sorted.
func Names() ([]string, error) {
	entries, err := os.ReadDir(Directory())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".json"); ok && !e.IsDir() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// Load is the swarm with that name.
func Load(name string) (*Swarm, error) {
	p, err := path(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errorf("no swarm named '%s'; create it with `gnat create %s`", name, name)
	} else if err != nil {
		return nil, err
	}
	var j swarmJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	s := &Swarm{Name: name, Repo: j.Repo, Agents: j.Agents, Files: j.Files}
	if j.Dockerfile != nil {
		s.Dockerfile = *j.Dockerfile
	}
	return s, nil
}

// Save writes the swarm's definition, replacing the old one.
func Save(s *Swarm) error {
	p, err := path(s.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	j := swarmJSON{Dockerfile: optional(s.Dockerfile), Repo: s.Repo, Agents: s.Agents, Files: s.Files}
	if j.Agents == nil {
		j.Agents = []Agent{}
	}
	if j.Files == nil {
		j.Files = []File{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(j); err != nil {
		return err
	}
	tmp := strings.TrimSuffix(p, ".json") + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Delete removes the swarm's definition and its logs; a swarm that isn't there is fine.
func Delete(name string) error {
	p, err := path(name)
	if err != nil {
		return err
	}
	logs, err := LogsDirectory(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.RemoveAll(logs)
}
