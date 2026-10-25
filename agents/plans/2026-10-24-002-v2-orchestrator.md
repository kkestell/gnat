# One local v2 orchestrator

## Goal

Implement milestone two of `agents/gnat-v2-spec.md`: `cd repo && gnat`
opens usable right-sidebar chat with one local harness in the original checkout.
Persist input, transcript, and exact session identity; support interruption,
orderly shutdown, and restart without replaying interrupted input.

## Related code

- `internal/v2/store/`: milestone-one SQLite schema and snapshots.
- `internal/v2/daemon/`: detached lifetime, authenticated CLI, embedded HTML.
- `internal/harness/claude.go`: existing stream-json translation and process groups.
- `agents/gnat-v2-spec.md` sections 5, 8–13 and milestone two define behavior.

## Decisions

- Implement Claude Code first through a neutral adapter interface; other configured
  harnesses receive an actionable unavailable error until milestone four. Use
  normal local credentials and project instructions, without rewriting either.
- Run `claude -p` per turn with configured model/effort, JSON streaming, and
  launch-scoped permission bypass for trusted local execution. Persist the
  provider's session ID before showing output and resume that exact ID, never
  the checkout's most recent session. Session creation is lazy on first input;
  installation/authentication checks establish readiness without spending a turn.
- Add version-two SQLite migration for messages, turns, durable request results,
  session state, and errors. Store normalized correlated events transactionally
  with transitions; retain full history and return snapshot plus cursor together.
- Put scheduling and controls in `internal/v2/core`, independent of HTTP and
  concrete adapters. Serialize transitions, run one turn per orchestrator,
  dequeue atomically, and stop scheduling after a failure/interruption until an
  explicit retry/resume. Pending input survives orderly shutdown; interrupted
  input is terminal and is never automatically replayed.
- Provide pause/resume, stop, interrupt, retry, and settled-boundary new conversation
  controls. Keep prior transcript inspectable and queued input intact on reset.
  A failed exact resume requires explicit reset rather than a silent new session.
- Extend the embedded page with external JavaScript/CSS and replayable SSE backed
  directly by committed SQLite events. Use same-origin browser mutations with a
  browser-only header and strict Origin/Host checks; keep CLI bearer authority
  separate. Use plain DOM text rendering, a resizable sidebar, and no UI build
  dependency. Leave the v1 React dashboard and worker/MCP features untouched.

## Test plan

- SQLite migration preserves milestone-one IDs/settings; request replay,
  cross-swarm isolation, FIFO claim, transactional state/events, reset/history,
  crash recovery, and snapshot/cursor consistency.
- Injected adapter tests cover serialized turns, queued input, controls,
  interruption, failure/retry, restart, and independence of two swarms.
- Claude fixture subprocess tests cover explicit cwd/session flags, streamed
  text/tools/usage, errors, exact resume, and process-group termination/reaping.
- HTTP tests cover browser authority, durable acceptance, SSE cursor replay,
  invalid origins/hosts, and independent swarm routes.
- Exercise the built detached binary in a disposable local repository using real
  Claude credentials: first prompt, follow-up after restart, original checkout
  edits, interruption, and retained transcript. Check UI JavaScript and inspect
  the rendered page with browser tooling if available.

## Implementation plan

1. Extend store schema, transitions, snapshot/history, and focused store tests.
2. Add neutral core scheduler and local Claude adapter with focused tests.
3. Compose runtime into daemon startup/shutdown; add browser API/SSE and usable
   sidebar chat, controls, error recovery, and replay.
4. Update architecture, spec status, AGENTS.md, README, setup/usage, and plugin
   source-build guidance. Record validation and any departures in the work log.
