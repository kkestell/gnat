# V2 harness neutrality review

## Scope and coverage

Reviewed the staged milestone-four implementation against
`agents/plans/2026-10-24-004-v2-harness-neutrality.md`: provider dispatch and
settings validation, Codex app-server and OpenCode ACP adapters, exact session
resumption, launch-scoped MCP, event translation, process ownership and
cancellation, mixed-team fixtures, live checks, and associated documentation.
Traced the unchanged core scheduler, coordination service, store events, and
browser usage rendering. Compared protocol handling with installed Codex JSON
schemas and the current official app-server and OpenCode ACP documentation.
The implementation run also passed live MCP calls for Claude Code and OpenCode
in both orchestrator and worker roles (four checks, 18.9 seconds).

Gaps:

- No live matrix was rerun during this review. The implementation run's Codex
  check reproduced successful MCP initialization followed by the installed Code
  Mode host timing out during tool discovery. Consequently, live Codex MCP
  coordination and the complete mixed-provider matrix remain unverified. This
  is a recorded integration limitation, not a reason to add helper management
  or compatibility infrastructure to Gnat's first-cut adapter.
- The mixed-team fixture exercises production dispatch and restart with real Git
  worktrees, but its provider protocols and replies are deterministic substitutes
  for model-driven collaboration. Final live results belong in the work log.

## Summary

| ID | Severity | Category | Fix | Title |
| -- | -------- | -------- | --- | ----- |
| F1 | Medium | Correctness | Fixed | Unsuccessful turns discard known token usage |

## F1. Unsuccessful turns discard known token usage

Medium · Correctness · Fixed · `internal/v2/harness/codex.go:148` and
`internal/v2/harness/opencode.go:116`

Codex checked the terminal turn status before emitting its collected token
usage. OpenCode checked `stopReason` before translating usage in the prompt
response. A provider failure or token-limit outcome therefore hid known usage
from the stored transcript, even when the provider had already reported it.
Claude's existing adapter emits usage before reporting its terminal error.

### Evidence

Added `TestNativeAdaptersFailedTurnUsage` using the existing provider fixtures.
The Codex fixture reports a failed turn; OpenCode reports `max_tokens`. Both
supply 25 input and 20 output tokens. Before the fix, both subtests failed with
`lost supplied usage for failed turn []`.

### Fix

Emit available usage once before interpreting the terminal outcome, preserving
the provider failure afterward. Persistence failures still propagate. The same
regression now confirms both the terminal error and the retained token values.
This follows the existing Claude behavior and required no new design decision.

## Checks run

- `go test ./internal/v2/harness -run TestNativeAdaptersFailedTurnUsage -count=1`
  — reproduced both failures before the fix.
- `go test ./internal/v2/harness -count=1` — passed after the fix, including the
  usage regression, exact resume, protocol failures, configuration validation,
  cancellation, and subprocess-child cleanup.
- Focused source and diff inspection — reviewed the staged changes and the
  surrounding callers described above.

## Verdict

F1 is fixed and covered by a regression. No other confirmed code findings remain
in the reviewed scope. Live Codex tool discovery and the full mixed-provider
workflow retain the coverage limit described above.
