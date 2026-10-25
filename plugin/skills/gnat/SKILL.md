---
name: gnat
description: Open and operate Gnat v2 local coding-agent workspaces through a persistent browser orchestrator, with Claude Code, Codex, or OpenCode workers in managed Git worktrees. Use when the user wants to direct a local swarm, inspect workers, integrate their work, or recover a saved workspace.
---

# Operating Gnat

Gnat v2 is a native macOS application. The user directs a persistent orchestrator
in the browser's right sidebar. The orchestrator runs in the original checkout,
creates/removes workers through Gnat MCP tools, and chooses how to collaborate.
Workers have separate managed worktrees and independent persistent sessions.
Docker, GitHub, a remote and Gnat-specific credentials are unnecessary.

## Open the workspace

Check `command -v gnat` and `gnat --help`. If missing, use `/gnat:install`.
If help exposes the old `create`/`up` command surface instead of `--no-open`, it
is v1; install v2 before following this skill. Do not use v1 commands or profiles.

From the requested local Git checkout with an initial commit, run `gnat` to
open its browser UI, or `gnat --no-open` to return the URL. Required new-swarm
settings are `orchestrator.harness`, `orchestrator.model` and
`orchestrator.effort` in `~/.config/gnat/config.yaml` (or under
`$XDG_CONFIG_HOME/gnat`). Existing swarms retain their saved settings. Missing
values are errors; consult the repository's README and `docs/setup.md`.

The supported harnesses are `claude`, `codex`, and `opencode`, using their normal
local installation/login and trusted process privileges. Match model and effort
to the harness. Do not rewrite global MCP settings, project instructions or
provider credentials. Gnat supplies MCP connections per launch.

## Direct and inspect work

Return the workspace URL and help the user formulate direction for its sidebar.
The public CLI does not create workers, send prompts, poll results or manage
turns. Do not invent replacement CLI commands or use private daemon credentials
to impersonate the browser or agents. If already running as a Gnat agent, use
the launch-bound `gnat` MCP server for the operations below.

`list_agents`, `read_agent` and `send_message` are available to all Gnat agents.
Only the orchestrator can call `create_agent`, `interrupt_agent` or
`remove_agent`. Tools infer sender/swarm from their authenticated connection.
Mutation request IDs are unique per deliberate operation and reused only for a
retry. Interrupt targets the observed running turn ID.

Workers can use different harnesses and start at committed HEAD or a specified
base. Messages queue durably and start later turns automatically. Execution
outcomes, questions, progress and results are conversation content; there is no
Gnat task registry, mandatory reporting ceremony or fixed team topology.

Worker cards expose execution state, path, branch and transcripts. Removed
workers leave the team view and remain inspectable through history. Switching
workspaces changes only the view; focus an older tab to refresh workspace links.

## Integrate and recover

Worker commits are already shared locally. The orchestrator uses ordinary Git
in the original checkout to merge or otherwise integrate them and run project
checks. Preserve existing staged/unstaged/untracked work as with any local
integration. Dirty worktrees and unmerged branches can refuse removal; repair
with ordinary Git and retry without forcing deletion.

Pause holds queued input and lets turns finish. Stop interrupts all agents and
holds input. Interrupt stops the orchestrator turn and pauses scheduling.
Resume/Retry rechecks prerequisites and releases pending input. Interrupted input
is never silently replayed; send new direction explicitly.

New conversation keeps identity, checkout, team, queue and earlier history. An
active turn first offers interruption; after it settles, choose New conversation
again. The fresh session receives saved team/queue orientation and can inspect
prior conversations. A failed exact resume does not silently create a session.

Closing the browser/terminal leaves work running. `gnat daemon stop` interrupts
managed execution across all swarms, reaps processes and preserves saved work.
After updating, reopen with `gnat` for the new listener URL. After a crash, the
user cleans up surviving processes before resuming; automatic crash cleanup,
workspace relocation and unregistering are outside the initial v2 scope.
