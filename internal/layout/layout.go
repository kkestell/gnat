// Package layout holds the paths and identities of a gnat container, which both the host
// (to set containers up) and the code in them (to find things) need.
package layout

const (
	// ContainerWorkspace is where an agent's clone of the repository lives.
	ContainerWorkspace = "/workspace"
	// AgentHome is the agent user's home, where each harness keeps its user-level config.
	AgentHome = "/home/agent"
	// AgentUID is the agent user's UID, which owns the workspace.
	AgentUID = 1000
	// BinaryPath is the linux gnat binary `gnat up` copies into every container.
	BinaryPath = "/opt/gnat/gnat"
	// FilesDir holds the swarm's files, staged for the agent to install (0700, agent-owned,
	// never in an image).
	FilesDir = "/opt/gnat-files"
)

// Command is the in-container command line for a hidden subcommand, such as
// `gnat internal hook`.
func Command(sub string, args ...string) []string {
	return append([]string{BinaryPath, "internal", sub}, args...)
}
