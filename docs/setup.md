# Setup

Gnat supports macOS. Install Git and at least one supported harness, and
complete its normal login in a terminal: `claude auth login`, `codex login`, or
`opencode auth login`. Gnat uses your existing credentials and configuration.

## Install from source

With Go 1.26 or newer, from this checkout:

```sh
make install
# Equivalent: go install ./cmd/gnat
```

Put Go's binary directory (`go env GOBIN`, or `$(go env GOPATH)/bin` if unset) on
PATH. `make build` creates `./gnat` instead. The native binary embeds its browser
assets. GitHub access and Gnat-specific credential setup are unnecessary. Bun is
used for browser development tests.

## Install a release

Release archives target Apple-silicon macOS. From a new temporary directory,
download the archive and checksum from the same
[release](https://github.com/kkestell/gnat/releases):

```sh
curl -fsSLO https://github.com/kkestell/gnat/releases/latest/download/gnat-darwin-arm64.tar.gz
curl -fsSLO https://github.com/kkestell/gnat/releases/latest/download/gnat-darwin-arm64.tar.gz.sha256
shasum -a 256 -c gnat-darwin-arm64.tar.gz.sha256
```

Continue only after the checksum passes:

```sh
tar -xzf gnat-darwin-arm64.tar.gz
gnat-darwin-arm64/gnat --help
```

The archive contains only the native executable. Install it with:

```sh
mkdir -p ~/.local/bin
install -m 0755 gnat-darwin-arm64/gnat ~/.local/bin/gnat
```

Add `~/.local/bin` to PATH if needed. `command -v gnat` shows which installation
wins when both source and release copies exist. The Claude Code plugin's
`/gnat:install` skill follows this same native installation path.

Before updating, stop the old daemon with `gnat daemon stop`; it preserves the
team, queues, worktrees and sessions. Install the replacement, then run `gnat`
from a saved checkout. A running daemon keeps using its old executable until
stopped.

## Configuration

Create `~/.config/gnat/config.yaml` (or `$XDG_CONFIG_HOME/gnat/config.yaml`):

```yaml
orchestrator:
  harness: claude
  model: sonnet
  effort: high
workers:
  model: sonnet
  effort: high
```

All three orchestrator settings are required for a new swarm; missing values
produce an error. `workers` is optional and inherits omitted values from the
orchestrator. Swarms and agents retain the settings resolved at creation;
editing the file affects new swarms. Worker creation can override settings.

Supported harness names are `claude`, `codex`, and `opencode`. Model and effort
must fit the selected harness. Codex validates its account's advertised models
and efforts. OpenCode uses `provider/model` IDs and ACP config variants; use
`default` effort for a model with no variant. Changing harness typically requires
changing model and effort too.

Codex uses its native app-server protocol, with live MCP checks passing for
codex-cli 0.164.0 in both agent roles. An earlier Homebrew helper timeout was
resolved by replacing that installation with OpenAI's standalone package.
OpenCode requires ACP 1 with HTTP MCP, session/resume,
and model/effort config options, checked through protocol capabilities. ACP has
no read-only authentication-status operation, so selected-provider authentication
errors appear when a prompt runs.

## First run and recovery

In a local Git checkout with an initial commit, run `gnat`, or `gnat --no-open`
to print the URL. The daemon starts automatically. Direct work in the right
sidebar; [usage](usage.md) explains workers, integration and controls.

Gnat runs trusted local processes with your normal privileges: Claude permission
bypass, Codex never-approve/full access, and ACP allow_once permissions. The
orchestrator edits the original checkout; workers use managed worktrees. Normal
project instructions and native harness delegation remain available. Per-launch
MCP configuration does not rewrite global configuration or project files.

Missing harnesses, login problems and incompatible protocols appear in the
sidebar. Repair the installation or login, then choose Retry. Delivered input
is not replayed: send new direction if a failed turn already consumed it. Failed
exact session resumption retains history; New conversation explicitly starts
fresh while keeping the team and queue.

State is under `$XDG_STATE_HOME/gnat`, defaulting to
`~/.local/state/gnat`. Runtime diagnostics are in `runtime/daemon.log` there;
the SQLite database, provider-owned sessions and worker worktrees together hold
the saved work. Do not edit the database or delete state to repair a login error.
Missing saved checkout paths are reported without relocation or recreation.
After a daemon crash, clean up surviving agent/tool processes using ordinary
process tools before resuming. Reopen with `gnat` to obtain the new listener URL.
