# V2 recovery and release

## Goal

Complete milestone six with exercised durable recovery, browser reconnection,
conversation reset and independent swarms, plus native macOS installation and
release paths and operating documentation matching the current runtime.

## Related code

- `internal/v2/daemon/page.js`, `page.html`, `chat.go` — browser snapshots,
  event replay, controls and workspace selection.
- `internal/v2/daemon/lifecycle_test.go`, `harness_test.go` — detached daemon
  lifecycle and production adapters with deterministic subprocess fixtures.
- `internal/v2/core/core_test.go`, `coordination_test.go` — scheduler, reset,
  saved sessions and team preservation.
- `Makefile`, `.github/workflows/release.yml` — build/install/release paths.
- `plugin/skills/{install,gnat}/SKILL.md` — agent-facing public instructions.

## Decisions

- Reconnect by loading an authoritative snapshot and subscribing after its cursor;
  retry initial connection failures too. Keep the current ephemeral listener;
  after daemon restart the user reopens the new URL with `gnat`.
- Offer interruption as the first step of an active conversation reset, then
  require a settled turn before exposing the actual reset. Preserve the existing
  separate runtime actions and their durable request IDs.
- Refresh workspace navigation on window focus to discover newly registered
  swarms without adding global event subscriptions or changing scheduling.
- Retain legacy source packages and their fixtures for now, but remove container
  helper builds and profile installation from native distribution targets. Ship
  only the native binary with embedded v2 assets and an injected release version.
- Keep the existing macOS arm64 release architecture; macOS source installs use
  the host architecture. Do not publish a release or replace the user's install
  while validating: use an isolated install directory and disposable state.
- Use deterministic subprocess adapters for repeatable recovery coverage and a
  small real-provider reset/restart exercise; distinguish individual live role
  checks from the complete mixed-provider coordination matrix.

## Test plan

- Browser script tests: initial failure, dropped stream and failed transition
  refresh recover from snapshots without duplicated transcript or usage; cross
  swarm events are ignored; reset offers interruption; focus updates navigation.
- HTTP replay tests: snapshot/subscription gap, Last-Event-ID continuation,
  disconnected-client work, independent swarm streams and matching snapshots.
- Real macOS detached subprocess tests: active shutdown/reaping, saved input and
  sessions across disk reopen, crash recovery with no interrupted-input replay,
  independent second swarm and explicit resume.
- Core team reset regression: identity/worktrees/queued input/history retained,
  fresh conversation gets saved team orientation, old sessions remain inspectable.
- Native build/release archive/checksum, isolated install and `gnat --no-open`
  smoke run without Docker or GitHub; version and help reflect the native surface.

## Implementation plan

1. Add browser reconnection, focus navigation refresh and active-reset affordance
   with deterministic browser-script and HTTP replay coverage.
2. Extend recovery tests through the detached daemon and reopened SQLite records,
   plus reset with an existing worker and queued input. Fix confirmed gaps only.
3. Simplify native build/install/release targets, inject tag versions in releases,
   and exercise the produced archive and isolated install on macOS.
4. Update README, setup/usage, architecture, repository instructions, specification
   implementation status, plugin skill instructions and plugin metadata.
5. Record outcomes and remaining live-validation limits in the work log; stage
   implementation and documentation for one independent code review.
