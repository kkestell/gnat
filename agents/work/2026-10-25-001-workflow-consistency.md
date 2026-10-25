# Consistent recovery, merged streamed text, and visible team state

## Plan

`agents/plans/2026-10-25-001-workflow-consistency.md`

## Summary

Recovery now follows `Finish`: stopped swarms stay stopped, and interrupted
workers notify the orchestrator once. Finished and recovered turns merge each
run of streamed text into one `message` event. Core reads the swarm record or a
queued count instead of a full snapshot. The browser refreshes `/state` for
state changes and reloads history only on connect and `turn_ended`. The UI shows
scheduling with Resume, a membership-aware status, inline errors on every card,
an empty state, removed workers in the header, and cancelled input; the unused
dashboard code is gone. The recovered React/Tailwind UI is included. The plan's
goal is met.

## Departures from the plan

- Fixed the Claude translator in `internal/harness/claude.go`. The smoke check
  showed every reply that followed a thinking block stored twice: as streamed
  fragments and again as a `message`. Claude Code sends one `assistant` event
  per completed content block, so the text block's position in the event (0) no
  longer matched its stream index (1). The translator now skips a completed text
  block whose text equals the text streamed for that parent. Without this, merged
  history and `read_agent` would repeat every such reply.
  `TestTranslatorMatchesStreamedBlocksSentSeparately` covers it.
- The browser smoke check ran through the daemon's HTTP API and served bundle.
  The Chrome extension was not connected, so the layout at desktop and narrow
  widths was not inspected visually.

## Decisions

- `refreshState` keeps the client's own cursor and events and ignores state
  older than the client's cursor; `turn_ended` returns through `refresh`.
- On narrow screens the worker area without cards sizes to its content
  (`flex: 0 0 auto`), so the two-line empty state is not clipped. The
  removed-worker selector that the old 28 px basis made room for now lives in
  the header.
- The cancelled-message transcript test asserts the label only, because
  Markdown renders in a layout effect that static rendering does not run; the
  projection test covers the text and sender.

## Checks run

- `go build ./... && go vet ./... && staticcheck ./... && test -z "$(gofmt -l .)" && go test -count=1 ./...` — Passed.
- `cd ui && bun test && bun run typecheck` — 55 tests passed; typecheck clean.
- `make ui` (`bun run build`) — Regenerated `internal/daemon/assets`.
- `claude plugin validate .` — Passed with warnings (unchanged).

## Manual verification

1. Re-measured store reads with a scratch test (deleted afterward). Each turn
   streamed 30 tool calls, each preceded by 10 fragments with 2 KB results.
   Fragments merged at `Finish`.

   | Turns | Events streamed | Events stored | `Snapshot` | `State` | `Swarm` |
   | --- | --- | --- | --- | --- | --- |
   | 50 | 18,150 | 4,655 | 11.7 ms | 0.20 ms | 0.04 ms |
   | 249 | 72,237 | 23,162 | 60.7 ms | 0.47 ms | 0.04 ms |
   | 957 | 257,004 | 89,006 | 244 ms | 1.4 ms | 0.04 ms |

   Before this change, `Snapshot` took 29 ms, 115 ms, and 461 ms at the
   equivalent stored event counts, and it ran for every browser state change
   and in core hot paths. Tool events dominate this synthetic shape; turns with
   more prose merge more.

2. Smoke check with an isolated daemon, a disposable repository, and Claude
   Haiku.

   ```sh
   S=/tmp/gnat-smoke; mkdir -p $S/state $S/config/gnat $S/repo
   go build -o $S/gnat ./cmd/gnat
   printf 'orchestrator:\n  harness: claude\n  model: claude-haiku-5-5\n  effort: low\n' > $S/config/gnat/config.yaml
   (cd $S/repo && git init -q -b main && echo hi > README && git add README && git commit -qm init)
   cd $S/repo && XDG_STATE_HOME=$S/state XDG_CONFIG_HOME=$S/config $S/gnat --no-open
   # POST {"action":"send",...} to /api/swarms/<id>/actions with Origin and X-Gnat-Browser: 1;
   # read /api/swarms/<id>/events?after=0, /api/swarms/<id>, and /api/swarms/<id>/state.
   XDG_STATE_HOME=$S/state XDG_CONFIG_HOME=$S/config $S/gnat daemon stop
   ```

   The stream delivered 16 `text_delta` events live. After the turn, the
   stored history held them as one `message` at the first fragment's sequence.
   The first run also showed the duplicate reply fixed above. After rebuilding,
   the restarted daemon reopened the swarm, a second prompt stored one `message`
   (sequence 32; fragments 33–50 merged), and pause and resume changed `/state`
   scheduling to `paused` and back to `running`. The served `page.js` contained
   the empty state, Removed workers selector, cancelled label, and `/state`
   path. No smoke processes remained after `gnat daemon stop`.

## Follow-up work

- Inspect the layout in a browser at desktop and narrow widths.
- Full snapshots still load all history on connect and after each finished
  turn. If long sessions make that slow, load transcripts per agent instead.
