# Usage

## Open a workspace

Run `gnat` from a committed local Git checkout to start/reuse the per-user daemon
and open that workspace in the browser. `gnat --no-open` prints its URL without
opening a browser. `gnat --help` and `gnat --version` are available.
`gnat daemon stop` shuts down the daemon while retaining all saved records.
Closing the terminal or browser leaves the daemon running.

A checkout root, subdirectory, and symlink alias reuse one swarm. Separate linked
user worktrees register separately. The browser links all registered workspaces
(focus an existing tab to refresh the list after registering another checkout)
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
  shows earlier transcripts. During an active turn the button first offers
  Interrupt before new conversation; wait for interruption to settle, then
  choose New conversation. Resume separately to release a paused queue. A failed
  exact resume never silently starts fresh.

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

### Integrating local work

Workers commit on their assigned branches. Their commits and branches are
immediately available in the original checkout because the worktrees share the
same Git repository. Ask the orchestrator to inspect and merge the desired
worker commits and run the project's checks there. For example:

> Have two workers implement the parser and its tests. Integrate their commits
> into this checkout with ordinary Git, preserve my local edits, and run the
> parser tests. Remove the workers after their work is integrated.

The orchestrator can use the branch from `list_agents` or a commit SHA received
in an ordinary message, then run `git merge --no-edit <worker-branch>` from the
original checkout. No push, remote, GitHub, or Gnat delivery command is needed.
These are ordinary Git operations chosen by the agents, not a required Gnat
workflow.

Git merges that create a merge commit require a clean index. When local changes
need to be set aside, ordinary `git stash push --include-untracked` and
`git stash pop --index` can preserve staged and unstaged work; inspect any
conflicts on restoration as usual. A failed merge can be resolved or aborted
with `git merge --abort`. Worker removal uses `git branch -d`, so merging the
worker branch with its ancestry allows normal cleanup; cherry-picking or
squashing may leave Git refusing branch deletion as unmerged. Refusals preserve
the branch and report the error for repair using ordinary tools.

## Reconnection and saved work

The browser retries initial connection failures and disconnected event streams.
Reconnection loads a consistent snapshot, then streams events after its cursor;
messages, worker membership, transcripts and reported usage reconstruct from
committed records. A network failure during send leaves an unacknowledged
request with a Retry button; retry reuses its request ID, preventing duplicate
input. Closing a tab does not cancel accepted input or running turns.

Daemon restarts use a new local listener URL. Run `gnat` again from the checkout
and use the returned URL; an old tab cannot discover a new port. A paused or
stopped swarm retains its workers, worktrees, conversations and queued messages.
Use Resume to continue pending input; issue new direction for interrupted work.
Switching workspaces changes only the view and leaves other swarms running.

New conversation resets only the orchestrator session, preserving its agent ID,
working directory, workers and queued input. Old transcripts remain selectable.
The fresh session receives bounded saved team/queue orientation and can inspect
prior conversations through `read_agent`; Gnat does not maintain a task summary.
Worker sessions remain independent and continue unchanged.

No relocation, unregister operation, automatic crash-process cleanup, or Git
inspection UI is included in this first cut. Keep saved checkout paths in place;
inspect and integrate Git work with ordinary local tools.
