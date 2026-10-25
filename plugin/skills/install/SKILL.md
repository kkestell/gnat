---
name: install
description: Install or update the native Gnat v2 application on macOS, verify its command surface, and explain required orchestrator configuration and normal harness login. Use when the user wants to install, update, or set up Gnat, or when gnat is missing from PATH.
---

# Installing Gnat

Gnat v2 ships one native executable with embedded browser assets. It requires
Git and an authenticated Claude Code, Codex, or OpenCode installation. It uses
normal local credentials; do not copy tokens, install profiles, set up Docker,
or require GitHub access for runtime work.

## Check and install

Run `uname -sm`. Release archives support `Darwin arm64`; other macOS
architectures use a source build. Linux/Windows are outside initial v2 support.
Check `command -v gnat` and existing `gnat --help`/`--version` before updating.
Stop an existing v2 daemon with that installed binary's `gnat daemon stop`
before replacing it; saved work is retained.

Download the native release into a new temporary directory:

```sh
task_tmp=$(mktemp -d)
cd "$task_tmp"
curl -fsSLO https://github.com/kkestell/gnat/releases/latest/download/gnat-darwin-arm64.tar.gz
curl -fsSLO https://github.com/kkestell/gnat/releases/latest/download/gnat-darwin-arm64.tar.gz.sha256
shasum -a 256 -c gnat-darwin-arm64.tar.gz.sha256
```

Stop if the download/checksum fails. Otherwise extract and inspect before
installing:

```sh
tar -xzf gnat-darwin-arm64.tar.gz
gnat-darwin-arm64/gnat --help
gnat-darwin-arm64/gnat --version
```

Older published releases may still be v1. Help must expose `--no-open` and
`daemon`, rather than `create` and `up`. If the downloaded release is v1, do not
install it as v2; use a source checkout and Go 1.26 or newer:

```sh
make install
```

Source installs use Go's configured binary directory. A verified v2 release
installs with:

```sh
mkdir -p ~/.local/bin
install -m 0755 gnat-darwin-arm64/gnat ~/.local/bin/gnat
```

Remove the temporary download directory when finished. Check which binary wins
with `command -v gnat`; explain conflicting installations instead of deleting
them. Ask before editing shell startup files to add the install directory to
PATH. Verify the installed binary with `--help` and `--version`.

## Configure and finish

Use the selected harness's existing installation/login. If needed, ask the user
to authenticate in their terminal with `claude auth login`, `codex login`, or
`opencode auth login`. Never request or print secret values. No Gnat credential
store is involved.

Explain the three required settings for a new swarm in
`~/.config/gnat/config.yaml` (under `$XDG_CONFIG_HOME/gnat` when set):

```yaml
orchestrator:
  harness: claude
  model: sonnet
  effort: high
```

Use the user's chosen harness, model and effort; do not replace existing
configuration without authorization. Valid harness names are `claude`, `codex`,
and `opencode`. Model/effort must fit that provider. Optional `workers` defaults
inherit omitted settings. Existing swarms retain resolved settings.

Report the installed path/version and anything still required. To start, run
`gnat` from a committed local checkout, or `gnat --no-open` to print its URL.
The browser's orchestrator sidebar is the normal entry point for directing
workers. `gnat daemon stop` preserves work; restart with `gnat` after updates to
obtain the new local URL. Refer to `docs/setup.md` for recovery and compatibility
notes. Do not create a swarm or send agent work unless the user requested it.
