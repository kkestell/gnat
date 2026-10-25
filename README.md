# gnat

Gnat v2 is a local application for directing coding agents through one
persistent orchestrator. **All six v2 milestones are implemented:** workspace
registration, durable chat, dynamic workers, harness neutrality, local integration,
and recovery and native release paths. Claude Code, Codex, and OpenCode
can each serve as orchestrator or worker, including mixed teams. Agents integrate
worker commits into the original checkout with ordinary local Git.

## Build and run on macOS

Requires macOS, Go 1.26 or newer, Git, and at least one installed/authenticated harness
(`claude auth login`, `codex login`, or `opencode auth login`). Build or install from this checkout:

```sh
make build           # Native binary at ./gnat.
make install         # Native go install ./cmd/gnat; put Go's bin directory on PATH.
```

For a release install or the Claude Code plugin
(`/plugin install gnat --marketplace kkestell/gnat`, then `/gnat:install`),
see [setup](docs/setup.md).

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
Ask the orchestrator to merge the worker branches and run the project checks; no
push or separate delivery command is needed. See [local integration](docs/usage.md#integrating-local-work)
for preserving local edits, resolving conflicts, and cleaning up merged workers.
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

During an active turn, New conversation first offers interruption; once it settles,
choose New conversation again. The fresh session keeps workers and queued input;
Resume releases a paused queue. Browser reconnection retries from saved snapshots.
After a daemon restart, run `gnat` again for the new listener URL.

State lives under `~/.local/state/gnat/v2` (`$XDG_STATE_HOME/gnat/v2` when set),
separate from v1 state. There is no automatic v1 migration. The former v1 CLI is
not exposed by the new entry point. Legacy source packages and fixtures remain
in the repository, but native build/install/release targets no longer build or
ship container helpers or profiles. Release archives contain only the native
binary with embedded UI assets. Older published releases may still be v1;
see [installation](docs/setup.md#install-a-release) to check before installing.

## Development and documentation

`go test ./internal/v2/...` covers the runtime, including real detached
subprocesses, orderly shutdown, crash recovery, reset and independent swarms on
macOS. `bun test internal/v2/daemon/page.test.js` checks browser reconnection and
controls. `make test` and `make lint` also include retained v1 code and UI.
Opt in to the mixed live matrix with authenticated local providers:

```sh
GNAT_LIVE_TEST=1 go test ./internal/v2/daemon -run TestLiveCoordination -v -timeout 20m
```

This uses disposable local repositories and exercises each harness as orchestrator
with workers on the other two, plus restart and follow-up delivery. Set
`GNAT_LIVE_HARNESS=claude`, `codex`, or `opencode` to select one orchestrator.
The models in `internal/v2/daemon/live_test.go` must be available locally.

A separate live integration scenario creates two workers, merges their commits,
runs the combined Go tests in the original checkout, preserves staged/unstaged
and untracked work, and removes the integrated workers:

```sh
GNAT_LIVE_TEST=1 go test ./internal/v2/daemon -run '^TestLiveLocalIntegration$' -v -timeout 12m
```

Both roles default to Claude. Set `GNAT_LIVE_HARNESS` for the orchestrator and
`GNAT_LIVE_WORKER` for both workers (`claude`, `codex`, or `opencode`).
Deterministic coverage also checks merge conflicts, abort, and unmerged-branch
removal refusal without calling a provider.

Live MCP checks passed for Claude Code, Codex, and OpenCode as both orchestrator
and worker. Codex 0.164.0 passed after replacing the broken Homebrew installation
with OpenAI's standalone package; the earlier helper timeout was resolved in the
milestone-four session.

A live conversation recovery scenario exercises exact session resumption after
restart, reset with queued input, and delivery into a new provider session:

```sh
GNAT_LIVE_TEST=1 go test ./internal/v2/daemon -run '^TestLiveConversationRecovery$' -v -timeout 5m
```

`make release VERSION=v2-example` builds the macOS arm64 archive and checksum
under `dist/`, with the version embedded in `gnat --version`. Tag publication
uses the same target; it does not require Docker or install agent profiles.

- [V2 specification](agents/gnat-v2-spec.md): scope and implementation milestones.
- [Architecture](agents/architecture.md): current runtime and legacy boundaries.
- [Setup](docs/setup.md) and [usage](docs/usage.md): installation, controls and recovery.
