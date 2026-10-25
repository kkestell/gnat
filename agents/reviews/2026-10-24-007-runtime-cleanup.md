# One Gnat runtime review

## Scope and coverage

Reviewed the staged changes for
`agents/plans/2026-10-24-007-runtime-cleanup.md`, including deleted implementation
dependencies, all 45 relocated runtime files, command entry, default state path,
database initialization, tests, build/release targets, plugin installation and
operating instructions, specification, and architecture. The consolidated
database schema retains every table, column, default, index, and foreign key
needed by the current runtime, with no upgrade path.

Gaps:

- Real provider sessions were not exercised during this review. The work log
  records the implementation's automated checks and isolated installation smoke
  check; those broad checks were not duplicated here.

## Summary

| ID | Severity | Category | Fix | Title |
| -- | -------- | -------- | --- | ----- |
| F1 | Low | Documentation | Fixed | Architecture points to nonexistent files |
| F2 | Low | Documentation | Fixed | ACP reference loses its precise documentation link |

## F1. Architecture points to nonexistent files

Low · Documentation · Fixed · `agents/architecture.md:3`

The architecture introduction directed contributors to three nonexistent file
paths for package descriptions and operating documentation. Readers following
those references could not find the current repository instructions.

### Evidence

Filesystem inspection confirmed the referenced locations were absent and that
`AGENTS.md`, `docs/usage.md`, and `docs/setup.md` contain the intended material.

### Fix

Updated the introduction to refer to those existing files. This was a trivial
documentation correction and required no design decision.

## F2. ACP reference loses its precise documentation link

Low · Documentation · Fixed · `agents/gnat-spec.md`

The provider reference replaced OpenCode's ACP documentation URL with its home
page. That removed the direct source for transport, sessions, and MCP behavior
while leaving the surrounding explanation unchanged. The provider's URL version
is unrelated to Gnat application branding.

### Evidence

Opening the [official OpenCode ACP reference](https://opencode.ai/v2/docs/cli/acp/)
confirmed the original URL remains available and documents the named interfaces.

### Fix

Restored the precise provider reference. This follows the plan's explicit choice
to retain external interface versions and required no new design decision.

## Checks run

- Compared every relocated runtime file against its previous source with import
  paths normalized. All 45 files remain accounted for; changes outside the store
  simplification are the planned path, version, message, and test updates.
- Checked the previous runtime's internal imports; all referenced packages were
  contained within the relocated runtime, so deleted packages were not required.
- Executed the previous complete schema and the consolidated schema in separate
  SQLite memory databases. All eight tables have identical columns, defaults,
  indexes, and foreign keys; AUTOINCREMENT remains and the current schema marker
  is one. Initialization has no upgrade path.
- Traced initialization and its incompatible-schema tests, state/config path
  handling, embedded browser/fixture paths, release version injection, workflow
  archive expectations, and plugin installation instructions.
- Verified corrected local documentation paths and the external ACP reference.
- `git diff --check` — passed.

## Verdict

The cleanup meets the plan and the user's no-compatibility requirement. F1 and
F2 are fixed; no correctness or runtime dependency findings remain.
