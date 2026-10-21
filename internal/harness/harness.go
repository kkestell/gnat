// Package harness holds the harnesses an agent runs its turns with, by name.
//
// A Harness gives the host its program, credential, efforts, and config, and gives the
// agent daemon a Runner for its turns. One whose agent speaks ACP is built on acp.go.
// Register a new one in All.
package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kkestell/gnat/internal/events"
	"github.com/kkestell/gnat/internal/hook"
	"github.com/kkestell/gnat/internal/layout"
)

// Emit takes each event a turn produces (see package events), to send to the control plane.
type Emit func(events.Event)

// Runner runs an agent's turns in its container, one at a time, continuing its session.
type Runner interface {
	// Run runs one turn on the message text, emitting its events, and returns its exit
	// status. new starts a fresh session instead of continuing the last one. stop is closed,
	// before Stop is called, when the turn is to end early; a turn that starts after it is
	// closed ends as soon as it can. An error is a failure of the runner itself, not of
	// the turn.
	Run(text string, new bool, stop <-chan struct{}) (int, error)

	// Stop ends the running turn, if there is one. It's called from another goroutine.
	Stop()
}

// Harness is a harness, as `gnat up` sets it up on the host and the agent daemon runs it.
// The model and effort are empty when the agent has none.
type Harness interface {
	// Name is what `gnat agent create --harness` takes.
	Name() string
	// Program is the command the agent image must have.
	Program() string
	// Credential is the environment variable holding its credential, and CredentialSource
	// says how to get one.
	Credential() string
	CredentialSource() string
	// Efforts are the values `--effort` takes.
	Efforts() []string
	// ConfigDir is its user-level config directory in the agent container.
	ConfigDir() string
	// StateDirs are the directories, under the agent's home, where it keeps its sessions.
	// Each is on a volume, so the agent's sessions outlive its container.
	StateDirs() []string
	// ConfigFiles are its user-level config files, by name in ConfigDir: the agent's
	// instructions, and settings that install gnat's hook.
	ConfigFiles(instructions, model, effort string) map[string][]byte
	// Runner makes a runner for the agent's turns, in its container.
	Runner(model, effort string, emit Emit) Runner
}

// info implements the parts of Harness that are plain data.
type info struct {
	name, program, credential, credentialSource string
	efforts                                     []string
	configDir                                   string
	stateDirs                                   []string
}

func (i info) Name() string             { return i.name }
func (i info) Program() string          { return i.program }
func (i info) Credential() string       { return i.credential }
func (i info) CredentialSource() string { return i.credentialSource }
func (i info) Efforts() []string        { return i.efforts }
func (i info) ConfigDir() string        { return i.configDir }
func (i info) StateDirs() []string      { return i.stateDirs }

var all = []Harness{newClaude(), newOx()}

// All returns every harness, in the order `gnat agent create --help` lists them.
func All() []Harness { return append([]Harness(nil), all...) }

// Names returns the name of every harness.
func Names() []string {
	names := make([]string, len(all))
	for i, h := range all {
		names[i] = h.Name()
	}
	return names
}

// Get returns the harness with the name.
func Get(name string) (Harness, error) {
	for _, h := range all {
		if h.Name() == name {
			return h, nil
		}
	}
	return nil, fmt.Errorf("unknown harness %q", name)
}

// hookTimeoutSeconds is how long the harness waits on the hook: twice the hook's own
// timeout on the control plane.
const hookTimeoutSeconds = hook.TimeoutSeconds * 2

// hookSettings is the settings that install gnat's PostToolUse hook, which both harnesses
// run after each tool call.
func hookSettings() map[string]any {
	return map[string]any{
		"hooks": map[string]any{
			"PostToolUse": []any{map[string]any{
				"matcher": "*",
				"hooks": []any{map[string]any{
					"type":    "command",
					"command": strings.Join(layout.Command("hook"), " "),
					"timeout": hookTimeoutSeconds,
				}},
			}},
		},
	}
}

// jsonBytes is a settings file: indented, with no HTML escaping, ending in a newline.
func jsonBytes(value any) []byte { return marshal(value, true) }

// marshal is the JSON for value without escaping HTML: compact with no trailing newline,
// or, with indent, indented and ending in one.
func marshal(value any, indent bool) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(value); err != nil {
		panic(err) // the values are built from literals and decoded JSON
	}
	if indent {
		return buf.Bytes()
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
