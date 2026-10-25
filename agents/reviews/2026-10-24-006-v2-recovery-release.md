# V2 recovery and release review

## Scope and coverage

Reviewed staged changes for
`agents/plans/2026-10-24-006-v2-recovery-release.md`: browser reconnection,
conversation reset and navigation, detached recovery and HTTP replay coverage,
team-preserving reset assertions, native build/install/release paths, and the
associated operating and plugin documentation. Traced browser actions through
the existing daemon, core and store transitions. No implementation commits
preceded this review.

Gaps:

- Browser checks execute the script in the existing deterministic VM harness;
  browser-cache restoration was simulated rather than exercised in a real
  browser.
- Did not rerun the provider-backed scenarios, broad Go validation, isolated
  installation smoke checks or release build; the implementation work log
  records those results. The original review carried forward a stale Codex
  helper limitation; the correction below records the resolved installation.
- Automatic crash-process cleanup, relocation, unregistering and Git inspection
  UI remain outside the accepted first-cut scope. Retained v1 source fixtures
  are not part of the native distribution.

## Summary

| ID | Severity | Category | Fix | Title |
| -- | -------- | -------- | --- | ----- |
| F1 | Medium | Resources | Fixed | Restored pages can retain an obsolete event subscription |

## F1. Restored pages can retain an obsolete event subscription

Medium · Resources · Fixed · `internal/v2/daemon/page.js:131`

When a snapshot fetch was still pending at page hide, restoring the page started
another connection attempt. Both attempts could subsequently open an
`EventSource`, overwriting the sole stored reference. Two subscriptions remained
open, and a later page hide closed only the most recent one. An obsolete failed
fetch could also close a healthy replacement subscription through the shared
reconnect path.

### Evidence

Reproduced the delayed-success case using the browser test harness: resolve the
initial snapshot only after page hide, restored page show and the replacement
snapshot completed. Before the correction, two streams remained open; hiding
the page again left one open. The original four browser cases passed without
covering that ordering.

### Fix

The parent implementation agent added a connection-attempt generation and
invalidates it when reconnecting, hiding the page or starting another attempt.
Superseded asynchronous results and stream callbacks cannot open or close the
current subscription. New attempts close the previous source and start a fresh
update chain. Two added regressions cover obsolete snapshot success and failure
after restoration. Verified the staged correction and reran all six browser
cases successfully.

Decision: invalidate superseded browser connection attempts using one generation
counter, because this keeps subscription ownership local to the existing
connection functions without introducing another connection manager.

Per the user's review instructions, the reviewer reported the finding and made
no implementation changes. The parent applied and staged the correction under
the user's explicit kauto implementation authorization.

## Checks run

- Inspected staged diffs and the dependent browser, daemon, core and store paths.
- Reproduced F1 with delayed snapshot completion in the VM browser harness.
- `bun test internal/v2/daemon/page.test.js` before correction: four cases passed,
  25 assertions.
- `bun test internal/v2/daemon/page.test.js` after correction: six cases passed,
  31 assertions, including both new restoration regressions.

## Verdict

One confirmed medium finding, F1, was corrected and verified. No remaining
findings in the reviewed milestone scope. The implementation meets the plan's
goal within its documented live-validation limits.

## Correction to the carried-forward Codex validation status

The milestone-four session's later turns replaced the broken Homebrew Codex
installation with OpenAI's standalone 0.164.0 package. Live MCP checks then passed
for Codex as orchestrator and worker in 15.15 seconds. Earlier references in this
review to an unresolved helper failure describe stale repository notes, not the
final session outcome. See the follow-up validation in
`agents/work/2026-10-24-004-v2-harness-neutrality.md`. This correction changes no
code-review finding and does not claim a rerun of the full mixed-provider matrix.
