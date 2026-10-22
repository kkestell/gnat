# Usage

Commands for a swarm take its name first. Commands that address agents take their names next, and address every agent when none are given. `gnat COMMAND -h` lists flags.

## Commands

| Command | Description |
| --- | --- |
| `gnat create SWARM --repo OWNER/NAME [--dockerfile FILE] [--file PATH]...` | Define a swarm. |
| `gnat delete SWARM` | Bring the swarm down and delete it, with its logs, clones, and sessions. |
| `gnat list` | List swarms. |
| `gnat up SWARM` | Build images and start the control plane and one container per agent. Returns once the control plane answers. |
| `gnat down SWARM` | Stop the containers. Clones, sessions, queued messages, and logs are kept. |
| `gnat send SWARM [AGENT...] [-m TEXT] [--now \| --new] [--wait [--timeout SECONDS]]` | Queue a message. Reads stdin without `-m`. |
| `gnat stop SWARM [AGENT...]` | End running turns. Queues, clones, and sessions are kept. |
| `gnat wait SWARM [AGENT...] [--timeout SECONDS]` | Wait until agents finish every message, then print their final replies. Exits 1 if a last turn failed or was stopped, 124 on timeout. |
| `gnat status SWARM [AGENT...]` | Show each agent's state, queue, time since its last event, usage, and how its last turn ended. Works while the swarm is down. |
| `gnat cp SWARM AGENT:PATH HOST_PATH` | Copy a file or directory out of an agent's clone. Works while the swarm is down. `-` writes a tar to stdout. |
| `gnat logs SWARM [AGENT...] [-n LINES] [-f]` | Show agents' messages, text, and tool calls. Works while the swarm is down. |
| `gnat web SWARM [--no-open]` | Open the web UI. |
| `gnat agent create SWARM AGENT [--profile NAME] [--instructions TEXT] [--harness claude\|ox] [--model MODEL] [--effort LEVEL]` | Add an agent (swarm down). `--instructions` supplies optional additional instructions. |
| `gnat agent delete SWARM AGENT` | Remove an agent with its clone and sessions (swarm down). |
| `gnat agent list SWARM` | List agents. |
| `gnat profiles list` | List available profile names, alphabetically. |
| `gnat file add SWARM HOST_PATH[:WORKSPACE_PATH]` | Add a file for every clone (swarm down). See [setup](setup.md#files-outside-source-control). |
| `gnat file delete SWARM WORKSPACE_PATH` | Remove a file (swarm down). |
| `gnat file list SWARM` | List files. |

## Agent profiles

`make install` includes three profiles from the repository's `profiles/`
directory: `coder` implements changes, `reviewer` reports code review findings,
and `researcher` investigates questions and compares options. They provide role
instructions and use the default harness, model, and effort. Existing profile
files are preserved during installation, so you can customize them.

Save reusable agent settings in `~/.config/gnat/profiles/NAME.yaml`
(`$XDG_CONFIG_HOME/gnat/profiles/NAME.yaml` when set). For example,
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

All four settings are optional. gnat always supplies basic instructions about
the swarm, available tools, and credentials. Use the profile's `instructions` or
`--instructions` for optional additional instructions. An omitted harness defaults
to `claude`; omitted model and effort use the harness's defaults. Explicit flags replace individual
profile settings regardless of flag order. Use `--model=""` or `--effort=""` to
restore the harness's default for that setting. Use `--instructions=""` to clear
the profile's additional instructions.

Profile names use the same characters as agent names: a letter or digit followed
by letters, digits, underscores, dots, or hyphens. Each file contains one YAML
document; unknown settings are rejected. `gnat profiles list` lists names
without parsing the files and prints nothing if the directory does not exist.

Settings are copied into the swarm definition when an agent is created. Editing
or removing a profile later does not change existing agents.

## Messages

Each agent works through its queue one message at a time, continuing one session.

- `--new` starts the message in a fresh session, which later messages continue.
- `--now` delivers the message into the running turn after the agent's next tool call. If the turn ends first, it starts the next turn, ahead of the queue.
- `gnat stop` ends the running turn; the agent goes on to its next message.

Agents can message each other with `/opt/gnat/gnat internal send TEAMMATE... [--now] -m TEXT`, which their instructions describe. Two agents can reply to each other forever; watch `gnat logs` and `gnat down` if they do.

An agent whose daemon crashes restarts in the same container with its clone and session, and the turn it was running is reported as interrupted.

## State

- Swarm definitions: `~/.local/state/gnat/swarms` (`$XDG_STATE_HOME/gnat/swarms`).
- Logs and `control.db` (queues, status, usage): `~/.local/state/gnat/logs/SWARM`. Each `gnat up` appends to them.
- Clones and sessions: Docker volumes named `gnat-SWARM-AGENT-*`. They survive `gnat down`, so the next `gnat up` picks up where agents left off.

`gnat delete` removes all of it.

Usage totals in `gnat status` and the web UI come from what each harness reports. Claude Code's cost is at API list prices and isn't reported for stopped or crashed turns.

The web UI lists the repository's pull requests, which the control plane fetches from GitHub with `GH_TOKEN`.

## Examples

These use [kkestell/swarm-test](https://github.com/kkestell/swarm-test). Run `gnat logs demo -f` or `gnat web demo` alongside to watch.

```sh
gnat create demo --repo kkestell/swarm-test
gnat agent create demo alpha --instructions "Be brief." --model haiku --effort low
gnat agent create demo beta --instructions "Be brief." --model haiku --effort low
gnat up demo
```

One session across queued messages:

```sh
gnat send demo alpha -m "What is the workspace codeword?"
gnat send demo alpha -m "What did I just ask you?"
```

Every agent, waiting for replies:

```sh
gnat send demo --wait -m "What is the workspace codeword?"
```

Separate clones:

```sh
gnat send demo alpha -m "Create notes.txt containing 'from alpha'."
gnat send demo beta -m "Create notes.txt containing 'from beta'."
gnat send demo alpha -m "Print notes.txt."
```

Mid-turn delivery (send the second while the first's sleep is running):

```sh
gnat send demo alpha -m "Use Bash to run: python3 -c 'import time; time.sleep(20)'. Then run 'ls src'. Then report what you did and any messages you received."
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
gnat send demo beta -m "Add a goodbye() function to src/hello.py on a new branch, commit it, push it, and open a pull request."
```

Ox agent (needs `OPENROUTER_API_KEY`):

```sh
gnat down demo
gnat agent create demo gamma --instructions "Be brief." --harness ox --model openrouter:deepseek/deepseek-v4.1-flash
gnat up demo
gnat send demo --wait -m "What is the workspace codeword?"
```

Copying out and cleaning up:

```sh
gnat cp demo alpha:notes.txt .
gnat delete demo
```
