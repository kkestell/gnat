# V2 local integration review

## Scope and coverage

Reviewed the staged milestone-five changes against
`agents/plans/2026-10-24-005-v2-local-integration.md` and the v2 specification's
local-integration contract and acceptance criteria 6–8. Traced the new
deterministic and opt-in live scenarios, extracted live-team setup, scheduler
and message delivery, worker Git allocation/removal, and associated operating
and architecture documentation. The change is proportionate: existing shared
worktrees already support integration, so regression coverage and live
validation need no new production service or integration policy.

Gaps:

- No provider-backed scenario was independently rerun by the reviewer. The
  parent completed the corrected Claude/OpenCode live scenario in 34 seconds
  and recorded its passing result in the work log. The reviewed assertions
  check merged ancestry, functional behavior, local-change preservation,
  commit messages, and worker cleanup.
- Codex's previously recorded live tool-discovery limitation and milestone-six
  recovery/release work remain outside this change's scope.

## Summary

| ID | Severity | Category | Fix | Title |
| -- | -------- | -------- | --- | ----- |
| F1 | Low | Documentation | Fixed by parent | Specification still reports milestone four |

## F1. Specification still reports milestone four

Low · Documentation · Fixed by parent · `agents/gnat-v2-spec.md:3` and
`agents/gnat-v2-spec.md:13`

The specification's status and current-implementation introduction both said
only milestones one through four were implemented. Its statement that the
remaining milestones were design specifications conflicted with the new
milestone-five completion claims in README, repository instructions, and both
plugin skills. A reader using the specification to identify remaining work
received an outdated answer.

### Evidence

Compared both current-implementation statements with the staged documentation
updates and the specification's milestone-five definition at line 802.

### Fix

The parent updated both status statements to include milestone five, recorded
ordinary local integration, and preserved the existing Codex validation caveat.
Verified the correction in the staged diff. No design decision was needed;
the reviewer did not modify or stage implementation files.

## Checks run

- `go test ./internal/v2/core -run '^TestLocalIntegration' -count=1` — passed,
  including combined functional verification, preservation of both index and
  worktree edits plus untracked content, conflict abort, refusal to delete an
  unmerged branch, and successful cleanup after ordinary Git resolution.
- `git diff --cached --check` — passed.
- Focused staged-diff and caller inspection — covered the new tests, shared
  live setup, unchanged scheduler/coordination and Git cleanup, documents, and
  the parent's F1 correction and final live result record.

## Verdict

The milestone-five implementation fits the approved architecture and adds no
unnecessary runtime machinery. The parent fixed the single low-severity finding
F1; no behavioral defect was confirmed.

## Correction to the carried-forward Codex validation status

The milestone-four session's later turns replaced the broken Homebrew Codex
installation with OpenAI's standalone 0.164.0 package. Live MCP checks then passed
for Codex as orchestrator and worker in 15.15 seconds. Earlier references in this
review to an unresolved helper failure describe stale repository notes, not the
final session outcome. See the follow-up validation in
`agents/work/2026-10-24-004-v2-harness-neutrality.md`. This correction changes no
code-review finding and does not claim a rerun of the full mixed-provider matrix.
