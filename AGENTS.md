# CLAUDE.md

## V2 design priorities

`agents/gnat-v2-spec.md` describes the first cut of v2. Kyle is its only user.
Favor a small implementation that is easy to understand and change over
operational robustness, generalized infrastructure, or speculative future
requirements. Known gaps are acceptable and can be addressed in follow-up work.
Pressure-test the ordinary workflow and internal consistency; do not repeatedly
reopen accepted limitations as release blockers without new evidence or a user
request.

V2 supplies basic building blocks for agents to organize their own work:
identities, worktrees, conversations, messages, and worker lifecycle tools.
The orchestrator is the user's entry point and manages worker membership, but
agents decide how to divide work, collaborate, integrate, and assess completion.
Do not encode a rigid workflow, task registry, required reporting ceremony,
fixed team topology, or mandatory review/integration sequence in Gnat.
Built-in agent instructions explain runtime facts: available tools, where
output goes, and how incoming messages start turns. They do not prescribe how
agents organize their work.

Accepted first-cut scope:

- macOS only, with local trusted processes and ordinary user credentials.
- Orchestrator harness, model, and effort come from required config values;
  missing values are errors, not a setup wizard.
- Native harness delegation stays unchanged. Agents handle development setup
  using ordinary tools and repository instructions.
- Stop/resume preserves the team and work. Explicit worker removal cleans up
  its resources using ordinary Git safeguards and preserves message history.
- After a daemon crash, the user may need to clean up surviving processes.
  Automatic process cleanup/reconciliation is not a first-cut requirement.
- Workspace relocation and unregistering are deferred. Git inspection UI is a
  planned follow-up; keep checkout metadata and UI boundaries suitable for it
  without implementing speculative abstractions now.

The default `cmd/gnat` entry point implements v2 milestones one through four:
local workspace registration, versioned SQLite records, a detached daemon, and
right-sidebar chat with a persistent local orchestrator. Claude Code, Codex, and
OpenCode can each run the orchestrator or workers, including mixed teams. Input,
transcripts, and exact sessions persist; controls handle interruption and restart.
Workers use separate worktrees and coordinate over authenticated MCP. New code
lives under `internal/v2/`; build it directly with `go build -o gnat ./cmd/gnat`.

The v1 packages, container entry point, and distribution targets remain until
later milestones. The documentation below describes that legacy implementation,
not constraints to carry into v2. In particular, Docker, Python compatibility,
profiles, GitHub proxies, and v1's optional orchestrator are superseded by the v2
specification.

## Current v1 implementation

gnat is a Go port of `swarm` (Python): run a swarm of coding agents, each in its own Docker container, on separate clones of one GitHub repository. Behavior, commands, flags, wire formats, and on-disk formats match the Python version except that everything named `swarm` that identifies the tool is `gnat` (binary, images, labels, `$XDG_STATE_HOME/gnat`, `GNAT_AGENT`, `/opt/gnat`), and harness credentials live in the system keyring (`gnat auth login`), not the environment or a `.env`. "Swarm" stays as the noun for a set of agents.

`agents/architecture.md` is the shape of the system, the lines drawn between its parts, and why: where state lives, how agents are identified, and how GitHub is reached. Read it before a change that moves one of those lines, and update it when you do.

## Commands

```sh
make linux     # cross-build the static linux gnat-container binaries into internal/workspace/bin
make build     # make linux, then go build -o gnat ./cmd/gnat
make install   # make linux, then go install ./cmd/gnat (embeds the gnat-container binaries)
make ui        # build the web UI (ui/) into internal/web/dist; commit the result
make release   # make linux, then the macOS arm64 release archive and checksum in dist/
make test      # go test ./..., bun test
make lint      # go vet, staticcheck, gofmt check, tsc
```

Before pushing, all of this must pass:

```sh
go build ./... && go vet ./... && staticcheck ./... && test -z "$(gofmt -l .)" && go test ./...
```

After changing `ui/`, also `make ui`, `cd ui && bun test && bun run typecheck`, and commit `internal/web/dist`.

Containers run a separate binary, `gnat-container` (`cmd/gnat-container`, a static linux build at `/opt/gnat/gnat-container`, embedded in `gnat` and copied in by `gnat up`); what ran as Python modules there are its subcommands, `gnat-container control|agent|hook|send` (`layout.Command`). For the retained v1 packages, the two binaries share no code beyond the small packages below: `gnat` must not link `control`, `agent`, or `hook`, and `gnat-container` must not link `cli`, `keyring`, or `workspace`. Check with `go list -deps ./cmd/gnat` and `go list -deps ./cmd/gnat-container`. The host reads `control.db` through `recorded`, never `control`. SQLite is `modernc.org/sqlite` (pure Go).

## Threat model

Agents are cooperative, not adversarial. The goal is that an agent's mistake, or instructions it picks up from something it reads, can't do broad damage: no GitHub credential in agent containers, only the swarm's repository, only the agent's own branches, merging only when granted, and no glaring holes. Defending against an agent that deliberately attacks the control plane or its teammates over the swarm network is out of scope: no web UI authentication, no per-agent networks, no encryption on the bridge. When a simpler option meets the goal, take it; don't add hardening beyond this without asking.

gnat also doesn't assume a team structure. A swarm needn't have an orchestrator or a reviewer; it may be all coders driven by an operator from a terminal. What gnat tells every agent is facts (commands, rules, what's refused); roles and workflow belong in profiles.

## Testing with a live swarm

Use `kkestell/gnat-sandbox`, a private throwaway repository with a tiny Python library (`wordtools.py`, tests via `python3 -m unittest`), for end-to-end runs: agents may push, open pull requests, and merge there. The active `gh` account may not be `kkestell`, so run gnat with `GH_TOKEN=$(gh auth token --user kkestell)`. Close leftover pull requests and delete their branches before a fresh run, and `gnat delete` the swarm afterwards.

## Packages

Two binaries, `cmd/gnat` (the host's commands) and `cmd/gnat-container` (what runs in containers); everything else is under `internal/`.

- `events`: the event format every harness is translated into (`Event` maps, constructors, `UsageTotals`).
- `protocol`: wire types (`Request`, `Response`, `Line`) and the one-shot request client (`Call`).
- `state`: swarm definitions as JSON under `$XDG_STATE_HOME/gnat/swarms`; names and workspace paths.
- `render`: events to readable log lines, and `UsageSummary`.
- `layout`: container paths and identities (workspace, home, UID, binary, files dir, log dir, ports) and `Command`; also `wait`'s timeout status and the hook's timeout.
- `recorded`: reads `control.db`, which `control` writes and the host reads for `gnat status` while a swarm is down.
- `control`: the control plane (mailboxes, status, usage, logs, `control.db`), the GitHub requests agents make with their secrets, every agent request, which must carry the agent's secret (`secrets.go`: who is asking is whoever's secret it is, never what the request says; the operator's commands come in over a Unix socket in the container instead), and the git proxy (`git.go`: a bare mirror served by `git http-backend`, with a pre-receive hook that enforces the push rules and pushes on to GitHub).
- `web`: serves the web UI (embedded from `internal/web/dist`), `/agents`, and the `/events` stream.
- `harness`: the programs an agent runs turns with (Claude, Codex, ACP, Ox).
- `agent`: the agent daemon, the container's main process.
- `hook`: the PostToolUse hook that delivers `--now` messages, and `gnat-container send`.
- `workspace`: cloning, files, instructions, and archives for containers.
- `keyring`: harness credentials in the system keyring (macOS `security`, `secret-tool` elsewhere).
- `cli`: the host's command line (`cli.Run`).

The web UI is `ui/`, a Bun + React + Tailwind app: `swarm.ts` turns events into each agent's transcript, usage, and latest entries; `markdown.js` is a hand-written, DOM-building Markdown renderer (no HTML parsing, http(s)/mailto links only). `bun dev` in `ui/` runs it with hot reloading on http://localhost:5180 against a pretend swarm (`ui/mock/swarm.ts`); with `GNAT_URL=$(gnat web SWARM --no-open)`, against a running one.

The Claude Code plugin is `plugin/` (skills `install` and `gnat`), listed by the marketplace in `.claude-plugin/marketplace.json`; `.claude/skills/gnat` links to `plugin/skills/gnat`. Pushing a `v*` tag runs `.github/workflows/release.yml`, which publishes `make release`'s archive, the one `/gnat:install` downloads. Check the plugin with `claude plugin validate .`.

Go that writes JSON Python wrote with `json.dumps` uses an `Encoder` with `SetEscapeHTML(false)` (`protocol.Encode`).
