# gnat

Gnat v2 is becoming a local application for directing coding agents through one
persistent orchestrator. **Milestones one through four are implemented:** workspace
registration, SQLite records, a detached per-user daemon, right-sidebar chat,
and dynamic workers coordinated through MCP. Claude Code, Codex, and OpenCode
can each serve as orchestrator or worker, including mixed teams.

## Build and run on macOS

Requires macOS, Go 1.26 or newer, Git, and at least one installed/authenticated harness
(`claude auth login`, `codex login`, or `opencode auth login`). Build directly from this checkout:

```sh
go build -o gnat ./cmd/gnat
# Or install the native entry point:
go install ./cmd/gnat
```

Create `~/.config/gnat/config.yaml` (or `$XDG_CONFIG_HOME/gnat/config.yaml`):

```yaml
orchestrator:
  harness: claude
  model: sonnet
  effort: high
# Optional defaults for new workers (omitted values inherit the orchestrator):
workers:
  model: sonnet
  effort: high
```

All three values are required for a new swarm. Harness choices are `claude`,
`codex`, and `opencode`. Reopening a swarm uses its saved settings. Model and
effort are provider-specific: Codex validates its advertised model/effort catalog;
OpenCode uses `provider/model` IDs and its ACP config options (`default` effort
for a model without variants). Changing worker harness usually also requires
setting its model and effort, either in `workers` or in the creation request.

In any local Git checkout with an initial commit:

```sh
cd ~/src/my-project
gnat                 # Print the URL and open the default browser.
gnat --no-open       # Print the same workspace URL without opening a browser.
gnat daemon stop     # Stop the daemon; keep saved swarms.
```

Subdirectories and symlink aliases select the same swarm. Separate user worktrees
have separate swarms served by the same daemon. The browser shows saved checkout
metadata and a resizable orchestrator chat. Prompts are durably queued and turns
stream text, tool activity, and reported usage. The orchestrator edits the
original checkout with normal local credentials and trusted process privileges;
harness permissions allow unattended tool use with normal user privileges.
Docker, GitHub, a remote, and Gnat-specific credential setup are unnecessary.

Ask the orchestrator to create workers as needed. Each has a persistent session,
a separate managed worktree, and an immutable-ID branch starting at committed
HEAD (or a requested base ref). The main area shows current workers and their
transcripts; removed workers remain inspectable through the history selector.
Agents exchange ordinary durable messages through the launch-scoped Gnat MCP
server. Messages start recipient turns without polling. Workers can inspect and
message teammates; only the orchestrator creates, interrupts, or removes workers.
Worker commits are available for local Git integration in the original checkout.
Removal stops execution and uses ordinary Git safeguards: dirty worktrees and
unmerged branches are retained with visible errors. Resolve the refusal using
ordinary tools and ask the orchestrator to retry removal.

Pause lets active turns finish and holds queued input for the whole swarm; Stop interrupts all agents and
holds the queue. Resume/Retry enables queued input without replaying interrupted
prompts. Interrupt ends the orchestrator turn and pauses swarm scheduling. New conversation
starts a fresh session at a settled boundary and keeps prior transcript history.
Closing the browser does not stop work. Daemon shutdown interrupts/reaps managed
processes and retains their session references; after a crash, clean up any
surviving processes before resuming.

State lives under `~/.local/state/gnat/v2` (`$XDG_STATE_HOME/gnat/v2` when set),
separate from v1 state. There is no automatic v1 migration. The former v1 CLI is
not exposed by the new entry point; its packages and distribution targets remain
in the repository until later v2 cleanup. Published releases may still be v1;
use the direct source build above for this implementation.

## Development and documentation

`go test ./internal/v2/...` covers the runtime, including real detached
subprocesses on macOS. `make test` and `make lint` include retained v1 code and UI.
Opt in to the mixed live matrix with authenticated local providers:

```sh
GNAT_LIVE_TEST=1 go test ./internal/v2/daemon -run TestLiveCoordination -v -timeout 20m
```

This uses disposable local repositories and exercises each harness as orchestrator
with workers on the other two, plus restart and follow-up delivery. Set
`GNAT_LIVE_HARNESS=claude`, `codex`, or `opencode` to select one orchestrator.
The models in `internal/v2/daemon/live_test.go` must be available locally.

The local Codex 0.164.0 live run currently fails tool discovery with
“timed out negotiating with the code-mode host,” although its HTTP MCP
initialization succeeds. Gnat uses the documented app-server protocol and does
not manage or work around Codex's internal helper. Protocol fixtures cover the
adapter; the full live three-provider matrix remains unverified.

The old `make build`, `make install`, and release targets still build container
helpers; direct native Go builds avoid those obsolete prerequisites.

- [V2 specification](agents/gnat-v2-spec.md): scope and implementation milestones.
- [Architecture](agents/architecture.md): current runtime and legacy boundaries.
- [Setup](docs/setup.md) and [usage](docs/usage.md): v2 entry and historical v1 reference.
