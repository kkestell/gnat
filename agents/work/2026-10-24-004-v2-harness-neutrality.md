# V2 harness neutrality work log

## Plan

`agents/plans/2026-10-24-004-v2-harness-neutrality.md`

## Summary

Added per-agent adapter dispatch, native Codex app-server and OpenCode ACP
integrations, normalized output, exact session continuation, launch-scoped MCP,
and bounded cancellation/process ownership. The deterministic mixed-team matrix
covers every harness as orchestrator, both other harnesses as workers, and restart.
Live MCP checks passed for Claude, Codex, and OpenCode in both roles. Codex's
initial Homebrew Code Mode host timeout was resolved after the milestone commit,
in the same session, by replacing the installation with OpenAI's standalone
package at the same version. See the follow-up validation below.

## Departures from the plan

- ACP has no read-only authentication-status operation. Probe checks its protocol;
  selected-provider authentication is checked by prompt execution and errors carry
  the normal login command. Models/efforts are validated through session options.
- The browser already consumed normalized text/tools/usage without provider
  parsing; no browser code or schema migration was needed.
- Temporary Codex environment filtering did not resolve the installed helper
  failure and was removed. Keep normal environment inheritance and the supported
  app-server protocol; do not add helper management to Gnat.
- A live-test predicate counted worker-directed instructions as replies. It now
  requires authenticated worker senders addressing the orchestrator, and verifies
  requested harness settings and stable session references after restart.

## Decisions

- Emit token usage once at turn completion. Infer Codex's prior cumulative baseline
  from the first total/last update, then subtract it from final totals; ACP uses
  end-prompt usage and omits cumulative session cost without a saved baseline.
- Keep RPC stderr on the null device directly: a child retaining a copied stderr
  pipe must not block parent reaping. Fixture tests cover this orphan scenario.

## Checks run

- `go test ./internal/v2/...` — passed, including fixture subprocesses and mixed
  teams in real temporary Git worktrees.
- `go test -race ./internal/v2/...` — passed.
- `go build -o /tmp/gnat-v2-milestone-four ./cmd/gnat` — passed.
- `go vet ./internal/v2/...` and focused gofmt check — passed.
- `GNAT_LIVE_TEST=1 go test ./internal/v2/harness -run
  'TestLiveAdapterMCP/(claude|opencode)' -v -timeout 8m` — passed all four
  orchestrator/worker checks using Claude Code 2.1.310 and OpenCode 1.19.0.
- Live mixed matrix and isolated Codex MCP check — Codex 0.164.0 completed
  without calling the configured tools, reporting an internal Code Mode host
  negotiation timeout. MCP initialization and tool discovery requests succeed
  at the HTTP layer. This initial failure preceded the successful installation
  repair and role checks recorded below; no helper workaround or environment
  filtering remains in Gnat.

## Protocol references

Checked on 2026-10-24 against installed codex-cli 0.164.0, Claude Code 2.1.310,
and OpenCode 1.19.0 (which exposes the documented OpenCode 2 ACP capabilities).

- [Codex app-server](https://learn.chatgpt.com/docs/app-server): handshake,
  account/model discovery, exact threads, turn lifecycle, per-thread overrides.
  Verified message shapes using the installed CLI's generated JSON schemas.
- [OpenCode ACP](https://opencode.ai/v2/docs/cli/acp/): ACP 1, HTTP MCP,
  session/resume, model/effort config options, permissions, usage and child updates.

## Manual verification

```sh
GNAT_LIVE_TEST=1 go test ./internal/v2/daemon -run TestLiveCoordination -v -timeout 20m
GNAT_LIVE_TEST=1 go test ./internal/v2/harness -run TestLiveAdapterMCP -v -timeout 3m
```

The live tests use disposable repositories and normal local provider credentials.

## Review

`agents/reviews/2026-10-24-004-v2-harness-neutrality.md` fixed one medium finding:
Codex and OpenCode now preserve available usage even when a turn ends with an
error or token limit, matching Claude. No new architecture decisions were needed.

## Follow-up validation after the milestone commit

The later turns in `Implement Fourth V2 Milestone` resolved the installation
failure. Homebrew's Codex 0.164.0 helper hung even on `--help`; OpenAI's standalone
installation at `~/.local/bin/codex`, also 0.164.0, started its helper normally.
The session then ran:

```sh
GNAT_LIVE_TEST=1 go test ./internal/v2/harness -run 'TestLiveAdapterMCP/codex' -v -timeout 5m
```

Both live subtests passed: orchestrator in 8.27 seconds and worker in 6.88
seconds (15.15 seconds total). This validates actual configured MCP tool calls
for both Codex roles. The complete mixed-provider coordination matrix was not
rerun by that command. These results were verified from the session transcript;
no provider test was rerun for this documentation correction.
