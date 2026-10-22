# Usage

Commands for a swarm take its name first. Commands that address agents take their names next, and address every agent when none are given. `gnat COMMAND -h` lists flags.

## Commands

| Command | Description |
| --- | --- |
| `gnat create SWARM --repo OWNER/NAME [--profile NAME\|PATH] [--dockerfile FILE] [--file PATH]...` | Define a swarm. `--profile` adds a [swarm profile](#swarm-profiles)'s agents. |
| `gnat delete SWARM` | Bring the swarm down and delete it, with its logs, clones, and sessions. |
| `gnat list` | List swarms. |
| `gnat up SWARM` | Build images and start the control plane and one container per agent. Returns once the control plane answers. |
| `gnat down SWARM` | Stop the containers. Clones, sessions, queued messages, and logs are kept. |
| `gnat send SWARM [AGENT...] [-m TEXT] [--now \| --new] [--wait [--timeout SECONDS]]` | Queue a message. Reads stdin without `-m`. |
| `gnat stop SWARM [AGENT...]` | End running turns. Queues, clones, and sessions are kept. |
| `gnat wait SWARM [AGENT...] [--timeout SECONDS]` | Wait until agents finish every message, then print their final replies. Exits 1 if a last turn failed or was stopped, 124 on timeout. |
| `gnat status SWARM [AGENT...]` | Show each agent's state, queue (see [messages](#messages)), time since its last event, usage, and how its last turn ended. Works while the swarm is down. |
| `gnat cp SWARM AGENT:PATH HOST_PATH` | Copy a file or directory out of an agent's clone. Works while the swarm is down. `-` writes a tar to stdout. |
| `gnat logs SWARM [AGENT...] [-n LINES] [-f]` | Show agents' messages, text, and tool calls. Works while the swarm is down. |
| `gnat web SWARM [--no-open]` | Open the web UI. |
| `gnat agent create SWARM AGENT [--profile NAME\|PATH] [--instructions TEXT] [--harness claude\|ox] [--model MODEL] [--effort LEVEL] [--merge]` | Add an agent (swarm down). `--instructions` supplies optional additional instructions. `--merge` lets it merge pull requests (see below); `--merge=false` overrides a profile that grants it. |
| `gnat agent delete SWARM AGENT` | Remove an agent with its clone and sessions (swarm down). |
| `gnat agent list SWARM` | List agents: name, harness, model, effort, and `merge` if it may merge pull requests. |
| `gnat profiles list [--swarm]` | List available agent profile names, or swarm profile names with `--swarm`, alphabetically. |
| `gnat auth login claude\|ox` | Save a harness's credential in the system keyring. See [setup](setup.md#credentials). |
| `gnat auth logout claude\|ox` | Remove a harness's saved credential. |
| `gnat file add SWARM HOST_PATH[:WORKSPACE_PATH]` | Add a file for every clone (swarm down). See [setup](setup.md#files-outside-source-control). |
| `gnat file delete SWARM WORKSPACE_PATH` | Remove a file (swarm down). |
| `gnat file list SWARM` | List files. |

## Agent profiles

`make install` includes four agent profiles from the repository's
`profiles/agents` directory: `coder` implements changes, `reviewer` reports
code review findings, `researcher` investigates questions and compares options,
and `orchestrator` hands out work to its teammates, has their pull requests
reviewed, and merges them. They provide role instructions. `orchestrator` runs
`claude-opus-5-5`, `coder` runs `claude-sonnet-5-5`, and `reviewer` runs
`openrouter:deepseek/deepseek-v4.1-flash` on Ox, each at `high` effort;
`researcher` uses the default harness, model, and effort. Existing profile files
are preserved during installation, so you can customize them.

Save reusable agent settings in `~/.config/gnat/profiles/agents/NAME.yaml`
(`$XDG_CONFIG_HOME/gnat/profiles/agents/NAME.yaml` when set). For example,
`coder.yaml`:

```yaml
instructions: |
  Implement the requested change.
  Add useful tests and explain what you changed.
harness: claude
model: sonnet
effort: high
```

```sh
gnat profiles list
gnat agent create demo alpha --profile coder
gnat agent create demo beta --profile coder --effort max
```

A profile may also set `merge: true`, which lets the agent merge pull requests (the
bundled `orchestrator` does); see Pull requests below.

All four settings are optional. gnat always supplies basic instructions about
the swarm, available tools, and credentials. Use the profile's `instructions` or
`--instructions` for optional additional instructions. An omitted harness defaults
to `claude`; omitted model and effort use the harness's defaults. Explicit flags replace individual
profile settings regardless of flag order. Use `--model=""` or `--effort=""` to
restore the harness's default for that setting. Use `--instructions=""` to clear
the profile's additional instructions.

`--profile` also takes a profile file's path instead of a name: a value containing
a slash or ending in `.yaml` or `.yml` is a path, as in
`gnat agent create demo alpha --profile ./coder.yaml`. The same goes for a swarm
profile with `gnat create --profile`.

Profile names use the same characters as agent names: a letter or digit followed
by letters, digits, underscores, dots, or hyphens. Each file contains one YAML
document; unknown settings are rejected. `gnat profiles list` lists names
without parsing the files and prints nothing if the directory does not exist.

Settings are copied into the swarm definition when an agent is created. Editing
or removing a profile later does not change existing agents.

## Swarm profiles

A swarm profile lists the agents to create with a swarm. Save it in
`~/.config/gnat/profiles/swarms/NAME.yaml`
(`$XDG_CONFIG_HOME/gnat/profiles/swarms/NAME.yaml` when set). `make install`
includes four from the repository's `profiles/swarms` directory: `small` (two
coders and two reviewers), `large` (four of each), and `small-orchestrated` and
`large-orchestrated`, which add an orchestrator. Use the ones without an
orchestrator when you, or an agent you're working with, hand out the work. This
is `small-orchestrated`:

```yaml
agents:
  - name: orchestrator
    profile: orchestrator
    harness: claude
    model: claude-opus-5-5
    effort: high
  - name: coder
    count: 2
    profile: coder
    harness: claude
    model: claude-opus-5-5
    effort: high
  - name: reviewer
    count: 2
    profile: reviewer
    harness: claude
    model: claude-opus-5-5
    effort: high
```

```sh
gnat profiles list --swarm
gnat create demo --repo OWNER/NAME --profile small-orchestrated
```

That creates `orchestrator`, `coder-1`, `coder-2`, `reviewer-1`, and `reviewer-2`,
in that order, all running `claude-opus-5-5` at `high` effort. Each entry needs a
`name`. With `count`, it creates that many agents named `NAME-1` through
`NAME-COUNT`; without it, one agent named `NAME`. `profile` names an
[agent profile](#agent-profiles) to start from, or gives its path (a relative path
is relative to the swarm profile's directory), and `instructions`, `harness`,
`model`, and `effort` replace its settings, the way flags do for
`gnat agent create`; an empty value clears one. The file has the same rules as an
agent profile: one YAML document, unknown settings rejected. If any agent is
invalid, `gnat create` creates nothing. Add or remove agents afterwards with
`gnat agent create` and `gnat agent delete`.

## Messages

Each agent works through its queue one message at a time, continuing one session.

- `--new` starts the message in a fresh session, which later messages continue.
- `--now` delivers the message into the running turn after the agent's next tool call. If the turn ends first, or the agent is idle, it starts the next turn, ahead of the queue. `gnat send --now` says so instead of "queued message for".
- `gnat stop` ends the running turn; the agent goes on to its next message.

Agents can message each other with `/opt/gnat/gnat-container send TEAMMATE... [--now] -m TEXT`, which their instructions describe. Two agents can reply to each other forever; watch `gnat logs` and `gnat down` if they do.

`gnat status` counts each agent's queued messages and breaks the count down: how many are `--now` messages not yet delivered, and how many each teammate sent, such as `3 queued (1 now, 2 from beta)`. A `--now` message from a teammate counts in both. A `--now` message leaves the count once it is delivered, mid-turn or as the start of a turn, so `now` going to zero means it has landed. With nothing to break down, it's just `1 queued`.

An agent whose daemon crashes restarts in the same container with its clone and session, and the turn it was running is reported as interrupted.

## Who is asking

`gnat up` gives each agent a secret that only it and the control plane have, and every
request an agent makes to the control plane carries it: messages, the connection
the agent's daemon keeps, the hook that delivers `--now` messages, git, and the pull
request commands. The control plane takes who is asking from the secret, not from
anything the request says, so a message labelled as from an agent is from that agent.
An agent can send, take its own messages, and make the GitHub requests below; asking how
other agents are doing, waiting on them, or stopping them is the operator's, whose
commands (`gnat send`, `status`, `wait`, `stop`) reach the control plane by a socket
inside its container instead.

## Pull requests

Agents reach GitHub's pull requests through the control plane, which holds the
GitHub token and enforces the rules, with `gnat-container pr create|list|view|diff|checks|comment|review|merge`.
Each agent has a secret `gnat up` generates for it, which is how the control
plane knows who is asking. Pull requests are always on the swarm's repository,
and one is opened only from a branch named `SWARM/AGENT/...` (the swarm's and the
agent's own name; names that would make invalid git refs, such as `a..b` or
`x.lock`, are refused). Only agents saved with `merge` (`gnat agent create --merge`,
or `merge: true` in a profile) may run `gnat-container pr merge`. A definition
saved without it gives no permission.

## State

- Swarm definitions: `~/.local/state/gnat/swarms` (`$XDG_STATE_HOME/gnat/swarms`).
- Logs and `control.db` (queues, status, usage): `~/.local/state/gnat/logs/SWARM`. Each `gnat up` appends to them.
- Clones and sessions: Docker volumes named `gnat-SWARM-AGENT-*`. They survive `gnat down`, so the next `gnat up` picks up where agents left off.

`gnat delete` removes all of it.

Usage totals in `gnat status` and the web UI come from what each harness reports. Claude Code's cost is at API list prices and isn't reported for stopped or crashed turns.

The web UI lists the repository's pull requests, which the control plane fetches from GitHub with `GH_TOKEN`.

## Examples

These use `OWNER/NAME`, a throwaway repository the agents can push to and open pull requests on. Run `gnat logs demo -f` or `gnat web demo` alongside to watch.

```sh
gnat create demo --repo OWNER/NAME
gnat agent create demo alpha --instructions "Be brief." --model haiku --effort low
gnat agent create demo beta --instructions "Be brief." --model haiku --effort low
gnat up demo
```

One session across queued messages:

```sh
gnat send demo alpha -m "What files are in the repository root?"
gnat send demo alpha -m "What did I just ask you?"
```

Every agent, waiting for replies:

```sh
gnat send demo --wait -m "What files are in the repository root?"
```

Separate clones:

```sh
gnat send demo alpha -m "Create notes.txt containing 'from alpha'."
gnat send demo beta -m "Create notes.txt containing 'from beta'."
gnat send demo alpha -m "Print notes.txt."
```

Mid-turn delivery (send the second while the first's sleep is running):

```sh
gnat send demo alpha -m "Use Bash to run: python3 -c 'import time; time.sleep(20)'. Then run 'ls'. Then report what you did and any messages you received."
gnat send demo alpha --now -m "Also say PINEAPPLE in your report."
```

`--now` jumping the queue when there's no tool call to deliver at:

```sh
gnat send demo alpha -m "Write a 300-word story about a fox. Don't use any tools."
gnat send demo alpha -m "Say THIRD."
gnat send demo alpha --now -m "Say SECOND."
```

Stopping a turn:

```sh
gnat send demo alpha -m "Use Bash to run: python3 -c 'import time; time.sleep(300)'"
gnat send demo alpha -m "Say DONE."
gnat stop demo alpha
gnat wait demo alpha
```

Fresh session:

```sh
gnat send demo alpha --new -m "What did I just ask you?"
```

Pull request:

```sh
gnat send demo beta -m "Add a line to README.md on a new branch, commit it, push it, and open a pull request."
```

An Ox agent alongside the Claude Code ones (after `gnat auth login ox`):

```sh
gnat down demo
gnat agent create demo gamma --instructions "Be brief." --harness ox --model openrouter:deepseek/deepseek-v4.1-flash
gnat up demo
gnat send demo --wait -m "What files are in the repository root?"
```

Copying out and cleaning up:

```sh
gnat cp demo alpha:notes.txt .
gnat delete demo
```
