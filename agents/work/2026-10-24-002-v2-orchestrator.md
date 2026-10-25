# One local v2 orchestrator

## Plan

`agents/plans/2026-10-24-002-v2-orchestrator.md`

## Summary

Milestone two supplies local Claude Code chat in the original checkout, durable
FIFO input and transcript, exact sessions across restart, replayable browser
updates, interruption, scheduling controls, and explicit conversation reset.
The live local-checkout workflow and browser interaction both passed.

## Departures from the plan

- No functional scope departures. Reused v1 protocol concepts through a small
  new translator rather than importing its container-oriented harness registry.
- Session creation is lazy on first input, as decided in the plan; readiness
  checks installation and authentication without making a paid model request.

## Decisions

- Read committed SQLite events on a short SSE timer rather than adding a second
  publication/subscription system. Snapshots and event cursors remain authoritative.
- Attribute streamed blocks by parent and block index, suppressing only matching
  full blocks; retain native subagent output without changing delegation policy.
- Bind interrupt to the observed turn ID, including that identity in durable
  request replay, so delayed requests cannot cancel a later turn.
- Wait for cancellation settlement before accepting Resume/Retry. This keeps
  the acknowledged scheduling state from being overwritten and avoids adding
  a deferred-resume record or a second operation lookup.
- Use process groups and wait for child exit concurrently with stdout reading so
  tool subprocesses cannot keep a crashed harness's output pipe open forever.

## Checks run

- `go test -race ./internal/v2/...` — passed, including detached macOS processes,
  schema migration, transaction failures, queue transitions, isolation, session
  continuity, and HTTP/event replay boundaries.
- `go build ./...`, `go vet ./...`, `staticcheck ./...`, repository-wide gofmt
  check, and `go test ./...` — passed.
- `node --check internal/v2/daemon/page.js` — passed.
- Focused adapter checks after refining streamed-block deduplication — passed.
- Review confirmed two Medium control races. Both were presented in commentary
  and corrected under the explicit autonomous kauto workflow; permanent tests
  cover delayed first interrupt delivery and gated cancellation settlement.
  `go test -race ./internal/v2/...` passed after the fixes.

## Manual verification

1. Built `/tmp/gnat-v2-m2` with `go build -o /tmp/gnat-v2-m2 ./cmd/gnat`.
   `/tmp/gnat-m2-live.py` created a disposable committed repository with isolated
   `XDG_STATE_HOME`/`XDG_CONFIG_HOME`, configured `claude`/`sonnet`/`high`, and
   invoked the binary's `--no-open` entry. Normal local Claude 2.1.310 credentials
   authenticated without copying or rewriting configuration.
2. Sent a prompt remembering `marigold`; stopped/restarted the detached daemon;
   sent a follow-up asking it to write that word to `proof.txt`. The exact saved
   session ID remained unchanged and the original checkout contained the file.
   A subsequent long-running turn was interrupted and retained in history.
3. `/tmp/gnat-m2-browser.cjs` used Playwright Chromium against the detached
   daemon's actual browser URL. Sent `BROWSER_OK` through the UI, reloaded, and
   compared transcripts for equality. Reset the conversation and inspected its
   previous history. Desktop (1440×960) and mobile (390×844) screenshots were
   visually inspected; the composer remained accessible and no page errors fired.
4. `/tmp/gnat-m2-shutdown.py` stopped the detached daemon while a real Claude
   Bash `sleep 120` tool was active, verified the managed harness PID was reaped,
   and restarted. The interrupted turn stayed terminal and a second queued
   message stayed pending. Explicit Resume delivered only that queued message.
   The disposable verification daemon was then stopped. A final browser check
   against the rebuilt binary also passed early control clicks during delayed
   initial loading, stale interrupt rejection, and retained live history; its
   daemon was stopped afterward.
5. Reviewed the current official [Claude CLI reference](https://code.claude.com/docs/en/cli-reference)
   and [programmatic execution documentation](https://code.claude.com/docs/en/headless)
   against installed behavior for streaming, exact resume, authentication status,
   local configuration, and launch-only permission flags.

## Follow-up work

- Dynamic workers/MCP remain milestone three; other adapters remain milestone four.
- Release/distribution cleanup and expanded recovery testing remain milestone six.
  The first cut still requires user cleanup of surviving processes after a daemon
  crash. Existing non-Claude swarms preserve settings and show adapter errors.
