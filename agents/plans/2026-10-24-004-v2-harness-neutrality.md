# V2 harness neutrality

## Goal

Implement milestone four: Claude Code, Codex, and OpenCode can each run the
orchestrator or workers, including mixed teams, with exact persistent sessions,
launch-scoped MCP, normalized output, and cancellation under the existing core.

## Related code

- `internal/v2/harness/claude.go`: existing process-per-turn adapter.
- `internal/harness/{codex,acp}.go`: retained protocol examples, without v1 setup.
- `internal/v2/core/{core,coordination}.go`: neutral scheduler and worker creation.
- `internal/v2/daemon/{daemon,live_test}.go`: production wiring and live workflow.
- `internal/v2/store/config.go`: saved harness/model/effort settings.
- `agents/gnat-v2-spec.md` section 10 and milestone four.

## Decisions

- Dispatch saved harness names in the adapter package; the core retains its small
  Probe/Run interface and no provider implementations. Validate supported names
  and required settings through store settings rather than a Claude-only gate.
- Use one owned subprocess per turn for all harnesses. Codex app-server resumes
  the saved thread and OpenCode ACP resumes the saved session; SQLite remains
  the source of neutral session identity, while providers retain their own files.
  This avoids idle-process ownership and a second session store.
- Share only JSON-RPC transport/process supervision between Codex and OpenCode.
  Keep their handshakes, configuration, event translation, and permissions in
  separate adapters. Cancel the identified provider turn/session with a bounded
  graceful wait and kill the process group on close or failure.
- Preserve HOME, credentials, local configuration, and native delegation. Inject
  runtime instructions and authenticated HTTP MCP per launch/session. Codex uses
  developer instructions and per-thread config; OpenCode uses assistant-context
  prompt blocks and session MCP/config options. No global/project files change.
- Check installed protocol capabilities and discovered model/effort choices rather
  than guessing compatibility from a version number. Report unknown metrics as
  absent; report usage once per Gnat turn, never add cumulative session totals.
- Keep the existing provider-neutral browser transcript. Expose adapter capability
  metadata in the harness package and exercise production dispatch in fixtures
  and a live matrix; document any unavailable live provider explicitly.

## Test plan

- JSON-RPC subprocess fixtures: authentication, unsupported protocols/settings,
  exact resume/reset, failed resume without replacement, per-session MCP tokens,
  working directory/instructions, text/tools/nested attribution, usage, early
  notifications, invalid output, persistence errors, and EOF.
- Real subprocess cancellation: graceful request targets, stalled cancellation,
  and process-group cleanup, including child processes.
- Mixed-harness orchestration through the existing MCP/core/store flow with real
  temporary Git worktrees; preserve role enforcement and per-agent session IDs.
- Opt-in live disposable-repository matrix: all available providers as
  orchestrator and worker, follow-up and restart session continuity.
- Focused v2 Go tests and race tests, native build, vet and formatting checks.

## Implementation plan

1. Add supported-settings validation and adapter dispatch/capability metadata;
   remove the core's Claude-only worker gate and wire production dispatch.
2. Add shared bounded JSON-RPC process transport and native Codex adapter.
3. Add OpenCode ACP adapter with discovered model/effort and HTTP MCP support.
4. Add protocol/lifecycle fixtures and mixed-team/live integration coverage.
5. Update architecture, spec status, README, setup/usage, repository instructions,
   and plugin source-build notes to describe the landed integrations.
