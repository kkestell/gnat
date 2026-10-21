# gnat

Run a swarm of coding agents, each in its own Docker container. Each agent runs [Claude Code](https://code.claude.com) or [Ox](https://github.com/kkestell/ox), and a swarm can mix the two.

## Install

Requires Go 1.26, Docker, and the [GitHub CLI](https://cli.github.com/).

```sh
make install
```

This cross-builds the static linux `gnat` binaries containers run (amd64 and arm64), embeds them in the host binary, and runs `go install ./cmd/gnat`; rerun it after pulling to update `gnat`. `make build` writes `./gnat` in the checkout instead. `make test` runs `go test ./...`; `make lint` runs `go vet`, `staticcheck`, and a `gofmt` check.

For Claude Code agents, get an OAuth token with `claude setup-token`, then set `CLAUDE_CODE_OAUTH_TOKEN` in the environment or in gnat's own `.env`, `~/.config/gnat/.env` (or `$XDG_CONFIG_HOME/gnat/.env`). For Ox agents, set `OPENROUTER_API_KEY` the same way. `gnat up` needs only the credentials of the harnesses its agents use, and each agent gets only its own harness's.

Agents clone, push, and open pull requests with `GH_TOKEN` from the environment or gnat's `.env`, or else `gh auth token` (the active `gh` account). To use another logged-in account, `export GH_TOKEN=$(gh auth token --user NAME)`.

Agents run with permissions skipped and hold this token, so anything that can steer an agent (a prompt injection in a file or web page it reads, say) can use everything the token can. Don't give them your everyday token. Use a [fine-grained personal access token](https://github.com/settings/personal-access-tokens/new) limited to the one repository, with repository permissions **Contents: read and write** (clone and push) and **Pull requests: read and write** (open and update pull requests); Metadata: read is added automatically. Add **Issues** or **Workflows** only if the agents need them. Better still, create a separate bot account, invite it to the repository with the least access that works, and make the token that account's. Set it as `GH_TOKEN`, which takes precedence over `gh auth token`. Agents' instructions also tell them never to print or send credentials from their environment, but that is a request, not a barrier: the token's scope is the limit. The same goes for `CLAUDE_CODE_OAUTH_TOKEN` and `OPENROUTER_API_KEY`, which agents also hold.

`gnat up` builds a base agent image, `gnat-agent`, from the Dockerfile in `internal/cli/Dockerfile`: `claude`, `ox-server` (built from Ox's main branch, fetched again on every build), `python3`, `git`, `gh`, the `agent` user (UID 1000) that owns `/workspace`, plus build-essential, [uv](https://docs.astral.sh/uv/), and common command-line tools: `curl`, `wget`, `rg`, `fd`, `jq`, `sqlite3`, `tree`, `file`, `less`, `ps`/`pgrep`/`pkill` (procps), `killall` (psmisc), `lsof`, `nc`, `zip`, `unzip`, and `xz`. Each agent's instructions list them. Agents run on it by default. To add packages or files, give `gnat create` a Dockerfile that starts `FROM gnat-agent` (see `Dockerfile` in this repo); `gnat up` builds the base first, then yours with the Dockerfile's directory as the build context, which is usually a checkout of the repository the swarm works on. Agent containers are started as user 1000 in `/workspace` with the `gnat` binary (`/opt/gnat/gnat`) as the entrypoint, so a Dockerfile that ends with `USER root`, another `WORKDIR`, `ENTRYPOINT`, or `CMD` still works. If the built image lacks `git`, `gh`, or the program of a harness the swarm uses (`claude`, `ox-server`), `gnat up` fails and names what's missing. The control image, `gnat-control`, is `debian:bookworm-slim`; `gnat up` copies the matching linux `gnat` binary into the control and every agent container, so containers need no Python of gnat's own and the image's `python3` and `uv` are there for the repository's own use.

## Claude Code skill

`.claude/skills/gnat/SKILL.md` is a [Claude Code skill](https://code.claude.com/docs/en/skills) that teaches Claude to operate gnat: prerequisites, creating a swarm and agents, dividing work, sending, watching, stopping, and tearing down. It is picked up as `/gnat` when Claude Code runs in this checkout. To use it from other projects, copy or symlink `.claude/skills/gnat` into their `.claude/skills/` or into `~/.claude/skills/`. It assumes `gnat` is on PATH (`make install`) and must be kept in step with the CLI.

## Usage

```sh
gnat create demo [--dockerfile Dockerfile] --repo OWNER/NAME [--file .env]
gnat agent create demo alpha --instructions "You review code." [--harness claude|ox] [--model MODEL] [--effort LEVEL]
gnat up demo
gnat send demo alpha -m "Look at the README"
gnat send demo -m "Pull the latest changes"
gnat send demo alpha --wait -m "Summarize the README in one line"
gnat wait demo --timeout 600
gnat status demo
gnat cp demo alpha:notes.txt .
gnat logs demo -f
gnat web demo
gnat down demo
gnat delete demo
```

`gnat up` starts a control plane container and one container per agent, each of which clones the repository into `/workspace` before taking messages (after the first `up`, it finds the clone it left there); agents commit, push, and open pull requests as the GitHub token's user. It prints each started container and the web UI's URL. `gnat send` queues a message on the control plane for each agent it names, or for every agent when it names none; each agent's daemon runs its harness on its messages one at a time, continuing the same session unless a message is sent with `--new`. `gnat send --now` messages are delivered mid-turn by a PostToolUse hook that gnat installs in each agent's harness settings (`~/.claude/settings.json` or `~/.config/ox/settings.json`) and that runs after each tool call; if the turn ends first, they start the next turn. `gnat wait` blocks until agents have finished every message sent to them and prints each one's final reply; `gnat send --wait` sends and then waits. `gnat status` shows whether each agent is starting (cloning), busy, or idle, how long a busy agent has gone without an event, how many messages it has queued, what it has used (see Token usage and cost), and how its last turn ended. An agent whose daemon crashes restarts in the same container, keeping its clone and session; a turn it was running is reported as interrupted. `gnat stop` ends an agent's running turn (killing Claude and the processes its tools started, or cancelling Ox's prompt) and reports the turn as stopped, in the log, web UI, `gnat status`, and `gnat wait`; the agent keeps its queue, clone, and session, and goes on to its next message. Agents can message each other: gnat installs in each agent's user-level instructions (`~/.claude/CLAUDE.md` or `~/.config/ox/AGENTS.md`) a section listing its teammates and the command `/opt/gnat/gnat internal send TEAMMATE... [--now] -m TEXT` (text on stdin if `-m` is omitted), which goes through the control plane like `gnat send`. Recipients see "Message from agent NAME", `gnat logs` shows `>>> [from NAME] ...`, and the web UI shows "from NAME:". An agent can't message itself; two agents can still reply to each other forever, so watch `gnat logs` and `gnat down` if they do. `gnat logs` shows each message and the agent's output. The control plane also serves a web UI on a free localhost port (`gnat web` opens it) that streams each agent's events, one column per agent, headed by its name, harness, model, effort, and usage totals. Agents' text and messages between agents are rendered as Markdown (headings, emphasis, code, lists, links, blockquotes, tables, rules) by a small renderer built into the page: it makes DOM nodes from text only, passes no raw HTML through, and links only `http(s)` and `mailto` URLs (images are shown as links, never loaded), so untrusted content can't inject markup. Tool calls and results stay preformatted.

Every command takes the swarm first; commands that address agents take their names next, and address every agent when none are given.

| Command | Description |
| --- | --- |
| `gnat create SWARM [--dockerfile FILE] --repo OWNER/NAME [--file HOST_PATH[:WORKSPACE_PATH]]...` | Define a swarm that works in a GitHub repository. `FILE` must start `FROM gnat-agent`; without it, agents run on the base image. Each `--file` is a file outside source control that every agent gets in its clone (see Files outside source control). |
| `gnat delete SWARM` | Bring the swarm down and delete it, with its logs and agents' clones and sessions. |
| `gnat list` | List swarms. |
| `gnat up SWARM` | Build the images (the base, then the swarm's Dockerfile) and start the control plane and a container per agent, each with its own clone of the repository. Returns once the control plane answers (it gives up after 30 seconds), so `gnat up demo && gnat send demo ...` is safe. |
| `gnat down SWARM` | Stop and remove the swarm's containers. Agents' clones and sessions, queued messages, and logs are kept, so `gnat up` carries on where the swarm left off; a turn running at `down` is reported as interrupted. |
| `gnat send SWARM [AGENT...] [-m TEXT] [--now \| --new] [--wait [--timeout SECONDS]]` | Queue a message for the named agents, or for every agent. Without `-m`, the message is read from stdin. `--now` delivers it into each running turn at the agent's next tool call. `--new` starts the message in a fresh session, which later messages continue; it can't be combined with `--now`. `--wait` then waits as `gnat wait` does. |
| `gnat stop SWARM [AGENT...]` | End each agent's running turn without losing its queue, clone, or session. Agents not running a turn are left alone. |
| `gnat wait SWARM [AGENT...] [--timeout SECONDS]` | Wait until the agents have finished every message sent to them, then print each one's final reply. Exits 1 if an agent's last turn failed or was stopped, 124 on timeout. |
| `gnat status SWARM [AGENT...]` | Show each agent's state (starting, busy, idle, or its container's state if it isn't running), queued messages, turns finished, for a busy agent the time since its last event, its usage (tokens and cost), and how its last turn ended (including stopped). |
| `gnat cp SWARM AGENT:PATH HOST_PATH` | Copy a file or directory out of an agent. A relative `PATH` is in the agent's clone. While the swarm is down, it copies from the agent's volumes, so only paths in its clone and sessions. `HOST_PATH` `-` writes a tar to stdout. |
| `gnat logs SWARM [AGENT...] [-n LINES] [-f]` | Show agents' messages and output: their text, tool calls, and tool results. Reads the logs on the host, so it works while the swarm is down. |
| `gnat web SWARM [--no-open]` | Print the swarm's web UI URL and open it in the default browser. |
| `gnat agent create SWARM AGENT --instructions TEXT [--harness claude\|ox] [--model MODEL] [--effort LEVEL]` | Add an agent (swarm must be down). `--harness` defaults to `claude`. `--model` and `--effort` default to the harness's own; Claude Code takes efforts `low`, `medium`, `high`, `xhigh`, and `max` (see Ox agents for Ox's). |
| `gnat agent delete SWARM AGENT` | Remove an agent, with its clone and sessions (swarm must be down). |
| `gnat agent list SWARM` | List the swarm's agents: name, harness, model, and effort. |
| `gnat file add SWARM HOST_PATH[:WORKSPACE_PATH]` | Add a file for every agent's clone (swarm must be down). |
| `gnat file delete SWARM WORKSPACE_PATH` | Remove a file (swarm must be down). |
| `gnat file list SWARM` | List the swarm's files: workspace path and host path. |

Swarm definitions are stored in `$XDG_STATE_HOME/gnat/swarms` (default `~/.local/state/gnat/swarms`).

Logs are kept on the host in `$XDG_STATE_HOME/gnat/logs/SWARM`: each agent's readable `AGENT.log` and `events.jsonl`, which the web UI replays. The directory is mounted into the control container at `/var/log/gnat`, which runs as your user so the files are yours. Logs survive `gnat down`, and `gnat logs` reads them while the swarm is down. A new `gnat up` appends to the previous run's logs rather than starting fresh, so the history of every run is in one place; the web UI shows only the events of agents that are still in the swarm. The same directory holds `control.db`, a SQLite database of the control plane's queued messages and each agent's status, which `gnat up` takes up again. `gnat delete` removes the directory; to start fresh sooner, delete it while the swarm is down.

Each agent's clone and its harness's sessions are on Docker volumes: `gnat-SWARM-AGENT-workspace` at `/workspace`, and one for each directory the harness keeps sessions in (Claude Code's `~/.claude`; Ox's `~/.local/share/ox`, plus `~/.local/state/gnat`, where gnat notes the session to continue). They survive `gnat down`, so the next `gnat up` gives each agent the clone it left, with its branches, commits, and uncommitted changes, and the session it was in, while images, gnat's binary, credentials, and instructions are fresh. While the swarm is down, inspect one with `docker run --rm -v gnat-demo-alpha-workspace:/workspace --entrypoint git gnat-agent -C /workspace status`. `gnat agent delete` removes the agent's volumes and `gnat delete` removes all of the swarm's.

## Token usage and cost

gnat totals what each agent's harness reports it used: input tokens (with how many were read from the prompt cache), output tokens, and cost in US dollars. `gnat status` and the web UI, beside each agent's name, show each agent's totals, such as `1.2M in (1.1M cached), 34k out, $1.23`. Totals are kept in `control.db`, so they survive `gnat down`.

Claude Code reports tokens and cost, for every model the turn used (subagents' included), when a turn finishes; its cost is at API list prices, whatever the account is billed. A turn that is stopped or crashes reports nothing. Ox reports each turn's tokens when the turn ends, stopped turns included, and its cost as the session's grows during the turn.

## Files outside source control

Agents sometimes need files that aren't in the repository, such as a `.env` with an API key. `gnat create demo ... --file .env --file ~/keys/ci.json:config/ci.json` (or `gnat file add`) gives every agent those files in its clone. `HOST_PATH` is resolved to an absolute path and stored in the swarm definition, like the Dockerfile; `WORKSPACE_PATH` is relative to `/workspace` and defaults to the file's name. `gnat up` fails if a host file is missing.

The files are never part of an image. `gnat up` copies them into each agent container before it starts, into a directory only the agent user can read, and after cloning the agent daemon copies each to its `WORKSPACE_PATH` (mode 0600, parent directories created) and adds the path to the clone's `.git/info/exclude`, so `git add` and `git status` ignore it. A path the repository already tracks is refused. Files are read when `gnat up` runs, and each `up` replaces the agents' copies with them, so edit the host file and `gnat down` then `up` to change what agents get.

A restarted agent (its daemon crashed) keeps its clone, and gnat leaves the files in it alone: an existing file is never overwritten, even if the agent edited it, but a file that is missing is copied again. Each new `gnat up` starts from fresh containers, so it starts from fresh copies.

Agents can read these files, and anything that can steer an agent can too, so give them narrowly scoped keys, not your own.

## Ox agents

`gnat agent create ... --harness ox` makes an agent that runs Ox instead of Claude Code. `--model` takes an Ox qualified model ID such as `openrouter:deepseek/deepseek-v4.1-flash`, and `--effort` takes `default`, `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`, which the model must support.

The agent daemon runs `ox-server acp` and drives it over ACP, the Agent Client Protocol. It continues one session across messages (`--new` starts another, and a restarted daemon loads the last one from `~/.local/state/gnat/acp-session`), translates Ox's session updates into the same events as Claude Code's output, so logs, the web UI, and `gnat wait` show both alike, and ends a turn for `gnat stop` by cancelling it. gnat installs the agent's instructions in `~/.config/ox/AGENTS.md`, and its model, effort, Auto mode (tools run without asking), and the swarm's hook in `~/.config/ox/settings.json`.

## Examples

These use the test repository [kkestell/swarm-test](https://github.com/kkestell/swarm-test). From the repo root:

```sh
gnat create demo --repo kkestell/swarm-test
gnat agent create demo alpha --instructions "Be brief." --model haiku --effort low
gnat agent create demo beta --instructions "Be brief." --model haiku --effort low
gnat up demo
```

Run `gnat logs demo -f` in a second terminal, or `gnat web demo`, to watch both agents as you go.

**Queued messages, one session.** Alpha answers in order and remembers the first message when it gets the second.

```sh
gnat send demo alpha -m "What is the workspace codeword?"
gnat send demo alpha -m "What did I just ask you?"
```

**One message, every agent.** `--wait` prints each agent's reply once both are done.

```sh
gnat send demo --wait -m "What is the workspace codeword?"
```

**Agents work in parallel, on separate clones of the repository.** Both write `notes.txt`; Alpha still reads back its own.

```sh
gnat send demo alpha -m "Create notes.txt containing 'from alpha'."
gnat send demo beta -m "Create notes.txt containing 'from beta'."
gnat send demo alpha -m "Print notes.txt."
```

**Mid-turn messages.** Send the second command while Alpha's first tool call is still running; Alpha picks it up after that tool call.

```sh
gnat send demo alpha -m "Use Bash to run: python3 -c 'import time; time.sleep(20)'. Then run 'ls src'. Then report what you did and any messages you received."
gnat send demo alpha --now -m "Also say PINEAPPLE in your report."
```

**`--now` jumps the queue.** The story uses no tools, so there's no tool call to deliver the `--now` message at; it starts the next turn, ahead of the message queued before it.

```sh
gnat send demo alpha -m "Write a 300-word story about a fox. Don't use any tools."
gnat send demo alpha -m "Say THIRD."
gnat send demo alpha --now -m "Say SECOND."
```

**Stopping a turn.** Alpha's runaway turn ends; the message queued behind it still runs, in the same session.

```sh
gnat send demo alpha -m "Use Bash to run: python3 -c 'import time; time.sleep(300)'"
gnat send demo alpha -m "Say DONE."
gnat stop demo alpha
gnat wait demo alpha
```

**Fresh session.** `--new` forgets the conversation so far; the next message continues the new session.

```sh
gnat send demo alpha -m "What did I just ask you?"
gnat send demo alpha --new -m "What did I just ask you?"
```

**Pull requests.** Beta pushes a branch and opens a pull request.

```sh
gnat send demo beta -m "Add a goodbye() function to src/hello.py on a new branch, commit it, push it, and open a pull request."
```

**An Ox agent.** Gamma runs Ox beside the Claude agents and answers like them (this needs `OPENROUTER_API_KEY`).

```sh
gnat down demo
gnat agent create demo gamma --instructions "Be brief." --harness ox --model openrouter:deepseek/deepseek-v4.1-flash
gnat up demo
gnat send demo --wait -m "What is the workspace codeword?"
```

**Copying files out.** Alpha's `notes.txt` from the earlier example, copied into the current directory; this works while the swarm is down too.

```sh
gnat cp demo alpha:notes.txt .
```

**Clean up.**

```sh
gnat delete demo
```
