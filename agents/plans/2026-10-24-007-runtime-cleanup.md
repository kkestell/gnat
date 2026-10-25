# Milestone seven: one Gnat runtime

## Goal

Remove the superseded implementation and make the local daemon, orchestrator,
worker worktrees, and browser chat the sole Gnat application. Source paths,
commands, documentation, and default state paths use the Gnat name without a
runtime generation qualifier.

## Related code

- `cmd/gnat/main.go` — sole public executable and private daemon entry.
- Current runtime packages — core, daemon, entry, harness, MCP, store, workspace.
- `Makefile`, `go.mod`, `go.sum` — build, release, checks, dependency closure.
- `AGENTS.md`, `README.md`, `docs/`, `plugin/`, `.claude-plugin/` — operating and
  development instructions.
- `agents/architecture.md` and the product specification — product contracts.

## Decisions

- Move the current runtime packages directly under `internal/`, replacing the
  superseded harness and workspace packages, because there is now one runtime.
- Delete the unused container command, old packages, React dashboard and bundled
  assets, profiles, Dockerfiles, helper fixtures, and unused dependencies; none
  are imported by the current runtime.
- Use `$XDG_STATE_HOME/gnat` (default `~/.local/state/gnat`) and `dev` as the
  development version. No old installation data exists to preserve, and no
  compatibility bridge or state relocation is needed. Create the complete SQLite schema directly and retain a numeric schema version
  check, without upgrade paths for obsolete milestone schemas.
- Remove superseded planning/work/review records from the checkout rather than
  rewriting historical claims; Git history retains those records. Keep a renamed,
  current product specification and the current architecture.
- Keep provider protocol versions, dependency versions, and ordinary release tags;
  they describe real external interfaces rather than application generations.
- Limit milestone seven to removal and relocation, preserving current runtime
  behavior except for the default state location and development version.

## Test plan

- Build and test every surviving Go package; run vet, staticcheck, and gofmt check.
- Run browser tests at their relocated path and validate the Claude plugin.
- Build a release with a sample tag; verify the executable version and archive.
- Smoke-test registration, reuse, and daemon shutdown in a disposable repository
  with isolated state/config directories, and verify the unversioned state path.
- Search tracked files and filenames for obsolete paths, generation branding,
  container runtime artifacts, and references to deleted sources.

## Implementation plan

1. Remove superseded source, dashboard, profiles, fixtures, and historical records;
   move the runtime packages into `internal/` and update all imports.
2. Remove generation wording from code, errors and tests; change the default state
   path and development version and collapse incremental schemas into a single schema with version validation.
3. Simplify build/test/lint/release targets, ignore rules, and dependency manifests.
4. Refresh developer/user/plugin docs and replace the specification with a current
   product contract under `agents/gnat-spec.md`; update the architecture boundary.
5. Run the checks above, record the work, and stage for independent code review.
