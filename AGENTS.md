# Gnat

Gnat is a macOS application for directing local coding agents through one
persistent orchestrator. `agents/gnat-spec.md` defines the product contract;
`agents/architecture.md` records the implementation boundaries. Read the
architecture before moving a boundary, and update it when you do.

Kyle is the only user. Favor small implementations that are easy to understand
and change over generalized infrastructure or speculative requirements. Known
gaps can be addressed in follow-up work. Pressure-test the ordinary workflow and
internal consistency without reopening accepted limitations as release blockers.

## Runtime and product boundaries

The public command surface is `gnat`, `gnat --no-open`, `gnat --help`,
`gnat --version`, and `gnat daemon stop`. A detached per-user daemon serves all
registered workspaces. The orchestrator uses the original checkout; workers use
managed Git worktrees. Claude Code, Codex, and OpenCode can run either role,
including mixed teams. Agents integrate commits using ordinary local Git.

Gnat supplies identities, worktrees, conversations, messages, and lifecycle tools.
The orchestrator is the user's entry point and manages worker membership. Agents
decide how to divide work, collaborate, integrate, and assess completion. Do not
add a task registry, rigid workflow, fixed topology, required reporting ceremony,
or mandatory review/integration sequence. Built-in instructions explain runtime
facts, not how agents must organize their work.

- macOS only, trusted local processes and ordinary user credentials.
- Harness, model and effort are required orchestrator config values; missing
  values are errors, not a setup wizard.
- Leave native harness delegation unchanged. Agents handle development setup
  using ordinary tools and repository instructions.
- Stop/resume preserves the team and work. Worker removal uses ordinary Git
  safeguards and preserves message history.
- After a daemon crash, users may need to clean up surviving processes.
  Automatic process cleanup/reconciliation is outside current scope.
- Workspace relocation and unregistering are deferred. Git inspection UI remains
  a follow-up; retain checkout metadata and independent main/sidebar UI boundaries.

## Packages

The sole executable is `cmd/gnat`. Packages under `internal/`:

- `entry`: workspace discovery, daemon startup and the public commands.
- `daemon`: process lifetime, local HTTP API, embedded JavaScript/CSS UI, SSE.
- `core`: neutral scheduling, controls, agent membership and coordination.
- `store`: SQLite records, schema validation, durable events and configuration.
- `workspace`: checkout discovery and worker branch/worktree management.
- `harness`: Claude, Codex app-server and OpenCode ACP adapters.
- `mcp`: authenticated, agent-bound coordination tools over loopback HTTP.

Core code must not depend on concrete harness adapters. Translate provider events
before they reach the core or browser. SQLite is `modernc.org/sqlite` (pure Go).
State defaults to `~/.local/state/gnat`, config to `~/.config/gnat/config.yaml`;
`XDG_STATE_HOME` and `XDG_CONFIG_HOME` override their parent directories.

## Commands and validation

```sh
make build                     # Native binary with embedded browser assets.
make install                   # go install ./cmd/gnat
make release VERSION=v0.0.0-example # macOS arm64 archive and checksum.
make test                      # Go packages and browser tests.
make lint                      # go vet, staticcheck and gofmt check.
```

Before pushing, all of this must pass:

```sh
go build ./... && go vet ./... && staticcheck ./... && test -z "$(gofmt -l .)" && go test ./...
```

After changing browser code, also run `bun test internal/daemon/page.test.js`.
Assets are embedded from `internal/daemon`; no asset generation is required.
Check plugin changes with `claude plugin validate .`. The Claude Code plugin is
`plugin/`, listed by `.claude-plugin/marketplace.json`; `.claude/skills/gnat`
links to `plugin/skills/gnat`.

Detached subprocess tests run on macOS and cover shutdown, crash recovery,
queues and independent swarms. Opt in to provider recovery coverage with
`GNAT_LIVE_TEST=1 go test ./internal/daemon -run '^TestLiveConversationRecovery$' -v -timeout 5m`.
Other live scenarios are documented in README. Use disposable local repositories
and isolated state/config/install directories for smoke checks. After daemon
restart, reopen with `gnat` to obtain the new listener URL.
