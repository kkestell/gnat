# Consistent recovery, merged streamed text, and visible team state

## Goal

Fix the problems that affect ordinary sustained use of Gnat, and commit the
recovered React/Tailwind UI with them.

- Crash recovery turns a stopped swarm into a paused one and does not tell the
  orchestrator that a worker was interrupted.
- Every streamed text fragment is a durable event. History grows by hundreds of
  rows per turn, `read_agent` returns sentence fragments, and the full snapshot
  (all events) is rebuilt in core hot paths and refetched by the browser on every
  state change.
- The UI hides swarm scheduling, shows lifecycle where membership matters,
  hides compact-card errors in tooltips, has no empty state, overlays removed
  worker history on the cards, omits cancelled messages, and carries dead code
  from the old dashboard.

When the work is done, finished turns store one `message` event per text block,
hot paths read only the records they need, browser state changes refresh state
without history, and the UI shows scheduling, status, errors, and cancelled
input plainly.

## Related code

- `internal/store/chat.go` — `fullSnapshot`, `ClaimAgent`, `Append`, `Finish`,
  `Recover`; `Finish` already notifies the orchestrator for interrupted workers.
- `internal/store/store.go` — `readSnapshot` (swarm record without history),
  `Snapshot`, `List`.
- `internal/store/coordination.go` — `notify`, `enqueue`, `cancelQueue` (emits
  `message_cancelled`), `ReadAgent`.
- `internal/core/core.go`, `internal/core/coordination.go` — `Snapshot` calls in
  `open`, `prepare`, `Mutate`, `schedule`, `Coordinate`, `provision`, `remove`,
  and `instructions`.
- `internal/daemon/http.go`, `internal/daemon/chat.go` — snapshot route and SSE
  handler.
- `ui/src/client.ts` — `refresh` on every transition event and after `deliver`.
- `ui/src/swarm.ts`, `ui/src/transcript.tsx`, `ui/src/App.tsx`,
  `ui/src/controls.ts` — transcript projection, rendering, cards, controls.
- `ui/test/*.test.ts(x)` — client, projection, and rendering tests.

## Decisions

- **Merge streamed text at turn end, in the store.** Streamed `text_delta` events stay
  durable while a turn runs, so live streaming and reconnection work unchanged.
  `Finish` and `Recover` call `mergeText(tx, swarm, turn, from)` in the same
  transaction as the turn outcome. It walks the turn's events in sequence and
  replaces each contiguous run of `text_delta` events with the same
  `parent_tool_use_id` by one `message` event: the first event of the run is
  updated in place to `{"type":"message","text":<joined>}` (keeping
  `parent_tool_use_id`) and the rest are deleted. The UI already projects a
  `message` exactly as it projects a delta run, so transcripts render the same.
  `ClaimAgent` records the `turn_started` sequence on the `Claim` (`Start`), and
  `mergeText` reads `WHERE swarm_id=? AND sequence>=? AND turn_id=?` to use the
  existing `events_swarm_sequence` index. `Recover` passes the turn's minimum
  sequence. Adapters are unchanged.
- **Sequence gaps are allowed.** Clients deduplicate by sequence and never
  require contiguity. A client that had not yet polled the last fragments before
  merging is corrected by the full refresh it performs on `turn_ended`.
- **Swarm record versus snapshot.** `Store.Swarm(id)` returns `readSnapshot`
  (identity, scheduling, orchestrator, worker defaults, cursor) without
  collections. Core uses it wherever it only needs those fields.
  `Store.Queued(agentID)` counts queued messages for `instructions`. The SSE
  handler checks existence with `Swarm`.
- **State without history for the browser.** `Store.State(id)` is the snapshot
  without events (`fullSnapshot(tx, id, false)`), served at
  `GET /api/swarms/{id}/state`. The client appends each transition event to its
  own events, keeps its own cursor, and replaces the other snapshot fields from
  `/state`. `turn_ended` and initial connect/reconnect still load the full
  snapshot. A failed state refresh takes the existing reconnect path.
- **Recovery follows `Finish`.** `Recover` keeps `stopped` scheduling (`CASE
  WHEN scheduling='stopped' ...`, as in `Finish`), pauses otherwise, merges the
  turn's streamed text, and for a worker enqueues `Worker <name> (<id>): interrupted: Daemon
  restarted during execution` through `notify`. Each running turn is recovered
  once because its state changes, so repeated recovery adds nothing.
- **Status label.** `agentStatus(agent)` in `swarm.ts` returns the membership
  with underscores replaced by spaces when membership is not `active`, otherwise
  the lifecycle. Every card uses it.
- **Scheduling in the header.** When scheduling is `paused` or `stopped`, the
  header shows "Paused" or "Stopped" and a Resume button. Resume leaves the
  orchestrator's ••• menu, which keeps Pause, Stop, Interrupt, and New
  conversation.
- **Errors in the shared card.** `AgentCard` renders `agent.error` below its
  header for every card, with an optional `errorAction` (the orchestrator's
  Recheck harness button). The separate error blocks in `WorkerDetail` and
  `Orchestrator` go away.
- **Removed workers in the header.** The "Removed workers" selector moves from
  the absolute overlay in `main` into the header after the workspace path.
- **Empty state.** With no workers and nothing selected, `main` shows "Your
  workspace is ready. Direct the orchestrator in the chat sidebar."
- **Cancelled input.** The `message` item replaces `midTurn` with an optional
  `cancelled` flag. `AgentState.apply` handles `message_cancelled` by pushing
  the looked-up message with `cancelled: true`; `ItemView` labels it "From
  <sender> (cancelled before delivery):".
- **Dead code removed.** `ContextView`, `LimitsView`, `Meter`, `parseLimits`,
  `limitWindows`, `resetTime`, `percent`, `contextSummary`, the `Limits` and
  `ContextWindow` types, `AgentState.context` and its `context` case, the `sent`
  and `session` items, and `midTurn`. No adapter produces these.
- **Verification.** Unit tests plus one manual smoke check with the built
  binary. No browser automation harness.

## Test plan

- `internal/store/chat_test.go`:
  - A worker turn running at restart: `Recover` interrupts it, queues exactly one
    system message to the orchestrator naming the worker, and a second `Recover`
    adds none.
  - A swarm stopped while a turn was running stays `stopped` after `Recover`; an
    unstopped one becomes `paused`.
  - `Finish` merges `text_delta` runs: two fragments, a `tool_call`, then a
    fragment with a different `parent_tool_use_id` become two `message` events
    with joined text and the original parent; the `tool_call` keeps its
    sequence. `Recover` merges an interrupted turn the same way.
  - `ReadAgent` after `Finish` returns the joined message, not fragments.
  - Existing `TestChatTransitionsAndRecovery` expectations change only where
    merging removes the `partial` fragment event.
- `internal/store`: `Swarm` returns the record without collections; `State`
  returns messages, turns, agents, and conversations with empty events; `Queued`
  counts only queued input for the agent.
- `internal/daemon/http_test.go`: `GET /api/swarms/{id}/state` returns the state
  without events and 404 for an unknown swarm.
- `ui/test/client.test.ts`: a non-`turn_ended` transition requests `/state`,
  keeps existing events, appends the transition event, and keeps the client
  cursor; `turn_ended` loads the full snapshot; a failed `/state` request
  reconnects; existing reconnect, acknowledgement, and reset tests still pass.
- `ui/test/swarm.test.ts`: `agentStatus` for provisioning, provisioning_failed,
  removing, removed, and active lifecycles; `message_cancelled` produces one
  cancelled message with text and sender; a merged `message` projects the same
  items as its delta run.
- `ui/test/transcript.test.tsx`: a cancelled message renders its sender, text,
  and "cancelled before delivery".

## Implementation plan

1. `internal/store/chat.go`: add `Start int64` to `Claim`, set from the
   `turn_started` insert in `ClaimAgent`. Add `mergeText` and call it in
   `Finish` before `turn_ended` and in `Recover` per turn. Change `Recover` to
   read the agent's role and name, preserve `stopped`, and call `notify` for
   workers. Give `fullSnapshot` an `events bool` parameter and add `State`.
2. `internal/store/store.go`: add `Swarm`; update `fullSnapshot` callers.
   `internal/store/coordination.go`: add `Queued`.
3. `internal/store/chat_test.go` (and a small store test for `Swarm`, `State`,
   `Queued`): add the cases above.
4. `internal/core/core.go`, `internal/core/coordination.go`: replace `Snapshot`
   with `Swarm` in `open`, `prepare`, `Mutate`, `schedule`, `Coordinate`,
   `provision`, and `remove`; use `Queued` in `instructions`.
5. `internal/daemon/http.go`: add `GET /api/swarms/{id}/state`.
   `internal/daemon/chat.go`: check existence with `Swarm`. Extend
   `internal/daemon/http_test.go`.
6. `ui/src/client.ts`: add `refreshState`, used for transitions other than
   `turn_ended` and after `deliver`; keep `refresh` for connect and `turn_ended`.
   Update `ui/test/client.test.ts`.
7. `ui/src/swarm.ts`: add `agentStatus`, the `cancelled` message flag and the
   `message_cancelled` case; remove the dead code. `ui/src/transcript.tsx`:
   render cancelled messages; remove `sent` and `session` cases. Update
   `ui/test/swarm.test.ts` and `ui/test/transcript.test.tsx`.
8. `ui/src/App.tsx`: header scheduling and Resume, header removed-worker
   selector, empty state, `agentStatus` and shared error line in `AgentCard`,
   Resume removed from the menu, dead components removed. Run `make ui` to
   regenerate `internal/daemon/assets/page.js` and `page.css`.
9. Re-measure with a scratch (uncommitted) store benchmark at the review's
   history sizes, comparing `Snapshot`, `State`, and `Swarm`, plus event counts
   after merging. Record the numbers in the work log.
10. Smoke check: build the binary; with isolated `XDG_STATE_HOME`,
    `XDG_CONFIG_HOME`, and a disposable committed repository, configure a cheap
    Claude model, open the workspace, send one short prompt, and confirm
    streaming, the merged transcript after the turn, the empty state, Pause →
    header "Paused" → Resume, and a narrow window. Stop the daemon and record the
    result in the work log.
11. Include the existing uncommitted UI recovery: `ui/`,
    `internal/daemon/assets/`, `internal/daemon/page.html`, the removed
    `internal/daemon/page.{css,js,test.js}`, `internal/daemon/http.go`,
    `internal/daemon/http_test.go`, harness message wording, `Makefile`,
    `AGENTS.md`, `README.md`, `docs/setup.md`, `docs/usage.md`, and
    `plugin/skills/gnat/SKILL.md`.

## Documentation updates

- `agents/architecture.md` — turn-end merging of streamed text, `Swarm`/`State` reads,
  the `/state` refresh path, and recovery notifications preserving `stopped`.
- `docs/usage.md` — header scheduling with Resume, status labels, and cancelled
  input in removed workers' transcripts.
