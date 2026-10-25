# Usage

## V2 local swarm (current source build)

Run `gnat` from a committed local Git checkout to start/reuse the per-user daemon
and open that workspace in the browser. `gnat --no-open` prints its URL without
opening a browser. `gnat --help` and `gnat --version` are available.
`gnat daemon stop` shuts down the daemon while retaining all saved records.
Closing the terminal or browser leaves the daemon running.

A checkout root, subdirectory, and symlink alias reuse one swarm. Separate linked
user worktrees register separately. The browser links all registered workspaces
and keeps each selected workspace on an explicit `/swarms/{id}` route.
The right sidebar runs the configured Claude Code, Codex, or OpenCode
orchestrator in the original checkout with saved
model/effort, normal local credentials, and an exact persistent session. Send
acknowledgement means the input is committed, even if the browser closes. Queued
input remains visible; transcript and reported usage replay on reconnection.

- **Pause:** prevent new turns across the swarm while active turns finish.
- **Stop:** interrupt every agent and prevent new turns, preserving the team.
- **Interrupt:** end the orchestrator turn and pause swarm scheduling.
- **Resume / Retry:** check prerequisites and enable queued input again.
- **New conversation:** create a fresh session when execution is settled, keeping
  queued input, identity, checkout, and prior history. The conversation selector
  shows earlier transcripts. A failed exact resume never silently starts fresh.

Interrupted input is not replayed: send new direction explicitly. Daemon stop
interrupts/reaps active processes and preserves queues and sessions. A restart
marks unfinished persisted turns interrupted and holds their queue for explicit
continuation. Following a daemon crash, clean up any surviving processes first;
automatic crash-process cleanup remains outside initial scope.

Ask the orchestrator to create workers with initial prompts. Workers start from
committed HEAD or an explicit committed base, each in a separate worktree and
conversation. Worker creation can override harness, model, and effort, so a team
can mix all three harnesses; model/effort choices must fit that harness. Their cards update as they join or leave; select a worker to
inspect its transcript, branch, path, and execution outcome. Removed workers are
available in the history selector. New orchestrator conversations preserve the
team and provide saved runtime orientation.

The Gnat MCP server exposes `list_agents`, `send_message`, and bounded
`read_agent` to all agents. Only the orchestrator can call `create_agent`,
`interrupt_agent`, and `remove_agent`. All mutations require unique request IDs
(reuse an ID only for transport retries). `interrupt_agent` also requires the
observed running turn ID. Messages queue during active execution and each starts
a later turn without requiring polling; agents decide how to coordinate and
communicate results. Successful turn endings do not establish task completion.

Creation returns a stable ID before provisioning finishes. Failed provisioning
is visible; retrying the same creation request reuses the identity. Removal
interrupts execution, cancels queued input, and removes the managed worktree and
branch with ordinary Git safeguards. Dirty worktrees or unmerged branches can
refuse removal: repair/integrate the remaining work using ordinary Git, then
retry the removal request. Nothing forces deletion. Worker commits are already
in the local repository for ordinary integration into the original checkout.

## Historical v1 command reference

The commands below describe retained v1 code and older releases. They are not
available through the current v2 source entry point.

Commands for a swarm take its name first. Commands that address agents take their names next, and address every agent when none are given. `gnat COMMAND -h` lists flags.

## Commands

| Command | Description |
| --- | --- |
| `gnat create SWARM --repo OWNER/NAME [--profile NAME\|PATH] [--dockerfile FILE] [--file PATH]...` | Define a swarm. Uses `./Dockerfile.gnat` if present; `--dockerfile` overrides it. `--profile` adds a [swarm profile](#swarm-profiles)'s agents. |
| `gnat delete SWARM` | Bring the swarm down and delete it, with its logs, clones, and sessions. |
| `gnat list` | List swarms. |
| `gnat up SWARM` | Build images and start the control plane and one container per agent. Returns once the control plane answers. |
| `gnat down SWARM` | Stop the containers. Clones, sessions, queued messages, and logs are kept. |
| `gnat send SWARM [AGENT...] [-m TEXT] [--now \| --new] [--wait [--timeout SECONDS]]` | Queue a message. Reads stdin without `-m`. |
| `gnat stop SWARM [AGENT...]` | End running turns. Queues, clones, and sessions are kept. |
| `gnat pause SWARM [AGENT...] [-m TEXT] [--wait [--timeout SECONDS]]` | Let running turns finish and start no more until `gnat resume`. With `-m`, give each running turn the message, as `send --now` does, dropped if the turn ends first. With `--wait`, then wait for the turns to end and print the replies. |
| `gnat resume SWARM [AGENT...]` | Let paused agents start their queued messages again. |
| `gnat wait SWARM [AGENT...] [--any] [--timeout SECONDS]` | Wait until agents finish every message, then print their final replies. Exits 1 if a last turn failed or was stopped, 124 on timeout. With `--any`, return as soon as at least one of them has (one already finished counts at once), printing the replies of those that have, each under its name, and on stderr the ones still busy (`still waiting on: alpha, gamma`); the exit status is for those that have. |
| `gnat status SWARM [AGENT...]` | Show each agent's state, queue (see [messages](#messages)), time since its last event, usage, how full its context window is, and how its last turn ended, then the Claude account's usage limits. Works while the swarm is down. |
| `gnat cp SWARM AGENT:PATH HOST_PATH` | Copy a file or directory out of an agent's clone. Works while the swarm is down. `-` writes a tar to stdout. `HOST_PATH` is treated as `docker cp` treats it: a directory copied to an existing directory goes inside it, and to a path that doesn't exist creates that path with the directory's contents; a `PATH` ending in `/.` copies the directory's contents, so `AGENT:.` copies the clone's contents. |
| `gnat logs SWARM [AGENT...] [-n LINES] [-f]` | Show agents' messages, text, and tool calls. Works while the swarm is down. |
| `gnat logs SWARM [AGENT...] --replies` | Show all final replies in recording order, headed by agent, turn, and task sender. Preserves multiline replies and works while the swarm is down. Cannot be combined with `-n` or `-f`. |
| `gnat web SWARM [--no-open]` | Open the web UI. |
| `gnat agent create SWARM AGENT [--profile NAME\|PATH] [--instructions TEXT] [--harness claude\|ox\|codex] [--model MODEL] [--effort LEVEL] [--merge]` | Add an agent (swarm down). `--instructions` supplies optional additional instructions. `--merge` lets it merge pull requests (see below); `--merge=false` overrides a profile that grants it. |
| `gnat agent set SWARM AGENT [--profile NAME\|PATH] [--instructions TEXT] [--harness claude\|ox\|codex] [--model MODEL] [--effort LEVEL] [--merge\|--no-merge]` | Change an agent's settings (swarm down), keeping its ID, clone, sessions, queue, logs, and history. Flags given replace their settings and the rest keep their values; with `--profile`, the settings the profile names replace the agent's, and flags replace those. A setting the profile leaves out keeps its value: a profile without `harness:` doesn't change the harness. A new harness can't continue the old one's session, so changing it deletes the agent's sessions and its next turn starts a fresh one. Prints what changed. |
| `gnat agent delete SWARM AGENT` | Remove an agent with its clone, sessions, and log (swarm down). |
| `gnat agent list SWARM` | List agents: name, harness, model, effort, and `merge` if it may merge pull requests. |
| `gnat profiles list [--swarm]` | List available agent profile names, or swarm profile names with `--swarm`, alphabetically. |
| `gnat auth login claude\|ox\|codex` | Save a harness's credential in the system keyring. See [setup](setup.md#credentials). |
| `gnat auth logout claude\|ox\|codex` | Remove a harness's saved credential. |
| `gnat file add SWARM HOST_PATH[:WORKSPACE_PATH]` | Add a file for every clone (swarm down). See [setup](setup.md#files-outside-source-control). |
| `gnat file delete SWARM WORKSPACE_PATH` | Remove a file (swarm down). |
| `gnat file list SWARM` | List files. |

## Agent profiles

`/gnat:install` and `make install` include seven agent profiles from the repository's
`profiles/agents` directory: `coder` implements changes (`codex-coder` uses
Codex, and `ox-deepseek-coder` uses a cheaper Ox model), `planner` plans them
without making them, `reviewer` reports code review findings, `researcher`
investigates questions and compares options, and `orchestrator` hands out work
to its teammates, has their pull requests reviewed, and merges them. They
provide role instructions. `coder` replies with a plan and waits for approval
before building, unless its task includes a plan or says to go ahead without
one; `planner` replies with a plan and changes nothing; `reviewer` checks a
change against its plan. The Claude profiles run `claude-opus-5-5` at `high` effort.
`codex-coder` uses Codex's model and effort defaults.
`ox-deepseek-coder` is `coder` on Ox with
`openrouter:deepseek/deepseek-v4.1-flash`. Existing profile
files are preserved during installation, so you can customize them.

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
or removing a profile later does not change existing agents; `gnat agent set
SWARM AGENT --profile NAME` applies the settings it names again.

## Swarm profiles

A swarm profile lists the agents to create with a swarm. Save it in
`~/.config/gnat/profiles/swarms/NAME.yaml`
(`$XDG_CONFIG_HOME/gnat/profiles/swarms/NAME.yaml` when set). `/gnat:install` and `make install`
include four from the repository's `profiles/swarms` directory: `small` (two
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
- `gnat pause` lets the running turn finish and starts no more: queued messages, and any sent meanwhile, wait until `gnat resume`, though `--now` messages still go into a running turn. `-m TEXT` gives the running turn a message at its next tool call, such as "commit and push what you have, and report"; unlike `send --now`, it's dropped if the turn ends first rather than starting another. A paused agent counts as finished for `gnat wait` once its turn ends, `gnat status` shows it as `idle, paused`, and it stays paused through `gnat down` and `gnat up`.

Agents can message each other with `/opt/gnat/gnat-container send TEAMMATE... [--now] -m TEXT`, which their instructions describe. Two agents can reply to each other forever; watch `gnat logs` and `gnat down` if they do.

`gnat status` counts each agent's queued messages and breaks the count down by sender, you first, with how many of each sender's are `--now` messages not yet delivered: `4 queued: 2 from you (1 now), 1 from alpha, 1 from beta (1 now)`. A `--now` message leaves the count once it is delivered, mid-turn or as the start of a turn, so its `now` going away means it has landed. When every queued message is yours and none is `--now`, it's just `2 queued`.

An agent whose daemon crashes restarts in the same container with its clone and session, and the turn it was running is reported as interrupted.

## Who is asking

`gnat up` gives each agent a secret that only it and the control plane have, and every
request an agent makes to the control plane carries it: messages, the connection
the agent's daemon keeps, the hook that delivers `--now` messages, git, and the pull
request commands. The control plane takes who is asking from the secret, not from
anything the request says, so a message labelled as from an agent is from that agent.
An agent can send, take its own messages, and make the GitHub requests below; asking how
other agents are doing, waiting on them, or stopping them is the operator's, whose
commands (`gnat send`, `status`, `wait`, `stop`, `pause`, `resume`) reach the control plane by a socket
inside its container instead.

## Pull requests

Agents reach GitHub's pull requests through the control plane, which holds the
GitHub token and enforces the rules, with `gnat-container pr create|list|view|diff|checks|comment|review|close|edit|ready|merge`.
Each agent has a secret `gnat up` generates for it, which is how the control
plane knows who is asking. Pull requests are always on the swarm's repository,
and one is opened only from a branch named `SWARM/AGENT/...` (the swarm's and the
agent's own name; names that would make invalid git refs, such as `a..b` or
`x.lock`, are refused). An agent may close, edit (`pr edit NUMBER --title TEXT`,
`--body TEXT`, or `--body-file FILE`), or mark a draft ready for review (`pr ready NUMBER`)
only a pull request whose head is one of its own branches in the swarm's repository.
`pr review --approve` and `--request-changes` also label the pull request
`gnat:approved` or `gnat:changes-requested` (taking away the other), so
`gh pr list --label gnat:approved` lists what agents have approved. Pull requests
opened by the operator's account, as the agents' are, can't be approved by it, so
on those the verdict is posted as a comment that says which. Only agents saved with `merge` (`gnat agent create --merge`,
or `merge: true` in a profile) may run `gnat-container pr merge`. A definition
saved without it gives no permission.

## State

- Swarm definitions: `~/.local/state/gnat/swarms` (`$XDG_STATE_HOME/gnat/swarms`).
- Logs and `control.db` (queues, pauses, status, usage): `~/.local/state/gnat/logs/SWARM`. Each `gnat up` appends to them.
- Clones and sessions: Docker volumes named `gnat-SWARM-AGENT-*`. They survive `gnat down`, so the next `gnat up` picks up where agents left off.

`gnat delete` removes all of it.

Each agent gets an ID when it's created, saved in the definition, which tells it apart from an earlier agent with the same name. If you delete an agent and create another by that name, the new one starts fresh: `gnat status`, `gnat logs`, and the web UI show none of the old one's turns, usage, queued messages, or output. Agents created before gnat gave IDs keep their history.

Usage totals in `gnat status` and the web UI come from what each harness reports. Claude Code's cost is at API list prices and isn't reported for stopped or crashed turns.

Codex reports tokens and context usage, with no dollar cost. Its agents use one
shared ChatGPT subscription credential. Renew it with `gnat auth login codex`
when it expires, then restart affected swarms with `down` and `up`; their
conversations continue. See [Codex setup](setup.md#codex-agents).

Each agent's context shows how full its context window was at its latest model call, as a percentage of the window (`context 23% of 1M`), or in tokens until the harness has said how big the window is; Claude Code says so at the end of its first turn. Claude Code also reports the usage limits of the account its token belongs to, which every Claude agent shares: `gnat status` ends with the latest any agent reported (`usage limits: 5h 7% (resets 13:20), 7d 49% (resets Sun 22:00)`), leaving out windows that have reset since and saying how long ago they were reported if it's been more than ten minutes. The web UI shows them in its header when the swarm has Claude agents.

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

Collecting replies as they come (wait again on the agents it says it is still waiting on, until it says none):

```sh
gnat send demo alpha -m "Count to 3, then say ALPHA."
gnat send demo beta -m "Use Bash to run: python3 -c 'import time; time.sleep(30)'. Then say BETA."
gnat wait demo alpha beta --any   # alpha's reply, then on stderr: still waiting on: beta
gnat wait demo beta --any         # beta's reply, and nothing on stderr
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

Pausing with a wrap-up:

```sh
gnat send demo alpha -m "Count slowly to 20 using Bash, one sleep 5 per number."
gnat send demo alpha -m "Say QUEUED."
gnat pause demo alpha -m "Stop counting and say WRAPPED." --wait
gnat status demo alpha    # idle, paused, 1 queued
gnat resume demo alpha
gnat wait demo alpha      # QUEUED
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

A Codex agent alongside the others (after `gnat auth login codex`):

```sh
gnat down demo
gnat agent create demo delta --harness codex --instructions "Be brief."
gnat up demo
gnat send demo delta --wait -m "What files are in the repository root?"
```

Copying out and cleaning up:

```sh
gnat cp demo alpha:notes.txt .
gnat cp demo alpha:. alpha-clone
gnat delete demo
```
