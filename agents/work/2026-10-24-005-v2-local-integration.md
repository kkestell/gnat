# V2 local integration work log

## Plan

`agents/plans/2026-10-24-005-v2-local-integration.md`

## Summary

Added deterministic real-Git integration coverage and an opt-in live workflow
using production adapters, browser input, and authenticated MCP. Two workers
commit independent changes; the orchestrator merges and verifies them in the
original checkout, preserves local work, and removes the integrated workers.
Conflict coverage checks merge abort, unmerged branch retention, ordinary Git
resolution, and removal retry. No production runtime change was needed.

## Departures from the plan

- Git refuses a merge that creates a merge commit when unrelated staged changes
  are present. Integration therefore explicitly stashes local work and restores
  it with `--index`, verifying both the index and working-tree contents afterward.
- The first live attempt completed integration, but the test waited for a
  `message` event while Claude emitted `text_delta` fragments and timed out. The completion
  check now assembles text by turn and handles both normalized output forms.
- A second live attempt integrated and verified both changes, but one worker
  inherited orchestrator effort because its creation call omitted the requested
  effort. The fixture now configures worker defaults through the existing
  settings API so this Git scenario does not depend on redundant model arguments.

## Decisions

- Share the existing live test's disposable repository, HTTP/MCP service, browser
  send, wait, and restart setup. Retain separate coordination and integration
  scenarios so the local Git exercise does not depend on the full harness matrix.
- Keep conflict resolution in ordinary Git; the regression uses `-X theirs` only
  to select the fixture's expected content and establish merge ancestry for safe
  branch removal. Gnat adds no resolution policy.

## Checks run

- `go test ./internal/v2/...` — passed.
- `go test -race ./internal/v2/...` — passed.
- `go vet ./internal/v2/...` — passed.
- `go build -o /tmp/gnat-v2-milestone-five ./cmd/gnat` — passed.
- Changed-Go gofmt check and `git diff --check` — passed.

## Manual verification

```sh
GNAT_LIVE_TEST=1 GNAT_LIVE_WORKER=opencode go test ./internal/v2/daemon -run '^TestLiveLocalIntegration$' -v -timeout 12m
```

Passed in 34 seconds: both worker commits were merged and independently checked,
local work was preserved, both worktrees/branches were removed, and message
history remained available.

Uses a disposable local repository, a Claude orchestrator, two OpenCode workers,
and ordinary provider credentials. The test checks merged commit ancestry,
functional Go tests, staged/unstaged/untracked local work, retained result
messages, and safe worker-resource removal. No new full harness matrix was
required. Correction: the earlier Codex helper failure had already been resolved
in the milestone-four session, where live MCP checks passed for both Codex roles;
the original repository notes had omitted that follow-up.

## Review

`agents/reviews/2026-10-24-005-v2-local-integration.md` found one low-severity
status inconsistency in the specification. Updated its current-implementation
statements to milestone five; no behavioral defects or new design decisions
were identified.
