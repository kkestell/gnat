# One local v2 orchestrator

## Scope and coverage

Reviewed the complete staged milestone-two change against
`agents/plans/2026-10-24-002-v2-orchestrator.md` and the v2 specification,
including store migration and transitions, the scheduler, Claude subprocesses
and protocol translation, HTTP/browser authority, event replay, chat rendering,
callers, tests, and related operating documentation. Applied the accepted
first-cut constraints: one trusted macOS user, Claude first, native delegation
unchanged, and no automatic cleanup of surviving processes after daemon crashes.

Gaps:

- Did not independently repeat the implementer's live Claude/browser checks or
  repository-wide validation recorded in the work log. Independently exercised
  the scheduler's control races with deterministic temporary adapter tests.
- Worker management, other harnesses, and deferred distribution cleanup are
  outside milestone two and were not reviewed as implemented behavior.

## Summary

| ID | Severity | Category | Fix | Title |
| -- | -------- | -------- | --- | ----- |
| F1 | Medium | Concurrency | Fixed | Delayed interrupt can cancel a subsequent turn |
| F2 | Medium | Concurrency | Fixed | Resume during cancellation is acknowledged and then lost |

Findings were presented before implementation edits. The parent task then
implemented the fixes under the user's explicit autonomous `kauto` instruction;
this reviewer independently checked the corrections without editing code.

## F1. Delayed interrupt can cancel a subsequent turn

Medium · Concurrency · Fixed · `internal/v2/core/core.go:97`

Before correction, Interrupt cancelled whichever execution was active when the
request reached the service. The browser, HTTP input, core API, and active
execution record carried no intended turn ID. If a user interrupts turn A near its end, a queued turn B can
start before the request is handled; B is then interrupted and its delivered
input cannot be replayed automatically. The specification explicitly requires
interrupt requests to target a specific turn so late requests cannot terminate
its successor (`agents/gnat-v2-spec.md:479`).

### Evidence

A temporary `TestReviewDelayedInterruptCancelsSuccessor` used the existing fake
adapter to start A, queue B, capture the intended A turn ID, finish A, wait for B
to start, then deliver the interrupt. It failed because B's `ended` channel
closed. The existing request-replay test covers an interrupt already accepted
for A, but not the first delivery of an initially delayed request.

### Fix

The browser now includes its intended `turn_id`; HTTP and core pass it to the
store. The store checks for a matching active turn in the mutation transaction
before changing scheduling. Stale or missing targets are rejected, and target
identity is part of the durable operation input. Accepted interrupt replays
still return their original results without cancelling later turns. Stop keeps
its swarm-wide semantics. The permanent
`TestInterruptTargetsOnlyNamedTurn` covers late first delivery against a running
successor; the existing control/restart test continues to cover accepted replay.
The browser API test verifies rejection of a missing target and propagation of a
valid target to the running execution.

## F2. Resume during cancellation is acknowledged and then lost

Medium · Concurrency · Fixed · `internal/v2/core/core.go:101`

After Interrupt or Stop is accepted, cancellation cleanup can still be running.
Before correction, Resume/Retry accepted in that interval set scheduling to
`running`, but the service skipped preparation because an execution remained active. When that turn
finishes, `Store.Finish` unconditionally changes non-stopped scheduling to
`paused` for interruption (`internal/v2/store/chat.go:338`). Its completion path
then skips scheduling because the turn did not complete successfully. The
accepted Resume is lost and queued input stays blocked until another action.
The same race exists while a cancelled readiness probe is settling.

### Evidence

A temporary `TestReviewResumeAfterAcceptedInterrupt` used a fake adapter that
waited on a gate after observing cancellation. With another message queued, it
accepted Interrupt and Resume, verified scheduling was `running`, then released
the adapter. It failed with `accepted Resume lost: scheduling=paused,
queued=queued`. The UI leaves these controls available during settlement.

### Fix

Resume/Retry now waits for a cancelled execution's completion channel before
committing its scheduling transition. The service releases its mutex while
waiting so cancellation can finish, then reacquires it and checks again before
running the existing durable operation path. Preparation also has a completion
channel, so cancelled readiness probes settle through the same mechanism.

The parent chose waiting rather than the review's proposed rejection to retain
the existing single durable request lookup and avoid an additional deferred
resume state. Resume is acknowledged only after cancellation has settled.
`TestResumeWaitsForInterruptionToSettle` holds cleanup behind a gate, verifies
that resume is not acknowledged early, then confirms the queued successor runs
and scheduling remains `running`.

## Decisions

- Retained the milestone's small architecture and accepted crash-cleanup scope;
  the findings concern reachable ordinary controls, not deferred robustness.
- Recommended explicit target identity for Interrupt because the specification
  already defines that contract; Stop retains its distinct swarm-wide behavior.
- The parent chose to wait for cancellation settlement before accepting
  Resume/Retry, preserving the existing durable request lookup without adding
  deferred scheduling state. This replaces the initial recommendation to reject
  the transition.
- The reviewer left implementation and existing staged files untouched; the
  parent made the authorized fixes, and the reviewer staged only this report.

## Checks run

- Temporary `go test ./internal/v2/core -run TestReview -count=1` — both targeted
  reproductions failed as described above; the temporary test file was removed.
- `go test ./internal/v2/core -run 'TestQueueControlsAndRestart|TestSwarmIndependenceAndFailure' -count=1`
  — passed after removing the temporary reproduction file.
- `go test -race ./internal/v2/core ./internal/v2/store ./internal/v2/daemon`
  — passed with the permanent regression tests and corrected request path.
- `go test ./internal/v2/daemon -run TestBrowserChatAndEventReplay -count=1`
  — passed with the new end-to-end target propagation assertions.
- Assessed the work log's additional live orderly-shutdown evidence: a detached
  daemon stopped during a real Claude Bash `sleep 120`, reaped the managed
  harness, preserved queued input, and delivered only that queue after restart
  and explicit Resume. This was implementer-provided evidence, not an
  independently repeated paid call.
- Inspected the complete staged diff and traced actions through the browser,
  HTTP decoder, durable operation replay, scheduler, and final state writes.

## Verdict

Milestone two covers the intended local orchestrator workflow. Both confirmed
control races, F1 and F2, are fixed; no findings remain open. No additional
implementation or architecture changes are required by this review.
