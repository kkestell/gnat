# Architecture

This is what the code can't say: the shape of the system, the lines drawn between its parts, and the reasons for them. The packages are listed in `AGENTS.md`; the commands and formats in `docs/usage.md` and `docs/setup.md`. A change that keeps these lines where they are shouldn't need to change this file.

## Runtime

`cmd/gnat` uses `internal/entry`. A normal invocation discovers a committed
local checkout, starts or reuses a detached per-user daemon, registers or opens
its swarm, and prints/opens `/swarms/{id}`. The public surface is `gnat`,
`--no-open`, `--help`, `--version`, and `gnat daemon stop`.

`internal/workspace` discovers checkouts and manages worker Git resources. Canonical checkout roots identify
workspaces; the canonical Git common directory is separate metadata. User
worktrees sharing a common directory have distinct swarms. Saved agent working
directories identify managed checkouts explicitly. Nothing contacts GitHub or
modifies the original checkout during registration.

`internal/store` owns `$XDG_STATE_HOME/gnat/gnat.db`, defaulting to
`~/.local/state/gnat/gnat.db`. SQLite `user_version` identifies the schema;
unknown versions and nonempty unversioned databases are refused. Workspace,
swarm, pending orchestrator, initial conversation, and ordered versioned creation
events commit together. A unique canonical root and unique workspace-to-swarm
relationship prevent duplicate identities. A single database connection
serializes registration within the daemon; snapshots include a sequence cursor.
New swarms resolve the required orchestrator harness/model/effort from
`$XDG_CONFIG_HOME/gnat/config.yaml`, defaulting to `~/.config/gnat/config.yaml`.
Reopening uses saved settings.

`internal/daemon` holds an advisory lock throughout its lifetime. A private
`runtime/daemon.json` records the ephemeral loopback address, PID, generation,
and CLI token. A readiness handshake verifies generation before reuse. Startup
runs a detached copy of the executable; it survives the launching terminal.
Authenticated CLI mutations register workspaces and stop the daemon. Host and
Origin checks protect the local HTTP destination. PID values are diagnostic;
shutdown uses the authenticated endpoint, never a signal to a saved PID.

`internal/core` owns scheduling and controls through a neutral adapter
interface. Each agent has at most one active turn; workers and different swarms run
independently. Accepted messages, atomic dequeue/turn creation, outcomes, and
normalized correlated events commit to SQLite together. Durable request IDs
return the original mutation result on retry. New databases are created with the complete schema in one transaction;
unsupported schema versions are refused without an upgrade path. Sender and recipient IDs persist
with each message; caller-scoped request IDs make deliberate duplicate messages
distinct while transport retries reuse results.

`internal/harness.Registry` selects Claude Code, Codex app-server, or
OpenCode ACP from each agent's saved settings. The core depends only on Probe/Run.
Each turn owns a subprocess group in the agent's saved checkout; closing it reaps
children. Normal local credentials/configuration and native delegation remain
intact. The provider session ID commits before output, and later turns resume
that exact identity. A failed resume is an error; only explicit conversation
reset starts fresh. Session creation is lazy after prerequisite checks.

Claude runs `claude -p` with stream-json, append-system-prompt, and launch-only
MCP configuration. Codex uses app-server initialize/account/model discovery,
thread/start or thread/resume with developer instructions and per-thread MCP
config, then turn/start. OpenCode uses ACP 1 initialize with HTTP MCP and resume
capabilities, session/new or session/resume with session MCP, config options for
model/effort, and assistant-context instructions on session/prompt. Providers own
their normal session files; SQLite holds the neutral session reference. No
second Gnat session file or empty HOME is used.

Trusted unattended execution uses Claude's permission bypass, Codex's never
approval policy and full local access, and ACP allow_once permission responses.
Codex and OpenCode share bounded JSON-RPC transport/process supervision but keep
provider behavior separate. Cancellation targets the exact turn/session, then
process-group cleanup bounds shutdown. Output translates before reaching the
store/browser; native child activity stays in the parent's transcript. Codex
usage derives a turn delta from provider totals; OpenCode emits end-prompt token
usage and omits cumulative session cost. Unknown metrics stay absent. OpenCode
checks authentication when the selected provider runs because ACP exposes no
read-only credential-status operation.

The daemon embeds a workspace switcher and resizable right-sidebar chat, using
the React/Tailwind UI in `ui/`. Every card uses the same scrolling transcript component, Markdown renderer,
tool groups and top fade, including workers and the orchestrator. Tool groups
expand into individual calls only in the expanded worker; worker cards and the
orchestrator show each group as one line.
`make ui` builds checked-in JavaScript/CSS under `internal/daemon/assets`; native
builds embed those assets without a frontend toolchain. Browser mutations require the intended Host, same Origin, and a custom browser header;
CLI registration/shutdown still requires its private bearer token. Snapshots
include transcript, queue, conversation history, and a cursor from one read
transaction. SSE reads only committed events after that cursor, scoped by swarm;
reconnection reloads a snapshot and subscribes after its cursor, deduplicating
by event sequence. Other state changes append their event and refresh
`/api/swarms/{id}/state`, the snapshot without event history; a finished turn
reloads the full snapshot. Core reads the swarm record or a queued count rather
than a snapshot. Streamed text fragments stay durable while a turn runs; the
turn's outcome transaction merges each run of fragments into one `message` event
at the run's first sequence, so saved history and `read_agent` hold whole text
blocks. Initial fetch failures and failed transition refreshes retry
the same recovery path. Workspace links refresh on window focus without changing
scheduling or the selected swarm. Page hide closes the stream, and restored
browser-cache pages reconnect. Unknown metrics stay absent.

Pause holds queued input; stop/interrupt cancel execution and hold scheduling.
Failures and interruptions never requeue delivered input. Resume/retry rechecks
prerequisites and delivers only pending messages. The browser offers interruption before an active conversation reset; the reset
requires a settled turn, retains old history and queued input, and preserves the checkout.
Daemon shutdown cancels scheduling, kills active process groups, waits for
children and outcomes, then closes SQLite. Startup marks persisted running turns
interrupted with the same outcome rules as a normal interruption: stopped swarms
stay stopped, others pause, and interrupted workers notify the orchestrator. Restart uses a new ephemeral listener; the
user reopens with `gnat` to get its URL. Surviving processes after daemon crashes
remain the user's cleanup responsibility. Worker failures enqueue system messages to the orchestrator. Missing saved paths are reported without recreating them.

Workers carry a separate membership state and durable provisioning stage, base
commit, branch, and path under `swarms/<swarm>/agents/<agent>/worktree`.
Creation reserves an identity and initial message before allocating Git resources
asynchronously. Stages reconcile interrupted provisioning at startup; collisions
and changed resources are reported without resets. Removal cancels queued input,
stops execution, removes the worktree without force, and deletes the branch with
`git branch -d`. Refusals keep a visible removing record for repair/retry; removed
identities and attributed history remain in SQLite. Stop/resume keeps workers,
worktrees, sessions, and queues. New orchestrator conversations get bounded saved
team/queue orientation and tools to inspect earlier history.

`internal/mcp` exposes the six coordination tools through stateless
JSON-response Streamable HTTP at `/mcp`. Random in-memory bearer bindings resolve
the agent and swarm; arguments cannot select the caller or another swarm.
Discovery and execution enforce orchestrator-only membership management. Host
and Origin checks cover MCP as well as browser routes; agent credentials do not
grant CLI or browser mutation authority. Each adapter launch receives its MCP
connection and factual runtime instructions through provider-supported overrides,
without rewriting global or project settings. New launches refresh ephemeral
bindings after restart. Optional `workers` defaults are resolved and saved when
a swarm is registered; creation overrides apply only to the new worker.

Worker worktrees share Git objects and refs with the original checkout. Agents
integrate worker commits there through ordinary Git and verify the combined
result with repository tools. Gnat adds no delivery operation or integration
state. Deterministic tests exercise divergent merges, preservation of staged and
unstaged local work through stash/restore, conflict abort, and branch removal
refusals; an opt-in live test exercises the same local integration path through
production adapters and authenticated MCP. Git inspection UI remains deferred.

The embedded main area displays live worker membership, metadata, and transcript
inspection independently of the orchestrator sidebar. Cards show membership
while a worker is not an active member and execution lifecycle otherwise, with
errors inline. The header shows paused or stopped scheduling with Resume.
Removed workers leave the team but remain selectable in the header, and their
cancelled input appears in their transcripts. All rendered text uses DOM text content;
provider-specific messages are translated before the browser sees them.

`make build` and `make install` build the native executable with embedded chat
assets. `make release VERSION=<tag>` produces a macOS arm64 archive containing
only that executable and an external checksum; the release workflow
injects/verifies the tag version. `make test` runs all Go packages and the
browser tests, and `make lint` checks Go and TypeScript code.
