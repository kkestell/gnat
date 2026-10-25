# Workflow consistency review

## Scope and coverage

Reviewed the staged changes for `agents/plans/2026-10-25-001-workflow-consistency.md`,
together with its work log: store recovery and turn-end text merging
(`internal/store/chat.go`, `store.go`, `coordination.go`), the `Swarm`, `State` and
`Queued` reads and their core callers, the `/state` route and asset serving in
`internal/daemon`, the Claude translator change, the React/Tailwind UI under `ui/`
(client refresh paths, transcript projection, cards, header, empty state), the
generated assets, the Makefile, and the documentation updates. I traced callers of
each changed store read, every event type that changes browser-visible records, the
SSE cursor path across deleted fragment sequences, and the Codex and OpenCode
fragment shapes that `mergeText` merges.

Gaps:

- No live provider turns and no visual browser inspection. Layout at desktop and narrow
  widths was checked only by reading the CSS and markup.

## Summary

| ID | Severity | Category      | Fix   | Title                                                         |
| -- | -------- | ------------- | ----- | ------------------------------------------------------------- |
| F1 | Medium   | Dependencies  | Fixed | `make test` and `make lint` fail in a fresh checkout          |
| F2 | Low      | Performance   | Fixed | Opening a workspace reads its full history three times        |
| F3 | Low      | Documentation | Fixed | Docs place Recheck harness beside every setup error           |
| F4 | Low      | Documentation | Fixed | Project instructions and a comment carry detour narrative     |

## F1. `make test` and `make lint` fail in a fresh checkout

Medium · Dependencies · Fixed · `Makefile:28`, `Makefile:34`

Only `make ui` installs the UI's dependencies. In a checkout without
`ui/node_modules`, which includes every new Gnat worker worktree, `make test` fails on
`react/jsx-runtime` and `make lint` fails because `tsc` and the React and Bun type
definitions are missing. README and AGENTS.md describe both targets as the standard
test and lint commands.

### Evidence

Extracted the staged `ui/` tree to an empty directory and ran `bun test` (1 failure:
`Cannot find module 'react/jsx-runtime'`) and `bun run typecheck` (`TS2688: Cannot
find type definition file for 'react'`).

### Fix

The `test` and `lint` targets run `bun install --frozen-lockfile` before `bun test` and
`bun run typecheck`, as `make ui` already does. The same fresh extraction then passed
55 tests and the typecheck.

## F2. Opening a workspace reads its full history three times

Low · Performance · Fixed · `internal/store/store.go:196`, `internal/daemon/http.go:88`

`gnat` registers the workspace through `OpenWorkspace`, which returned the full
snapshot with every event although the CLI uses only the swarm ID. The page handler
for `/swarms/{id}` then read the full snapshot again to render the workspace name and
ID, and the browser loaded it a third time on connect. At the work log's larger
history sizes each full read takes 60 to 244 ms before JSON encoding.

### Fix

`OpenWorkspace` returns the state without event history (`fullSnapshot(tx, id, false)`),
and the page handler reads `db.Swarm`. The browser's connect still loads the full
snapshot once. The store's concurrent-open test now also checks that the result has
no events.

## F3. Docs place Recheck harness beside every setup error

Low · Documentation · Fixed · `docs/usage.md:31`, `plugin/skills/gnat/SKILL.md:63`

`docs/usage.md` said Recheck harness appears beside setup errors, and the Gnat skill
said harness setup errors offer it. The button appears only on the orchestrator card,
when the orchestrator's lifecycle is `error` and scheduling is running
(`canAct(snapshot, "retry")`). A worker card with a setup error shows the error
without the button.

### Fix

Both documents now say the button appears beside the orchestrator's setup error while
scheduling is enabled. `docs/usage.md` also says it retries harness setup checks,
which matches `open(id, true)` rechecking every active agent.

## F4. Project instructions and a comment carry detour narrative

Low · Documentation · Fixed · `AGENTS.md:78`, `ui/src/swarm.ts:1`

The new AGENTS.md text ended with a warning against adding a separate preview renderer
or transcript data model, and the `swarm.ts` header described the module as recovered
from the original dashboard. The user's documentation conventions call for
declarative statements of the correct approach without warnings or history.

### Fix

AGENTS.md now states that every agent card, including workers, expanded workers and
the orchestrator, uses the same Transcript component and transcript data, and that
roles only change controls. The `swarm.ts` header reads "Transcript projection and
formatting shared by every agent card." `make ui` produced identical assets, because
minification strips comments.

## Checks run

- `go build ./... && go vet ./... && staticcheck ./... && test -z "$(gofmt -l .)" && go test -count=1 ./...`: passed before and after the fixes.
- `cd ui && bun test && bun run typecheck`: 55 tests passed; typecheck clean, before and after the fixes.
- `make ui`: regenerated assets match the staged `internal/daemon/assets` byte for byte, before and after the fixes.
- Fresh extraction of `ui/` without `node_modules`: `bun test` and `bun run typecheck` failed before the F1 fix and passed after `bun install --frozen-lockfile`.
- `make test` and `make lint`: passed after the fixes.
- `claude plugin validate .`: passed with the existing missing-version warning.

## Verdict

The change does what the plan describes. Recovery matches `Finish`, turn-end merging
keeps sequences and parents consistent with the browser's projection, the `/state`
path discards responses older than the client's cursor, and the Claude translator
change prevents duplicate replies after thinking blocks. The four findings were
fixed: build targets now work in fresh worktrees (F1), opening a workspace reads the
full history once instead of three times (F2), and the docs (F3, F4) are corrected.
The change is ready to commit.
