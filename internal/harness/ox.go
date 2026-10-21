package harness

import "github.com/kkestell/gnat/internal/layout"

// newOx is Ox (github.com/kkestell/ox), an ACP server: `ox-server acp`.
func newOx() *ox {
	return &ox{acpHarness{
		info: info{
			name:             "ox",
			program:          "ox-server",
			credential:       "OPENROUTER_API_KEY",
			credentialSource: "create an OpenRouter API key",
			efforts:          []string{"default", "none", "minimal", "low", "medium", "high", "xhigh", "max"},
			configDir:        layout.AgentHome + "/.config/ox",
			// Ox's session database, and the session the runner continues.
			stateDirs: []string{layout.AgentHome + "/.local/share/ox", acpStateDir},
		},
		command: []string{"ox-server", "acp"},
	}}
}

type ox struct{ acpHarness }

func (*ox) ConfigFiles(instructions, model, effort string) map[string][]byte {
	// Ox's ACP sessions take their model and effort from these settings. The container is
	// the sandbox, so Auto mode runs tools without asking.
	settings := hookSettings()
	settings["mode"] = "auto"
	if model != "" {
		settings["model"] = model
	}
	if effort != "" {
		settings["effort"] = effort
	}
	return map[string][]byte{"AGENTS.md": []byte(instructions), "settings.json": jsonBytes(settings)}
}
