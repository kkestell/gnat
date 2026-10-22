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
	// BinaryPath is the linux gnat-container binary `gnat up` copies into every container.
	BinaryPath = "/opt/gnat/gnat-container"
	// FilesDir holds the swarm's files, staged for the agent to install (0700, agent-owned,
	// never in an image).
	FilesDir = "/opt/gnat-files"
	// LogDir is where the control container keeps each agent's readable log
	// (<agent>.log), events.jsonl, and control.db; it is a bind mount of the swarm's logs
	// directory on the host, so they survive `gnat down`.
	LogDir = "/var/log/gnat"
	// GitPort is the port the control container serves git on, to the swarm's network
	// only: the agents' clone, fetch, and push.
	GitPort = 7001
	// WebPort is the port the UI is served on inside the control container.
	WebPort = 8080
	// TimeoutStatus is `wait`'s exit status when it gives up, as timeout(1)'s.
	TimeoutStatus = 124
	// HookTimeoutSeconds is how long the hook waits on the control plane: the harness
	// waits on the hook for twice as long.
	HookTimeoutSeconds = 5
)

// Command is the in-container command line for a gnat-container subcommand, such as
// `gnat-container hook`.
func Command(sub string, args ...string) []string {
	return append([]string{BinaryPath, sub}, args...)
}
