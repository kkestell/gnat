# V2 local integration

## Goal

Complete milestone five by exercising workers committing in managed worktrees
and an orchestrator integrating and verifying those commits in the original
checkout through ordinary Git, without GitHub or a delivery operation.

## Related code

- `internal/v2/core/coordination_test.go`: real repositories, worker lifecycle,
  message delivery, and Git removal safeguards.
- `internal/v2/daemon/live_test.go`: production adapters and authenticated MCP
  through the browser API in disposable local repositories.
- `internal/v2/workspace/worktrees.go`: shared branches and safe cleanup.
- `agents/gnat-v2-spec.md`: milestone five and acceptance criteria 6–8.

## Decisions

- Keep integration in ordinary agent Git operations. Add regression coverage and
  live validation rather than a runtime integration service or prescribed team
  workflow; existing worktrees already share repository objects and refs.
- Use a tiny dependency-free Go repository with two independently testable worker
  changes. Verify both changes together in the original checkout and retain
  unrelated staged, unstaged, and untracked local work.
- Cover divergent worker branches with ordinary merges and safe removal, plus a
  conflicting merge that can be aborted without losing either side. Agents
  remain responsible for choosing integration and conflict-resolution methods.
- Add a separate opt-in live integration test using the existing provider
  settings and an optional worker harness selection. Run an available local
  harness pair; the previous milestone's Codex helper limitation does not require
  a runtime workaround or another full harness matrix.

## Test plan

- Deterministic core tests with controlled turns, real commits, result messages,
  original-checkout merges, functional tests, local-change preservation, and
  removal after integration. Confirm conflicting integration leaves commits and
  worktrees recoverable through ordinary Git.
- Live production adapters: orchestrator creates two Gnat workers, receives
  their commit messages, merges, verifies the combined result, and removes both;
  assert repository contents, ancestry, local work, and retained history.
- Focused v2 tests, v2 race tests, native build, vet, and changed-Go formatting.

## Implementation plan

1. Add deterministic local integration and conflict coverage to the core tests.
2. Share live-test setup as needed and add the separate live integration scenario.
3. Run the deterministic checks and live disposable-repository workflow; fix
   confirmed runtime defects only if the workflow exposes them.
4. Update current milestone status and explain local integration and its
   validation in README, architecture, usage, repository and plugin instructions.
