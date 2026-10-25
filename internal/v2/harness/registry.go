package harness

import (
	"context"
	"fmt"

	"github.com/kkestell/gnat/internal/v2/store"
)

// Registry selects adapters from saved agent settings, independently for every turn.
type Registry struct{}

// Capabilities describes the guarantees shared by the initial integrations.
// Model/effort choices are validated by providers where discovery is available.
type Capabilities struct {
	Harness                                     string
	Resume, Interrupt, HTTPMCP, MidTurnDelivery bool
}

func (Registry) Capabilities(name string) (Capabilities, error) {
	if _, err := adapter(name); err != nil {
		return Capabilities{}, err
	}
	return Capabilities{Harness: name, Resume: true, Interrupt: true, HTTPMCP: true}, nil
}

type provider interface {
	Probe(context.Context, store.Settings) error
	Run(context.Context, *store.Claim, func(map[string]any) error) error
}

func adapter(name string) (provider, error) {
	switch name {
	case "claude":
		return Claude{}, nil
	case "codex":
		return Codex{}, nil
	case "opencode":
		return OpenCode{}, nil
	default:
		return nil, fmt.Errorf("unsupported harness %q (use claude, codex, or opencode)", name)
	}
}
func (Registry) Probe(ctx context.Context, settings store.Settings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	a, err := adapter(settings.Harness)
	if err != nil {
		return err
	}
	return a.Probe(ctx, settings)
}
func (Registry) Run(ctx context.Context, c *store.Claim, emit func(map[string]any) error) error {
	a, err := adapter(c.Agent.Settings.Harness)
	if err != nil {
		return err
	}
	return a.Run(ctx, c, emit)
}
func prompt(c *store.Claim) string {
	if c.Sender != "" && c.Sender != "user" {
		return "Message from " + c.Sender + ":\n" + c.Text
	}
	return c.Text
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func text(v any) string           { s, _ := v.(string); return s }
func session(ref, saved string, emit func(map[string]any) error) error {
	if ref == "" || (saved != "" && ref != saved) {
		return fmt.Errorf("harness returned an unexpected session identity; retry the saved conversation or explicitly start a new one")
	}
	return emit(map[string]any{"type": "session", "session_ref": ref})
}
