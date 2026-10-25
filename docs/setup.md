# Setup

## V2 local swarm (current source build)

On macOS, run `go build -o gnat ./cmd/gnat` or `go install ./cmd/gnat`.
Install and authenticate at least one of Claude Code (`claude auth login`),
Codex (`codex login`), or OpenCode (`opencode auth login`). No
Docker, GitHub, profiles, or Gnat-specific credential setup is needed.
Set the required `orchestrator.harness`, `orchestrator.model`, and
`orchestrator.effort` in `~/.config/gnat/config.yaml` (under
`$XDG_CONFIG_HOME/gnat` when set), as shown in the [README](../README.md).
The browser provides right-sidebar chat with the configured local orchestrator.
Supported harness names are `claude`, `codex`, and `opencode` for both roles.
Missing installation/authentication and incompatible protocols show
actionable errors in the sidebar. Repair the local installation/login and use
Retry without losing the swarm or queued input. Existing swarms retain their
resolved settings.

Gnat uses the normal local harness configuration, credentials, project
instructions, and native delegation. It does not rewrite them. Execution permits
unattended operations with ordinary user privileges (Claude permission bypass,
Codex never-approve/full access, ACP allow_once); the orchestrator works directly
in the original checkout and workers
use separate managed Git worktrees. Gnat injects an agent-specific MCP connection
for each launch without writing global MCP configuration or project files.
Optional `workers` harness/model/effort defaults inherit missing values from the
orchestrator and are saved for each new swarm; existing agents keep their settings.

Codex needs the native app-server initialize/account/model/thread/turn protocol
(protocol checked with codex-cli 0.164.0; see the README for its live tool-use
limitation). OpenCode needs ACP 1 with HTTP MCP, session/resume,
and session config options for model and effort, as documented for OpenCode 2;
compatibility is checked by protocol capabilities rather than the executable's
major version. Models use OpenCode's `provider/model` IDs; select effort from
its variants or use `default` when there is no effort option. ACP has no read-only
authentication-status operation, so selected-provider authentication errors appear
when a prompt runs; repair login and send new direction if input was delivered.
Gnat does not change global configuration or copy provider credentials.

The native binary embeds its chat page and JavaScript/CSS assets. `gnat` starts the daemon
automatically; `gnat daemon stop` interrupts managed turns, waits for subprocess cleanup, and preserves SQLite state. Runtime files are
private under `$XDG_STATE_HOME/gnat/v2/runtime` (default
`~/.local/state/gnat/v2/runtime`); startup failures identify `daemon.log` there.
V1 state is preserved separately and is not migrated. Missing saved checkout
paths are reported in the browser, with no relocation or automatic recreation.

## Historical v1 installation

The remainder describes v1 releases and retained distribution targets, not the
current source entry point. Published releases may still be v1.

## Installing

On a Mac with Apple silicon, the Claude Code plugin installs gnat from the latest [release](https://github.com/kkestell/gnat/releases): `/plugin install gnat --marketplace kkestell/gnat`, then `/gnat:install`. It puts `gnat` in `~/.local/bin`, installs the bundled profiles (keeping any you have), checks Docker and `gh`, and helps save the credentials below. Rerun it to update. The release archive, `gnat-darwin-arm64.tar.gz`, holds the `gnat` binary and the `profiles` directory, with a SHA-256 checksum beside it; pushing a `v*` tag builds it (`make release`) and publishes it.

Elsewhere, or to build from source, `make install` cross-builds the static linux `gnat-container` binaries containers run (amd64 and arm64), embeds them in the `gnat` binary, and installs it with `go install`. Rerun it after pulling.

It also installs the bundled profiles, from the repository's `profiles/agents`
and `profiles/swarms`, into `~/.config/gnat/profiles/agents` and
`~/.config/gnat/profiles/swarms` (under `$XDG_CONFIG_HOME/gnat` when set),
keeping any existing files. Run `make install-profiles` to install just the
profiles.

## Credentials

`gnat auth login HARNESS` saves a harness's credential in the system keyring (the macOS keychain, or the Secret Service through `secret-tool` elsewhere). For Claude and Ox it reads the credential from the terminal without echoing it, or as one line of standard input; Codex opens a browser login. `gnat auth logout HARNESS` removes it.

For Claude Code agents, `claude setup-token` signs you in through the browser and prints a long-lived OAuth token (`sk-ant-oat…`). Copy it, then paste it at the prompt:

```sh
claude setup-token
gnat auth login claude
```

For Ox agents, create an OpenRouter API key and paste it at the prompt of `gnat auth login ox`. Agents hold the key, so give them one of their own rather than the one you use yourself.

For Codex agents, install the [Codex CLI](https://developers.openai.com/codex/cli)
on the host, then run:

```sh
gnat auth login codex
```

Complete ChatGPT sign-in in the browser. gnat uses a private temporary login,
leaving your normal Codex configuration and login alone. It saves only the
access token, account ID, and expiry in its keyring; it discards the refresh
token. One saved credential serves all Codex agents, which share your ChatGPT
subscription allowance. Codex does not fall back to API billing.

Renew manually when the credential expires: rerun `gnat auth login codex`, then
`gnat down SWARM` and `gnat up SWARM` for each running Codex swarm. Sessions
survive that restart. Login prints the actual expiry; token lifetimes can vary.

`GH_TOKEN` is for cloning, pushing, and the pull request commands. Without it, gnat uses `gh auth token`; for another logged-in account, `export GH_TOKEN=$(gh auth token --user NAME)`. Only the control container gets it. Agents clone, fetch, and push through the control plane, with a secret `gnat up` generates for each, so a prompt injection in an agent can't take the token; what it can still do is what the control plane allows an agent: push branches named `SWARM/AGENT/...` to the one repository, never delete or force-push, and use the pull request commands.

`gnat up` needs only the credentials of the harnesses its agents use, and each agent gets only its own harness's.

Agents run with permissions skipped and hold their harness's credential, so anything that can steer an agent, such as a prompt injection in a file it reads, can use that. They don't hold the GitHub token, but the control plane acts on GitHub for them, so still don't give it your everyday token. Use a [fine-grained personal access token](https://github.com/settings/personal-access-tokens/new) limited to the one repository with **Contents** and **Pull requests** read and write, adding **Issues** or **Workflows** only if needed, or a token for a separate bot account.

## Images

`gnat up` builds `gnat-agent` from `internal/cli/Dockerfile`: `claude`, `codex` and its code-mode host (pinned to 0.163.0), `ox-server` (from Ox's main branch on every build), `git`, `python3`, `uv`, build-essential, and common command-line tools, with an `agent` user (UID 1000) that owns `/workspace`.

`gnat create` automatically selects `Dockerfile.gnat` in the current directory when present; otherwise agents use the base `gnat-agent` image. Put repository-specific packages or files in `Dockerfile.gnat`, starting with `FROM gnat-agent` (see this repo's example). Use `--dockerfile FILE` to select a different file. The selected absolute path is saved in the swarm definition, and `gnat up` builds it with its own directory as the context. Containers always start as UID 1000 in `/workspace` with `/opt/gnat/gnat-container` as the entrypoint, so a trailing `USER`, `WORKDIR`, `ENTRYPOINT`, or `CMD` doesn't matter. `gnat up` fails if the image lacks `git` or a harness the swarm uses.

Agents run git as `agent`, which refuses a repository owned by another user ("detected dubious ownership"). If the Dockerfile clones a repository as root, such as a toolchain built from source, add `RUN git config --system --add safe.directory PATH` for it, or `chown` it to 1000.

## Files outside source control

For files agents need that aren't in the repository, such as a `.env`, pass `--file` to `gnat create` or use `gnat file add`:

```sh
gnat create demo --repo OWNER/NAME --file .env --file ~/keys/ci.json:config/ci.json
```

`WORKSPACE_PATH` (after the colon) is relative to `/workspace` and defaults to the file's name. Files are read at `gnat up`, never baked into an image, copied into each clone with mode 0600, and added to `.git/info/exclude`. A path the repository tracks is refused. To change what agents get, edit the host file, then `gnat down` and `gnat up`. Agents can read these files, so use narrowly scoped keys.

## Codex agents

```sh
gnat agent create demo delta --harness codex --instructions "Implement the requested change."
```

Alternatively, use the bundled `codex-coder` profile. Omitted model and effort
use Codex's defaults; `--model` and `--effort` override them, and the selected
model must support the effort. gnat drives Codex's app-server, keeps each
agent's thread on its own volume, and uses its tool hook for `--now` and pause
messages. The external-token login interface is experimental and tied to the
pinned Codex release. Token and context usage appear in status and the web UI;
Codex does not report a dollar cost through this integration.

## Ox agents

```sh
gnat agent create demo gamma --instructions "Be brief." --harness ox --model openrouter:deepseek/deepseek-v4.1-flash
```

`--model` takes an Ox qualified model ID. `--effort` takes `default`, `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`, if the model supports it. gnat drives `ox-server acp` over the Agent Client Protocol, and Ox agents show up in logs, the web UI, and `gnat wait` the same as Claude Code agents.

## Claude Code plugin

This repository is a Claude Code [plugin marketplace](https://code.claude.com/docs/en/plugin-marketplaces) with one plugin, `gnat`, in `plugin/`. It has two skills: `/gnat:install`, which installs gnat and its credentials, and `/gnat:gnat`, which teaches Claude to operate gnat and assumes `gnat` is on PATH. `.claude/skills/gnat` links to the second, so it loads as `/gnat` in this checkout.
