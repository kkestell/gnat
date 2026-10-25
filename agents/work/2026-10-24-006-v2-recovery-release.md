# V2 recovery and release work log

## Plan

`agents/plans/2026-10-24-006-v2-recovery-release.md`

## Summary

Milestone six is implemented: browser reconnects recover from snapshots, active
reset offers interruption first, workspace navigation refreshes on focus, and
native build/install/release paths no longer build container helpers or install
profiles. Recovery coverage exercises real detached macOS processes and reopened
SQLite state, independent swarms, HTTP replay, worker-preserving reset and live
Claude session continuation/reset. Operating docs and plugin instructions now
present the v2 command surface.

## Departures from the plan

- No core/store production changes were needed: existing persistence and reset
  behavior passed the expanded recovery scenarios.
- The first crash fixture mistook an unreaped direct-child daemon zombie for
  surviving execution. The normal CLI exits after launch, but the test remains
  alive; its process check now reaps its own detached child using nonblocking
  wait before asserting disappearance. This adds no runtime cleanup policy.
- `make lint` initially reached the retained UI typecheck without installed
  Bun/React type packages. `cd ui && bun install --frozen-lockfile && bun run
  typecheck` repaired the local dependency state and passed without source or
  lockfile changes.

## Decisions

- Browser page-cache restoration reconnects like a fresh view; page hide closes
  streams and retry timers. Superseded connection attempts are invalidated so a
  delayed pre-hide snapshot cannot open or close a restored page's stream.
- Keep legacy source packages, profile samples and Docker fixtures as archived
  development inputs; native targets neither build nor distribute them. Update
  the old ignore/sample comments to avoid advertising removed build paths.
- Run the release executable's version check on arm64 runners; archive and
  checksum checks apply on every runner so Intel macOS can still cross-build.

## Checks run

- `go test -race ./internal/v2/...` and `go vet ./internal/v2/...` — passed.
- `make test` — passed all Go packages, 4 v2 browser cases (25 assertions), and
  47 retained UI cases. After the review correction, the focused browser suite
  passed 6 cases with 31 assertions, including both delayed old-fetch outcomes.
- `make lint` — Go vet, staticcheck and gofmt passed; retained UI typecheck passed
  after the frozen dependency installation described above.
- `go build ./...`, changed-Go gofmt and `git diff --check` — passed.
- `make build` and `make release VERSION=v2-recovery-check` — passed; the release
  was rebuilt after the browser correction and daemon tests passed again. The archive
  contained exactly its directory and native executable, checksum passed, and
  executable printed `gnat version v2-recovery-check`.
- `claude plugin validate .` — passed with the pre-existing missing plugin
  version warning. No release or plugin version was invented for this change.
- Isolated `GOBIN` source install and release-binary smoke run — passed: both
  opened the same saved workspace, left Git clean, installed no profiles and
  stopped their daemon successfully. Dependencies of `cmd/gnat` include v2
  packages without legacy container/control/credential packages.

## Manual verification

```sh
GNAT_LIVE_TEST=1 go test ./internal/v2/daemon -run '^TestLiveConversationRecovery$' -v -timeout 5m
```

Passed in 12 seconds with authenticated local Claude: saved the first provider
session, resumed that exact session after service restart, reset with queued
input, retained old history, and delivered the pending message into a distinct
new provider session. The detached deterministic test separately covers actual
daemon shutdown/crash and SQLite reopen.

For the native installation smoke check, use a temporary directory with separate
`GOBIN`, `XDG_STATE_HOME`, and `XDG_CONFIG_HOME`, and a committed disposable Git
repository. `GOBIN=<temp>/bin make install` installs the source binary. Put a
fixture `claude` on PATH responding to `--version` and `auth status`, and write
`orchestrator: {harness: claude, model: fixture, effort: high}` to the temporary
config. Run the installed binary's `--no-open` in that repository, GET the
returned URL with `/swarms/` replaced by `/api/swarms/`, save its swarm/agent/
conversation IDs, then `daemon stop`. Repeat with
`dist/gnat-darwin-arm64/gnat`; the IDs match, `git status --porcelain` is empty,
and `<temp>/config/gnat/profiles` is absent. Both daemon stops complete before
temporary-directory removal. No user install or real swarm was replaced.

## Follow-up work

Correction after consulting the milestone-four session: its later installation
repair resolved the Codex helper timeout, and live MCP checks passed for both
Codex roles. The original milestone-six plan, work log and completion response
incorrectly carried forward stale notes from before that repair. The full live
mixed-provider matrix was not rerun for milestone six. No release was published.
Workspace relocation, unregistering, Git UI and automatic surviving-process
cleanup after daemon crashes remain outside the agreed first-cut scope.

## Review

Independent review confirmed one medium-severity browser resource-lifetime race:
page-cache restoration could overlap an earlier snapshot fetch, opening a second
event stream after restoration. The parent added connection-attempt generations
and ignores obsolete callbacks/results, with delayed-success and delayed-failure
regressions. The reviewer verified the staged correction and recorded F1 as fixed in
`agents/reviews/2026-10-24-006-v2-recovery-release.md`.
