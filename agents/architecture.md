# Architecture

This is what the code can't say: the shape of the system, the lines drawn between its parts, and the reasons for them. The packages are listed in `CLAUDE.md`; the commands and formats in `usage.md` and `setup.md`. A change that keeps these lines where they are shouldn't need to change this file.

## Current v2 foundation

`cmd/gnat` now uses `internal/v2/entry`. A normal invocation discovers a committed
local checkout, starts or reuses a detached per-user daemon, registers or opens
its swarm, and prints/opens `/swarms/{id}`. The public surface is `gnat`,
`--no-open`, `--help`, `--version`, and `gnat daemon stop`.

`internal/v2/workspace` only reads Git. Canonical checkout roots identify
workspaces; the canonical Git common directory is separate metadata. User
worktrees sharing a common directory have distinct swarms. Saved agent working
directories identify managed checkouts explicitly. Nothing contacts GitHub or
modifies the original checkout during registration.

`internal/v2/store` owns `$XDG_STATE_HOME/gnat/v2/gnat.db`, defaulting to
`~/.local/state/gnat/v2/gnat.db`. SQLite `user_version` identifies the schema;
unknown versions and nonempty unversioned databases are refused. Workspace,
swarm, pending orchestrator, initial conversation, and ordered versioned creation
events commit together. A unique canonical root and unique workspace-to-swarm
relationship prevent duplicate identities. A single database connection
serializes registration within the daemon; snapshots include a sequence cursor.
New swarms resolve the required orchestrator harness/model/effort from
`$XDG_CONFIG_HOME/gnat/config.yaml`, defaulting to `~/.config/gnat/config.yaml`.
Reopening uses saved settings. No v1 records are read, converted, or removed.

`internal/v2/daemon` holds an advisory lock throughout its lifetime. A private
`v2/runtime/daemon.json` records the ephemeral loopback address, PID, generation,
and CLI token. A readiness handshake verifies generation before reuse. Startup
runs a detached copy of the executable; it survives the launching terminal.
Authenticated CLI mutations register workspaces and stop the daemon. Host and
Origin checks protect the local HTTP destination. PID values are diagnostic;
shutdown uses the authenticated endpoint, never a signal to a saved PID.

The daemon embeds a read-only HTML workspace switcher with a distinct
orchestrator sidebar. It reads the same SQLite snapshots as the HTTP API and
reports missing saved paths without recreating them. Harness execution, chat,
message scheduling, worker creation, event streaming, and recovery of running
turns are later milestones. There are currently no harness processes to stop.
The retained v1 React dashboard is not served by v2.

The rest of this document describes the retained v1 packages. Their Docker,
profile, credential proxy, and compatibility constraints do not apply to v2.
The old distribution/build targets remain pending milestone-six cleanup; source
builds can use `go build -o gnat ./cmd/gnat` directly.

## Legacy v1 shape

A running swarm is one Docker network with one control container and one container per agent. The host runs `gnat`, which does three things and no more: edits swarm definitions on disk, drives Docker, and runs commands inside the control container. Everything that happens while a swarm is up happens in the containers, in `gnat-container`, a static linux binary embedded in `gnat` and copied into every container at `gnat up`. Images carry tools, never gnat: a gnat change needs no image rebuild, and the binary in a container is always the same build as the `gnat` that started it.

The control container is the swarm's only shared process. It holds the message queues, the agents' status and usage, the logs, the GitHub token, and a mirror of the repository, and it serves the web UI. Agent containers hold a clone, a harness, and its credential, and run one daemon whose whole job is to take turns from the control plane and report what happened.

The host never speaks to the control plane over the network. `send`, `stop`, `pause`, `resume`, `wait`, and `status` are `docker exec` of the control container's own binary, which talks to the control plane over a Unix socket in that container. The web UI is the one published port, bound to loopback. Logs are read from the host's own copy. `cp` is `docker cp`, from the container or from its volumes. So a swarm keeps working under the build that started it, and the only interfaces that cross builds are the command line and output of `gnat-container control`, and the files on the host.

## Where state lives

There are three places, chosen by what has to outlive what.

- **The swarm definition** is a JSON file on the host. It is the only thing `gnat create`, `agent create`, `agent set`, and `file add` touch, and it is read only at `gnat up`: the control plane is told the agent list and each agent's setup on its command line, and agent containers are created from it. That is why the definition can only change while the swarm is down. Profiles are copied into it at creation, or by `agent set`, and forgotten.
- **Logs, `events.jsonl`, and `control.db`** are a host directory bind-mounted into the control container, which runs as the host user so the files are the host user's. Each `up` appends to them. `control.db` is what lets a swarm that goes down come back up where it was: queued messages, which agents are paused, whether each agent was mid-turn, its last reply, its usage totals, how full its context window last was, and the account usage limits its harness last reported. The host reads it directly for `gnat status` while the swarm is down, so the host must be able to read the database without the control plane's code. Everything there is kept by agent name, so the definition gives each agent an ID when it's created, `up` tells the control plane each agent's ID with its setup, and the control plane records whose history each name holds. When it starts, it forgets the history of a name that has left the swarm or whose ID has changed, which means it was deleted and created again. `recorded` holds the rule, so `gnat status` while the swarm is down agrees with what coming back up would show. The ID is identity rather than a setting, so changing an agent's settings with `agent set` keeps its history.
- **Each agent's clone and harness sessions** are Docker volumes, so they survive `down`, a daemon crash, and a container restart. An agent that comes back up continues its session in the clone it left. Only `gnat delete` and `agent delete` remove them, and `agent set` removes the sessions when it changes the harness, since the new one can't continue them.

The repository mirror in the control container, the staged files, and the agents' secrets are deliberately not kept. They are made fresh at every `up`.

## Who is asking

Every request an agent makes to the control plane carries a secret that `gnat up` generated for that agent and put in its container's environment, where the daemon, the harness, the hook, and everything the agent runs inherit it. The control plane knows the agent by the secret and by nothing else: a message's sender, whose queue is taken from, whose output is logged, whose branches may be pushed, and who may merge are all whoever's secret the request has, and anything the request says about itself is overwritten. The same secret is git's password.

The operator has no secret. The operator's commands come in on the control container's Unix socket, which only a process in that container can reach, and are trusted completely. That split, network means an agent and socket means the operator, is the whole of the control plane's authorization. The operator can do things no agent can (ask after other agents, wait on them, stop them, send as the operator), and an agent can do only what concerns itself: send as itself, take its own urgent messages, report its own output, and ask GitHub for what it is allowed.

The web UI has no authentication. It is published on loopback and shows what the logs show. See the threat model in `CLAUDE.md` for why that is enough.

## GitHub by proxy

No agent container has a GitHub credential. The control plane holds the token and is the only place GitHub is reached, so it is the only place the rules can be enforced, and the rules are stated once, there.

Git goes through a bare mirror in the control container, served over smart HTTP to the swarm's network. An agent's clone has the control plane as its origin, with its secret as the password, and nothing in a request chooses the repository: whatever path the agent asks for, the one mirror answers. A fetch refreshes the mirror from GitHub first, so agents see each other's pushed work without the control plane relaying anything. A push lands in the mirror's pre-receive hook, which checks every ref against the rules and pushes on to GitHub itself, so the mirror never records a push GitHub refused, and the agent is told why. The rules are the agent's branch prefix (`SWARM/AGENT/`), no deletes, and no force. The prefix comes from the control plane's own knowledge of who pushed, never from the push.

Pull requests go the same way: `gnat-container pr` is a thin client that sends a request to the control plane, which creates, lists, comments, reviews, closes, edits, marks ready, or merges with its token. A pull request can only be opened from a branch under the requesting agent's prefix, which is how the web UI later knows which agent a pull request belongs to, and only one whose head is such a branch can be closed, edited, or marked ready by that agent. Merging is a per-agent permission that `gnat up` tells the control plane; the request can't ask for it.

Two consequences follow from one token. Every agent commits as the token's account, so authorship in the repository is the operator's, not the agent's, and a pull request's author is the operator too, which GitHub doesn't let approve it or ask for changes. So the control plane posts such a verdict as a comment, and records it in a label (`gnat:approved` or `gnat:changes-requested`), whoever the author, so what's ready to merge can be listed without reading reviews. And the token's scope is the swarm's scope: whatever the token can do to the repository, the control plane can be made to do by an agent within the rules above, so the token should be a narrow one.

Agent names are therefore branch-name components. The name pattern allows names git doesn't (such as `a..b`), and `gnat up` refuses a swarm whose names can't make branches, since those agents could never push.

## Turns and delivery

An agent runs one turn at a time, and a turn is one message. The control plane keeps each agent's queue in `control.db` and pushes the next message to the agent's daemon over the one long-lived connection the daemon holds; the daemon runs the harness on it and reports the events and the end of the turn on the same connection. There is no other channel from the control plane to an agent, so if the connection drops mid-turn, the turn is lost: the daemon ends it, and when it attaches again the control plane records the turn as interrupted. The busy flag is persisted, which is what makes a swarm that went down mid-turn report that turn interrupted when it comes back.

A message leaves the queue at the moment the agent becomes busy with it, so there is no instant at which a message is neither queued nor running. "Idle" means an empty queue and no turn running, and that is what `wait` waits for. Because agents message each other and a reply can arrive after a wait saw an agent idle, waiting on several agents means passing over them until a pass finds all idle and no turn has ended since the pass before. `wait --any` needs no passes: it waits on every agent at once, returns when one is idle, and reports that one with whichever others are idle at that moment.

Messages sent with `--now` are delivered through the harness's own hook mechanism: all harnesses run gnat's hook after every tool call, and the hook takes the urgent messages from the control plane and returns them as context. That is why mid-turn delivery happens only at tool calls, why a turn without tool calls never sees them, and why urgent messages still pending when a turn ends start the next turn ahead of the queue. The hook is given a short timeout and never fails the turn: a message it can't fetch waits for the next tool call. A message that starts a fresh session can only start a turn, so `--now` and `--new` exclude each other.

Stopping is a line on the same connection. The daemon kills the harness's process group (Claude), cancels the prompt (ACP), or interrupts the turn (Codex) and reports the turn stopped. Codex is killed with its process group if it cannot finish the interrupt. Because the daemon reads the stop before it reads any later turn, a stop can't outlive its turn and kill the next.

Pausing needs nothing from the daemon: the control plane stops handing out turns. A paused queue gives up no message, and pausing takes the queue's lock, which starting a turn holds while it marks the agent busy, so once a pause returns no turn starts and whether one is running is settled. The message a pause gives the running turn is held in memory, not queued, so the hook's next take delivers it and the turn's end drops it; it never becomes a turn of its own.

An agent's daemon is its container's main process, restarted by Docker on failure, and it clones before it attaches so a harness never runs in an empty workspace. A clone that is already there is kept and only repointed at the control plane. Files from `gnat file` are staged in the container by `up` and moved, not copied, into the clone by the daemon, so a restarted daemon doesn't overwrite the agent's edits to them.

## One event format

Every harness is translated, in the agent container, into the same event format before anything leaves it. The control plane, the readable logs, `events.jsonl`, `gnat wait`'s reply, usage totals, and the web UI know nothing of any harness's own output, and adding a harness touches nothing on the control plane's side. Events are maps rather than structs because they cross the wire and into files that readers of other builds, and the UI, must read while ignoring what they don't know.

The control plane adds its own events for what only it sees: a message arriving, a message sent to a teammate, and a turn ending. With those, `events.jsonl` is the whole history of a swarm, and the web UI is a pure function of it: the page replays every record, then follows new ones, and rebuilds each agent's transcript and totals itself. The server keeps no UI state. The readable log is a rendering of the same stream for `tail`. The host also reads `events.jsonl` through `recorded` for `gnat logs --replies`, which recovers complete final replies with their turn and task sender, even while the swarm is down.

## Harnesses

A harness is two halves that never meet. On the host it says what `gnat up` needs to set a container up: the program the image must have, the credential's environment variable, the user-level config files (the agent's instructions and settings that install the hook), and the directories where it keeps sessions, which become volumes. In the container it gives the daemon a runner that turns one message into events and an exit status, continuing the last session or starting a new one on request.

Session continuity is the harness's own, kept in its own files on its volume: Claude Code is run once per turn with `--continue`, and an ACP server is kept running across turns with its session ID saved to a file so a restarted daemon loads it. gnat doesn't keep or inspect sessions.

Codex also keeps one app-server process across turns. Its `.codex` volume holds
its own conversation files and gnat's current thread ID and cumulative usage
baseline. A restarted runner resumes that exact thread; a failed resume is an
error rather than a fresh conversation. All harness output is translated before
it leaves the agent container.

`gnat auth login codex` runs a separate temporary ChatGPT login and saves an
access-only snapshot in the host keyring. All Codex agents receive that snapshot
through `GNAT_CODEX_AUTH`, without a refresh token. They cannot rotate the shared
login. Renewal is an operator action: log in again, then bring affected swarms
down and up so they receive the replacement while keeping their sessions. The
control plane has no Codex authentication responsibilities.

What gnat tells every agent is facts: its teammates, how to message them, what the container has, that it has no GitHub credential, which branches it may push, and which pull request commands exist. Roles and workflow are left to profiles and the operator, because gnat assumes no team structure. Instructions are written into the harness's config directory at `up`, so changing them is a definition change, made while the swarm is down.

## Fixed formats

gnat is a port of the Python `swarm`, and these are compatible with it, so either implementation can read what the other wrote: the swarm definition JSON, `control.db`'s schema, `events.jsonl`, the JSON lines `gnat-container control status` prints, and the request and response lines of the wire protocol. JSON that Python wrote is written without HTML escaping, numbers are formatted as Python's `str` would, and command lines are parsed as argparse would. Oddities of that kind in the code are compatibility, not taste. The built web UI in `internal/web/dist` is checked in because the container binary embeds it, so a Go build needs no Bun.

## Boundaries that hold

- One repository per swarm, and an agent's branches are under `SWARM/AGENT/`. Both are relied on by the git rules, the pull request rules, and the UI's attribution of pull requests.
- `control` is a reserved name: it is the control container's network alias.
- The two binaries share only the small leaf packages. The container binary must stay a static cross-build with no host dependencies, and the host must read a down swarm from its files alone.
- Agents are cooperative. The control plane defends the token, the repository, and other agents' branches against mistakes and against instructions an agent picks up, not against an agent that attacks the control plane or its teammates. Hardening past that line is out of scope without a decision to move the line.
