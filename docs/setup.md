# Setup

## Building

`make install` cross-builds the static linux `gnat` binaries containers run (amd64 and arm64), embeds them in the host binary, and installs it with `go install`. Rerun it after pulling.

It also installs the bundled `coder`, `reviewer`, and `researcher` profiles
into `~/.config/gnat/profiles` (`$XDG_CONFIG_HOME/gnat/profiles` when set),
keeping any existing files. Run `make install-profiles` to install just the
profiles.

## Credentials

`gnat auth login HARNESS` saves a harness's credential in the system keyring (the macOS keychain, or the Secret Service through `secret-tool` elsewhere). It reads the credential from the terminal without echoing it, or as one line of standard input. `gnat auth logout HARNESS` removes it.

For Claude Code agents, `claude setup-token` signs you in through the browser and prints a long-lived OAuth token (`sk-ant-oat…`). Copy it, then paste it at the prompt:

```sh
claude setup-token
gnat auth login claude
```

For Ox agents, create an OpenRouter API key and paste it at the prompt of `gnat auth login ox`. Agents hold the key, so give them one of their own rather than the one you use yourself.

`GH_TOKEN` is for cloning, pushing, and opening pull requests. Without it, gnat uses `gh auth token`; for another logged-in account, `export GH_TOKEN=$(gh auth token --user NAME)`.

`gnat up` needs only the credentials of the harnesses its agents use, and each agent gets only its own harness's.

Agents run with permissions skipped and hold these credentials, so anything that can steer an agent, such as a prompt injection in a file it reads, can use them. Don't give agents your everyday GitHub token. Use a [fine-grained personal access token](https://github.com/settings/personal-access-tokens/new) limited to the one repository with **Contents** and **Pull requests** read and write, adding **Issues** or **Workflows** only if needed, or a token for a separate bot account.

## Images

`gnat up` builds `gnat-agent` from `internal/cli/Dockerfile`: `claude`, `ox-server` (from Ox's main branch on every build), `git`, `gh`, `python3`, `uv`, build-essential, and common command-line tools, with an `agent` user (UID 1000) that owns `/workspace`.

To add packages or files, pass `gnat create --dockerfile FILE`, where `FILE` starts `FROM gnat-agent` (see `Dockerfile` in this repo). It is built with its own directory as the context, usually a checkout of the swarm's repository. Containers always start as UID 1000 in `/workspace` with `/opt/gnat/gnat` as the entrypoint, so a trailing `USER`, `WORKDIR`, `ENTRYPOINT`, or `CMD` doesn't matter. `gnat up` fails if the image lacks `git`, `gh`, or a harness the swarm uses.

## Files outside source control

For files agents need that aren't in the repository, such as a `.env`, pass `--file` to `gnat create` or use `gnat file add`:

```sh
gnat create demo --repo OWNER/NAME --file .env --file ~/keys/ci.json:config/ci.json
```

`WORKSPACE_PATH` (after the colon) is relative to `/workspace` and defaults to the file's name. Files are read at `gnat up`, never baked into an image, copied into each clone with mode 0600, and added to `.git/info/exclude`. A path the repository tracks is refused. To change what agents get, edit the host file, then `gnat down` and `gnat up`. Agents can read these files, so use narrowly scoped keys.

## Ox agents

```sh
gnat agent create demo gamma --instructions "Be brief." --harness ox --model openrouter:deepseek/deepseek-v4.1-flash
```

`--model` takes an Ox qualified model ID. `--effort` takes `default`, `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`, if the model supports it. gnat drives `ox-server acp` over the Agent Client Protocol, and Ox agents show up in logs, the web UI, and `gnat wait` the same as Claude Code agents.

## Claude Code skill

`.claude/skills/gnat` is a [Claude Code skill](https://code.claude.com/docs/en/skills) that teaches Claude to operate gnat. It loads as `/gnat` in this checkout; to use it elsewhere, copy or symlink it into another project's `.claude/skills/` or `~/.claude/skills/`. It assumes `gnat` is on PATH.
