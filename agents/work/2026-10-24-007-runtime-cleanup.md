# One Gnat runtime

## Plan

`agents/plans/2026-10-24-007-runtime-cleanup.md`

## Summary

The current local runtime is the only implementation in the checkout. Its
packages live directly under `internal/`; the old command, packages, dashboard,
profiles, fixtures, and unused dependencies are removed. Documentation, plugin
metadata, build targets, errors, default state paths and development versions
use the Gnat name. The plan's goal is met.

## Decisions

- The user confirmed there is no previous installation data to preserve. No
  compatibility bridge, state relocation, or migration strategy was added.
- The schema is created directly rather than incrementally upgraded through
  milestone-era migrations. Numeric schema validation still refuses incompatible
  databases; obsolete upgrade tests were replaced by rejection coverage.
- Superseded development records were deleted rather than edited into a false
  history; the product specification was renamed and its detailed contracts kept.

## Checks run

- `make build` — passed.
- `go test ./...` — passed, including macOS detached subprocess coverage; live
  provider tests remained opt-in.
- `bun test internal/daemon/page.test.js` — six passed.
- `go vet ./...`, `staticcheck ./...`, and gofmt check — passed.
- `claude plugin validate .` — passed, with the existing advisory that the plugin
  manifest has no version field.
- `make release VERSION=v0.0.0-cleanup` — passed. The archive contains only its
  directory and executable; the executable reports the injected version and the
  archive checksum passes.
- `git diff --check` and source/documentation searches — passed. No obsolete
  runtime paths or application generation branding remain. Provider/dependency
  versions and Git porcelain format names retain their real version numbers.

## Manual verification

Built an isolated installation with `GOBIN=<temporary>/bin make install`, set
`XDG_STATE_HOME=<temporary>/state` and `XDG_CONFIG_HOME=<temporary>/config`, and
created a disposable committed Git repository. Configuration used
`orchestrator: {harness: claude, model: example, effort: high}`; no agent prompt
was submitted.

1. Ran the installed `gnat --no-open` twice from the repository and observed the
   same swarm URL.
2. Verified `state/gnat/gnat.db` and `state/gnat/runtime/daemon.json`, and checked
   the URL against the descriptor listener.
3. Verified `gnat --version` reports `gnat version dev` and the checkout stays
   clean.
4. Ran `gnat daemon stop`; readiness disappeared and the database remained.
5. Repeated registration/reuse/shutdown after schema consolidation with the final
   binary; verified schema version 1 and the complete agent columns directly in
   the disposable database. Go checks and the release were rerun successfully.

No departures from the plan or follow-up implementation work.
