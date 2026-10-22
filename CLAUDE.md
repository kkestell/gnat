# CLAUDE.md

gnat is a Go port of `swarm` (Python): run a swarm of coding agents, each in its own Docker container, on separate clones of one GitHub repository. Behavior, commands, flags, wire formats, and on-disk formats match the Python version except that everything named `swarm` that identifies the tool is `gnat` (binary, images, labels, `$XDG_STATE_HOME/gnat`, `GNAT_AGENT`, `/opt/gnat`), and harness credentials live in the system keyring (`gnat auth login`), not the environment or a `.env`. "Swarm" stays as the noun for a set of agents.

## Commands

```sh
make build     # go build -o gnat ./cmd/gnat
make install   # go install ./cmd/gnat (will embed the linux container binaries)
make ui        # build the web UI (ui/) into internal/web/dist; commit the result
make test      # go test ./..., bun test
make lint      # go vet, staticcheck, gofmt check, tsc
```

Before pushing, all of this must pass:

```sh
go build ./... && go vet ./... && staticcheck ./... && test -z "$(gofmt -l .)" && go test ./...
```

After changing `ui/`, also `make ui`, `cd ui && bun test && bun run typecheck`, and commit `internal/web/dist`.

Containers run the same `gnat` binary (static linux build at `/opt/gnat/gnat`); what ran as Python modules there are hidden subcommands, `gnat internal control|agent|hook|send` (`layout.Command`). SQLite is `modernc.org/sqlite` (pure Go).

## Packages

One binary, `cmd/gnat`; everything else is under `internal/`.

- `events`: the event format every harness is translated into (`Event` maps, constructors, `UsageTotals`).
- `protocol`: wire types (`Request`, `Response`, `Line`) and the one-shot request client (`Call`).
- `state`: swarm definitions as JSON under `$XDG_STATE_HOME/gnat/swarms`; names and workspace paths.
- `render`: events to readable log lines, and `UsageSummary`.
- `layout`: container paths and identities (workspace, home, UID, binary, files dir) and `Command`.
- `control`: the control plane (mailboxes, status, usage, logs, `control.db`).
- `web`: serves the web UI (embedded from `internal/web/dist`), `/agents`, and the `/events` stream.
- `harness`: the programs an agent runs turns with (Claude, ACP, Ox).
- `agent`: the agent daemon, the container's main process.
- `hook`: the PostToolUse hook that delivers `--now` messages.
- `workspace`: cloning, files, instructions, and archives for containers.
- `keyring`: harness credentials in the system keyring (macOS `security`, `secret-tool` elsewhere).
- `cli`: the command line, host commands and hidden subcommands (`cli.Run`).

The web UI is `ui/`, a Bun + React + Tailwind app: `swarm.ts` turns events into each agent's transcript, usage, and latest entries; `markdown.js` is a hand-written, DOM-building Markdown renderer (no HTML parsing, http(s)/mailto links only). `bun dev` in `ui/` runs it with hot reloading on http://localhost:5180 against a pretend swarm (`ui/mock/swarm.ts`); with `GNAT_URL=$(gnat web SWARM --no-open)`, against a running one.

Go that writes JSON Python wrote with `json.dumps` uses an `Encoder` with `SetEscapeHTML(false)` (`protocol.Encode`).
