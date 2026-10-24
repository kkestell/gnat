# Codex support review

## Scope and coverage

Reviewed the 22 staged files against
`agents/plans/2026-10-23-001-codex-support.md`, including credential import and
delivery, subprocess ownership, session persistence, cancellation, event and
usage translation, container packaging, profiles, documentation, and tests.
Traced the existing runner, daemon, and CLI integration around the changes.

This review leaves findings unfixed and the report unstaged, as required by the
repository's review instructions. Neither the implementation nor its staging
was changed.

Gaps:

- No fresh native browser login or live Docker swarm was exercised during this
  review. The work log records the implementation's earlier native smoke tests.
- Failure reproductions use a fake app-server and a fake login executable. The
  signal probe uses only synthetic credentials.

## Summary

| ID | Severity | Category | Fix | Title |
| -- | -------- | -------- | --- | ----- |
| F1 | Medium | Error handling | Trivial | An exited app-server can leave its turn busy |
| F2 | Medium | Resources | Trivial | Interrupting login bypasses temporary credential cleanup |

`Trivial` means the fix needs no additional product decision; both findings
remain unfixed.

## F1. An exited app-server can leave its turn busy

Medium · Error handling · Trivial · `internal/harness/codex.go:475`

The active receive loop detects server exit only through stdout EOF. If the
app-server exits while a descendant still holds its stdout open, there is no
EOF, and the loop does not observe the process completion channel. Active turns
also have no receive deadline until they are stopped. The agent therefore stays
busy until the descendant exits or the operator stops it, blocking normal turn
completion and subsequent queued messages.

### Evidence

A temporary Go test overlay extended the fake app-server to start `sleep 30`
with inherited stdout and then exit during a turn. The test observed the server
completion channel close, but `Run` remained blocked. It failed with:

```text
Run remained busy after app-server exited while a child held stdout open
```

The reproduction stopped the runner for cleanup. The overlay did not modify the
reviewed source or index. The existing server-exit fixture has no descendant
holding stdout and does not cover this case.

### Recommended fix

Observe subprocess exit during active protocol waits and terminate remaining
descendants when the server dies. Preserve handling of any already-buffered
completion message. Add a regression case where a child retains stdout after
the server exits.

## F2. Interrupting login bypasses temporary credential cleanup

Medium · Resources · Trivial · `internal/cli/codex_auth.go:21`

`loginCodex` relies on a deferred `RemoveAll` while waiting for the native login
process. The parent has no interrupt handler on this path, so terminal Ctrl+C
exits gnat without running that defer. This leaves the temporary Codex directory
behind. If the native login has already written `auth.json`, the leftover cache
contains the full native credential bundle, including its refresh token. This
violates the plan's requirement to always remove the temporary directory and
retain only the access-token snapshot in gnat's keyring.

### Evidence

Ran the built `gnat auth login codex` with a temporary fake Codex executable and
isolated `TMPDIR`. The executable wrote an `auth.json` containing synthetic
access and refresh tokens, then waited. Sending SIGINT to the foreground process
group produced:

```json
{"exit_status": -2, "auth_cache_remains_after_ctrl_c": true}
```

The probe removed its temporary files afterward and did not reach keyring
writes. Existing cancelled-login tests return an ordinary command error, which
runs deferred cleanup and misses abrupt parent termination.

### Recommended fix

Handle login interruption through a controlled return: stop the child, allow
temporary-directory cleanup to complete, and preserve any previously saved
credential. Cover actual parent interruption after the native cache is written.

## Checks run

- `go test ./internal/harness ./internal/cli` — passed.
- Focused inherited-stdout test through a temporary Go overlay — failed as
  expected, confirming F1; runner and child cleaned up.
- Process-group SIGINT login probe with synthetic credentials — confirmed F2;
  probe files cleaned up.
- Inspected the staged implementation, callers, tests, documentation, and pinned
  native protocol schemas for the behavior under review.

## Verdict

F1 and F2 need fixes before merging. Both have established intended behavior and
require no additional product decision. No fixes were applied, staged, or
committed during this review.
