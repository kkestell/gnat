# Codex support

## Goal

Add `codex` as a harness alongside Claude Code and Ox. Operators sign in once
with their ChatGPT subscription through `gnat auth login codex`, then use that
credential for any number of Codex agents. Agents support the existing message,
session, stop, pause, log, and usage behavior.

Renew credentials manually through `gnat auth login codex`. Running swarms pick
up the replacement on their next `down` and `up`, preserving their sessions.

## Related code

- `internal/harness/harness.go` — registration, container configuration,
  credentials, effort levels, and the `Runner` interface.
- `internal/harness/claude.go` — subprocess handling and event translation.
- `internal/harness/acp.go` — persistent server processes, saved session IDs,
  cancellation, and streamed turn handling.
- `internal/cli/auth.go` — keyring login, logout, and credential loading.
- `internal/cli/swarm.go` — harness selection, credential delivery, configuration
  archives, session volumes, and image checks already use the harness interface.
- `internal/hook/hook.go` — urgent messages and pause instructions through
  `PostToolUse`; its JSON output also fits Codex's documented hook contract.
- `internal/events/events.go` — the event format consumed by logs, status, and
  the UI. Usage events contain increments, and unavailable metrics are null.
- Official references: [app-server](https://learn.chatgpt.com/docs/app-server),
  [hooks](https://learn.chatgpt.com/docs/hooks), and
  [authentication](https://learn.chatgpt.com/docs/auth).

## Decisions

### Shared subscription credential, with manual renewal

Keep the existing operator model: one keyring item named for the harness, loaded
at `gnat up`, and delivered only to agents using that harness. No control-plane
credential service or new wire operation is needed.

For Codex, `gnat auth login codex` invokes the host's Codex CLI browser login in
a private temporary configuration directory with file-backed authentication.
Require ChatGPT authentication. Read the resulting credential locally, extract
only its access token, account ID, and expiry, and save that snapshot in gnat's
keyring. Always remove the temporary directory. Leave the user's normal Codex
configuration and login alone. Missing Codex, cancelled login, malformed output,
and non-ChatGPT credentials produce clear errors without changing the saved
credential.

Define `CodexCredential` in `internal/harness/codex.go` with `access_token`,
`account_id`, and `expires_at`. Pass its compact JSON through `GNAT_CODEX_AUTH`,
using the existing credential delivery path. Never retain or distribute the
refresh token. Check the access token's own expiry rather than assuming a fixed
lifetime. The locally inspected token lasts ten days; that is an observation,
not a contract to encode.

Codex agents authenticate with the access-token snapshot. Expiry or a request
for refreshed credentials ends the turn with instructions to renew through
`gnat auth login codex` and restart the swarm. Authentication failures must not
erase sessions, silently change accounts, or fall back to API billing.

### Native Codex app-server

Run `codex app-server` over standard input and output. Use its documented
external ChatGPT token login, thread lifecycle, and interrupt methods. This
external-token interface is experimental; isolate it in the Codex runner and
pin the container's Codex release to the version verified by the integration
fixtures and smoke test. The installed 0.163.0 schema additionally labels this
login mode unstable and internal-only: successful verification of this interface
is a delivery requirement, and any rejection must be reported before treating
the harness as usable.

Keep one server process per agent daemon, and preserve the current thread ID
and usage baseline under `/home/agent/.codex/gnat-session.json`. Mount
`/home/agent/.codex` as the harness's state directory. Resume that exact thread
after a server or daemon restart; `--new` creates a new thread. A failed resume
is an error rather than permission to discard the conversation.

The container supplies the sandbox. Set Codex to unrestricted filesystem access
and no interactive approvals, matching the existing harnesses. Model and effort
remain optional; omitted settings use Codex's defaults. Pass explicit values
through and report unsupported combinations clearly.

Write user instructions to `.codex/AGENTS.md`, settings to `config.toml`, and
the existing synchronous hook configuration to `hooks.json`. Enable hooks and
bypass hook trust for this unattended invocation so `--now` and pause messages
are actually delivered. Preserve the existing hook timeout. No hook output
adapter is needed.

### Existing events and consumers

Translate completed text, reasoning, tool starts/results, failures, and final
replies into existing gnat events. Buffer deltas into complete blocks and avoid
emitting the completed item again after its streamed content. Select the final
answer from the current turn, rather than treating commentary as its reply.

Translate cumulative token usage into increments, subtracting cached tokens
from input counts where Codex includes them. Preserve a baseline across resumed
turns and restarts; ignore historical replay and reset the baseline for a new
thread. Report the latest context usage and size when supplied. Leave cost and
other unreported values null. Keep this change to Codex token/context reporting;
the existing Claude account-limit display does not become a Codex account-limit
feature.

## Implementation plan

1. Add `internal/cli/codex_auth.go` and `internal/cli/codex_auth_test.go` for the
   isolated native login and access-only credential extraction. Add the Codex
   login branch in `internal/cli/auth.go`; retain ordinary keyring logout.
   Introduce `CodexCredential` and validation in `internal/harness/codex.go`.
2. Implement Codex metadata, configuration, runner, and event translation in
   `internal/harness/codex.go`, with `internal/harness/codex_test.go` covering
   the observable process and protocol behavior. Register Codex in
   `internal/harness/harness.go`. Reuse existing process helpers where they fit;
   keep the Codex protocol separate from ACP.
3. Make the runner handle server requests as well as responses and notifications.
   Authentication renewal requests receive an actionable failure. Unexpected
   requests for interactive input must receive a response and fail clearly,
   rather than leaving the turn blocked. Bound startup and protocol waits,
   correlate thread/turn IDs, and make stop work during startup and active turns.
   Interrupt the current turn, with process-group termination if the server
   cannot finish it, without allowing a delayed stop to affect the next turn.
4. Extend `internal/harness/harness_test.go` with the fake Codex server entrypoint.
   Remove its assumption that every harness has `settings.json`; retain shared
   interface checks and test each harness's actual hook configuration.
5. Install the verified Codex Linux release for both supported architectures in
   `internal/cli/Dockerfile`. Extend `internal/cli/swarm_test.go` to cover its
   installation. Extend `internal/cli/commands_test.go` and
   `internal/cli/up_test.go` for Codex selection, configuration archives, model
   and effort settings, persistent volumes, and credential isolation in a mixed
   swarm. The generic swarm setup should otherwise need no production changes.
6. Add `profiles/agents/codex-coder.yaml` using the existing coder instructions,
   `harness: codex`, and Codex's model and effort defaults.

## Test plan

- Login saves only the access-token snapshot; the refresh token never reaches
  the keyring value or agent environment. Failed and cancelled login preserve
  the previous credential, and temporary authentication files are removed.
- Fake server tests cover initial authentication, two turns in one thread,
  `--new`, restart/resume without replayed output or usage, and failed resume.
- Representative command, file-change, and MCP tool events preserve matching
  IDs and failures. Commentary, reasoning, and final replies remain distinct.
  Token totals, cache subtraction, context updates, and unavailable cost have
  their expected meanings across turns.
- Stop before startup, during a turn, and immediately before the next turn
  cannot hang or cancel the wrong turn. Test EOF, malformed protocol output,
  startup failure, authentication expiry, and renewal requests with safe errors.
- A mixed Claude/Ox/Codex swarm gives each agent only its own harness credential;
  secrets stay out of arguments, logs, events, and swarm definitions. Codex
  configuration installs the same message-delivery hook with the correct timeout.
- On `kkestell/gnat-sandbox`, verify the pinned native Codex interface with a
  ChatGPT subscription: send work, inspect logs and `wait`, continue a session,
  deliver a `--now` message during tool use, stop a turn, and resume after
  `down`/`up`. Exercise pause delivery as well. Run two Codex agents with the
  same access snapshot to confirm independent sessions and no refresh attempts.
  Use a fake expired snapshot for renewal handling rather than waiting ten days.

## Documentation updates

- `README.md`, `docs/setup.md`, and `docs/usage.md`: add Codex, host CLI login
  prerequisites, shared ChatGPT subscription authentication, manual renewal and
  swarm restart, model/effort behavior, and a short Codex agent example. Explain
  that subscription usage shares the account's allowance and cost may be absent.
- `agents/architecture.md`: document Codex's saved thread and access-only
  authentication, manual renewal, and its use of the existing delivery hook.
- `AGENTS.md`: include Codex in the harness package description.
- `plugin/skills/install/SKILL.md` and `plugin/skills/gnat/SKILL.md`: include Codex
  in setup and operation instructions, using `gnat auth login codex` without
  asking for credentials in chat.
