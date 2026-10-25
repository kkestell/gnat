# gnat

Gnat v2 is becoming a local application for directing coding agents through one
persistent orchestrator. **Milestone one is implemented:** workspace registration,
SQLite records, a detached per-user daemon, and a browser workspace switcher.
Harness execution, chat, and dynamic workers are upcoming milestones.

## Build and run on macOS

Requires Go 1.26 or newer and Git. Build directly from this checkout:

```sh
go build -o gnat ./cmd/gnat
# Or install the native entry point:
go install ./cmd/gnat
```

Create `~/.config/gnat/config.yaml` (or `$XDG_CONFIG_HOME/gnat/config.yaml`):

```yaml
orchestrator:
  harness: claude
  model: your-model-id
  effort: high
```

All three values are required for a new swarm. Harness choices are `claude`,
`codex`, and `opencode`; this milestone records settings without launching or
validating an installed harness. Reopening a swarm uses its saved settings.

In any local Git checkout with an initial commit:

```sh
cd ~/src/my-project
gnat                 # Print the URL and open the default browser.
gnat --no-open       # Print the same workspace URL without opening a browser.
gnat daemon stop     # Stop the daemon; keep saved swarms.
```

Subdirectories and symlink aliases select the same swarm. Separate user worktrees
have separate swarms served by the same daemon. The browser shows saved checkout
metadata and a pending orchestrator; it cannot accept prompts yet. Docker,
GitHub, a remote, and harness credentials are unnecessary for this milestone.

State lives under `~/.local/state/gnat/v2` (`$XDG_STATE_HOME/gnat/v2` when set),
separate from v1 state. There is no automatic v1 migration. The former v1 CLI is
not exposed by the new entry point; its packages and distribution targets remain
in the repository until later v2 cleanup. Published releases may still be v1;
use the direct source build above for this foundation.

## Development and documentation

`go test ./internal/v2/...` covers the foundation, including real detached
subprocesses on macOS. `make test` and `make lint` include retained v1 code and UI.
The old `make build`, `make install`, and release targets still build container
helpers; direct native Go builds avoid those obsolete prerequisites.

- [V2 specification](agents/gnat-v2-spec.md): scope and implementation milestones.
- [Architecture](agents/architecture.md): current foundation and legacy boundaries.
- [Setup](docs/setup.md) and [usage](docs/usage.md): v2 entry and historical v1 reference.
