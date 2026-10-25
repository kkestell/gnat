---
name: install
description: Install or update `gnat` on this Mac from its latest GitHub release, check that Docker and GitHub access are ready, and save the Claude Code, Codex, or Ox credentials its agents use. Use when the user wants to install, update, or set up gnat, or when `gnat` isn't on PATH.
---

# Installing gnat

## Source-build v2 status

The current source entry point implements v2 milestones one through three, with a
detached local daemon and right-sidebar chat using one local Claude Code
orchestrator. Install/authenticate Claude normally (`claude auth login`);
Gnat uses local credentials and trusted execution in the original checkout.
Workers run in separate managed worktrees and coordinate through authenticated
MCP tools; the orchestrator creates/removes them and workers can inspect/message
teammates. The browser shows dynamic membership and retained worker history.
Other harness adapters remain milestone four. For a v2
source build, follow the repository README: native `go install ./cmd/gnat`,
required orchestrator settings in `~/.config/gnat/config.yaml`, then `gnat` or
`gnat --no-open` in a committed checkout. Stop it with `gnat daemon stop`.
Do not apply the Docker, profile, or auth instructions below to v2.

The remainder applies to older v1 releases. Published releases may still be v1;
check `gnat --help` to identify the installed command surface before proceeding.


Get the user from nothing to ready to run `gnat up`: the `gnat` binary on PATH, its bundled profiles, Docker running, GitHub access, and a credential for each harness they'll use. Rerunning this updates gnat and skips what's already done. Tell the user what you're doing at each step, briefly.

Never ask the user to paste a token or key into the chat, and never print one: the transcript is logged. Credentials go into the system keyring through `gnat auth login`, from the user's clipboard for Claude and Ox or a browser login for Codex.

## 1. Check the machine

```sh
uname -sm
```

The release is for macOS on Apple silicon (`Darwin arm64`). On anything else, stop and tell the user to build from source: <https://github.com/kkestell/gnat#install>.

```sh
docker info --format '{{.ServerVersion}}'
```

If `docker` isn't found, the user needs Docker Desktop (<https://www.docker.com/products/docker-desktop/>) or another Docker engine such as OrbStack. If it's installed but the daemon doesn't answer, ask them to start it. Don't install or start Docker yourself. Carry on with the other steps either way, and remind them at the end.

## 2. Install the binary and profiles

Download the release into a new temporary directory, check it against its checksum, and install it:

```sh
tmp=$(mktemp -d)
cd "$tmp"
curl -fsSLO https://github.com/kkestell/gnat/releases/latest/download/gnat-darwin-arm64.tar.gz
curl -fsSLO https://github.com/kkestell/gnat/releases/latest/download/gnat-darwin-arm64.tar.gz.sha256
shasum -a 256 -c gnat-darwin-arm64.tar.gz.sha256
tar -xzf gnat-darwin-arm64.tar.gz
mkdir -p ~/.local/bin
install -m 0755 gnat-darwin-arm64/gnat ~/.local/bin/gnat
```

If the checksum doesn't match, stop and tell the user; don't install it.

Then copy the bundled profiles into the config directory without replacing any the user already has (they may have customized them):

```sh
profiles="${XDG_CONFIG_HOME:-$HOME/.config}/gnat/profiles"
for kind in agents swarms; do
  mkdir -p "$profiles/$kind"
  cp -nv gnat-darwin-arm64/profiles/$kind/*.yaml "$profiles/$kind/"
done
```

`cp -nv` prints `not overwritten` and exits 1 for each profile the user already has; that's expected on an update. Tell the user which ones were kept, since their copies don't pick up changes to the bundled ones.

Remove the temporary directory afterwards.

### PATH

Check `command -v gnat` and whether `~/.local/bin` is on PATH (`echo "$PATH" | tr : '\n'`).

- If `~/.local/bin` isn't on PATH, ask the user before adding `export PATH="$HOME/.local/bin:$PATH"` to their shell's startup file (`~/.zshrc` for zsh, the default on macOS). It takes effect in new terminals and new Claude Code sessions, not this one.
- If `command -v gnat` finds a different `gnat` first, such as `~/go/bin/gnat` from `make install` in a checkout, tell the user which one wins and let them decide whether to remove it.

Use `~/.local/bin/gnat` by its full path for the rest of this skill.

## 3. GitHub

gnat reaches GitHub with `GH_TOKEN` from the environment, or else the active `gh` account's token. Only gnat's control container holds it; agents don't. Check:

```sh
command -v gh && gh auth status
```

- If `gh` is missing and Homebrew is installed (`command -v brew`), offer to `brew install gh`. Otherwise point them to <https://cli.github.com/>.
- If `gh` isn't logged in, ask the user to run `gh auth login` in a terminal; it's interactive. Wait for them to say it's done, then check `gh auth status` again.

Tell the user, once, that gnat acts on GitHub with this token for its agents, so a narrower one is safer than their everyday account: a fine-grained personal access token (<https://github.com/settings/personal-access-tokens/new>) for just the swarm's repository, with **Contents** and **Pull requests** read and write, exported as `GH_TOKEN` when they run gnat. It's optional; don't press it.

## 4. Harness credentials

Ask which harnesses the user's agents will run, with AskUserQuestion (multi-select):

- **Claude Code**: uses their Claude subscription, through a long-lived OAuth token.
- **Codex**: uses their ChatGPT subscription, with manual credential renewal.
- **Ox**: runs models through OpenRouter, with an OpenRouter API key.

Check which are already saved; this prints the keychain item's attributes, never the secret, and fails if there's none:

```sh
security find-generic-password -s gnat -a claude >/dev/null 2>&1 && echo saved || echo missing
security find-generic-password -s gnat -a codex >/dev/null 2>&1 && echo saved || echo missing
security find-generic-password -s gnat -a ox >/dev/null 2>&1 && echo saved || echo missing
```

If one the user picked is saved already, ask whether to keep it or replace it.

### Claude Code

Ask the user to run this in a terminal (not through `!` here; it signs in through the browser and is interactive):

```sh
claude setup-token
```

It prints a token starting `sk-ant-oat`. Ask them to copy the whole token and tell you when it's on the clipboard. Then save it without it ever appearing in the transcript, check it looks right first, and clear the clipboard:

```sh
pbpaste | tr -d '[:space:]' | grep -q '^sk-ant-oat' && pbpaste | tr -d '[:space:]' | ~/.local/bin/gnat auth login claude && pbcopy </dev/null
```

If the check fails, nothing was saved: tell the user the clipboard doesn't hold a Claude token (don't show what it does hold) and ask them to copy it again.

### Codex

Codex login needs the Codex CLI installed on the host (`command -v codex`). If
it is missing, direct the user to <https://developers.openai.com/codex/cli>.
Ask the user to run this in a terminal and complete ChatGPT browser sign-in:

```sh
~/.local/bin/gnat auth login codex
```

No credential is copied through chat or the clipboard. gnat saves one access-only
credential for every Codex agent to share and prints its expiry. When it expires,
repeat this command and restart affected swarms with `gnat down` and `gnat up`;
their sessions survive. Codex agents share the account's subscription allowance.

### Ox (OpenRouter)

Ask the user to create a key at <https://openrouter.ai/settings/keys>. Agents hold this key, so suggest one just for gnat, with a credit limit. Ask them to copy it and tell you when it's on the clipboard, then:

```sh
pbpaste | tr -d '[:space:]' | grep -q '^sk-or-' && pbpaste | tr -d '[:space:]' | ~/.local/bin/gnat auth login ox && pbcopy </dev/null
```

As above, if the check fails, nothing was saved; ask them to copy the key again.

## 5. Finish

Run the `security find-generic-password` checks again to confirm each picked harness's credential is saved, and `~/.local/bin/gnat profiles list` to confirm the profiles are there.

Then tell the user, in a few lines:

- What was installed or updated, and anything still missing (Docker not running, `gh` not logged in, a credential not saved).
- If PATH changed, that new terminals and Claude Code sessions pick it up.
- The first `gnat up` builds the agent image, which takes a few minutes.
- To start, they can ask you to run work on a repository with a swarm (the `gnat` skill covers operating it), or run `gnat create NAME --repo OWNER/NAME --profile small` and `gnat up NAME` themselves.
- After an update, a running swarm keeps the old gnat until `gnat down NAME` and `gnat up NAME`.
