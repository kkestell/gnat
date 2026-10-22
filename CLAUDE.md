# CLAUDE.md

gnat is a Go port of `swarm` (Python): run a swarm of coding agents, each in its own Docker container, on separate clones of one GitHub repository. Behavior, commands, flags, wire formats, and on-disk formats match the Python version except that everything named `swarm` that identifies the tool is `gnat` (binary, images, labels, `$XDG_STATE_HOME/gnat`, `GNAT_AGENT`, `/opt/gnat`), and harness credentials live in the system keyring (`gnat auth login`), not the environment or a `.env`. "Swarm" stays as the noun for a set of agents.

## Commands

```sh
make linux     # cross-build the static linux gnat-container binaries into internal/workspace/bin
make build     # make linux, then go build -o gnat ./cmd/gnat
make install   # make linux, then go install ./cmd/gnat (embeds the gnat-container binaries)
make ui        # build the web UI (ui/) into internal/web/dist; commit the result
make test      # go test ./..., bun test
make lint      # go vet, staticcheck, gofmt check, tsc
```

Before pushing, all of this must pass:

```sh
go build ./... && go vet ./... && staticcheck ./... && test -z "$(gofmt -l .)" && go test ./...
```

After changing `ui/`, also `make ui`, `cd ui && bun test && bun run typecheck`, and commit `internal/web/dist`.

Containers run a separate binary, `gnat-container` (`cmd/gnat-container`, a static linux build at `/opt/gnat/gnat-container`, embedded in `gnat` and copied in by `gnat up`); what ran as Python modules there are its subcommands, `gnat-container control|agent|hook|send` (`layout.Command`). The two binaries share no code beyond the small packages below: `gnat` must not link `control`, `agent`, or `hook`, and `gnat-container` must not link `cli`, `keyring`, or `workspace`. Check with `go list -deps ./cmd/gnat` and `go list -deps ./cmd/gnat-container`. The host reads `control.db` through `recorded`, never `control`. SQLite is `modernc.org/sqlite` (pure Go).

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
- `control`: the control plane (mailboxes, status, usage, logs, `control.db`), the GitHub requests agents make with their secrets, and the git proxy (`git.go`: a bare mirror served by `git http-backend`, with a pre-receive hook that enforces the push rules and pushes on to GitHub).
- `web`: serves the web UI (embedded from `internal/web/dist`), `/agents`, and the `/events` stream.
- `harness`: the programs an agent runs turns with (Claude, ACP, Ox).
- `agent`: the agent daemon, the container's main process.
- `hook`: the PostToolUse hook that delivers `--now` messages, and `gnat-container send`.
- `workspace`: cloning, files, instructions, and archives for containers.
- `keyring`: harness credentials in the system keyring (macOS `security`, `secret-tool` elsewhere).
- `cli`: the host's command line (`cli.Run`).

The web UI is `ui/`, a Bun + React + Tailwind app: `swarm.ts` turns events into each agent's transcript, usage, and latest entries; `markdown.js` is a hand-written, DOM-building Markdown renderer (no HTML parsing, http(s)/mailto links only). `bun dev` in `ui/` runs it with hot reloading on http://localhost:5180 against a pretend swarm (`ui/mock/swarm.ts`); with `GNAT_URL=$(gnat web SWARM --no-open)`, against a running one.

Go that writes JSON Python wrote with `json.dumps` uses an `Encoder` with `SetEscapeHTML(false)` (`protocol.Encode`).
