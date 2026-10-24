# Codex support

## Plan

`agents/plans/2026-10-23-001-codex-support.md`

## Summary

Implemented the Codex harness, shared access-only ChatGPT credential login,
manual renewal, persistent conversations, cancellation, event translation, and
the coder profile. Verified two real Codex agents on `kkestell/gnat-sandbox`.
The plan's goal is met; changes are staged without a commit.

## Departures from the plan

- Codex 0.163.0 requires a separate `codex-code-mode-host` release binary for
  tool execution. The image installs both binaries for its target architecture.
  The first tool-use smoke test exposed the missing helper; it passed after
  installing it.
- The CLI's `--dangerously-bypass-hook-trust` flag does not authorize hooks for
  app-server in this release. The runner instead discovers gnat's exact user
  hook through `hooks/list` and writes its native definition hash through
  `config/value/write`. It does not trust project or unrelated hooks. Native
  urgent and pause delivery passed after this change.

## Checks run

- `go test -race ./internal/harness ./internal/cli` — passed.
- `go test ./...` — passed.
- `go vet ./...` and `staticcheck ./...` — passed.
- `test -z "$(gofmt -l .)"` and `git diff --check` — passed.
- `make build` — built the host and static Linux arm64/amd64 container binaries.
- `docker build -q -t gnat-agent:latest -f internal/cli/Dockerfile .` — passed
  on Linux arm64, including the pinned native binaries.
- Dependency checks with `go list -deps` — the two binary boundaries hold.
- `claude plugin validate .` — passed with the existing warning that the
  marketplace plugin has no version.

## Manual verification

The live test used an access-only snapshot of the existing local ChatGPT login
and an isolated state directory with a temporary keyring reader. It did not
change the user's keyring or normal Codex login. Browser login itself is covered
by the fake-CLI tests, including cancellation, invalid credentials, expiry,
temporary-file cleanup, and preservation of an existing credential on failure.

To reproduce with a normally saved gnat credential:

```sh
make build
./gnat auth login codex
export GH_TOKEN="$(gh auth token --user kkestell)"
./gnat create codex-smoke --repo kkestell/gnat-sandbox
./gnat agent create codex-smoke alpha --harness codex --effort low
./gnat agent create codex-smoke beta --harness codex --effort low
./gnat up codex-smoke
./gnat send codex-smoke alpha --wait -m 'Remember violet-otter; reply READY.'
./gnat send codex-smoke beta --wait -m 'Remember amber-finch; reply READY.'
./gnat send codex-smoke alpha --wait -m 'Reply only with your remembered marker.'
./gnat down codex-smoke
./gnat up codex-smoke
./gnat send codex-smoke alpha --wait -m 'Reply only with your remembered marker.'
```

Observed distinct conversations sharing one credential, marker recall before
and after restart, and token/context usage without dollar cost.

For each control check below, wait for the sleep command to appear in logs
before sending the next control command:

```sh
./gnat send codex-smoke alpha -m 'Run sleep 12, then reply with any new operator marker.'
./gnat send codex-smoke alpha --now -m 'Reply exactly HOOK-SEEN.'
./gnat wait codex-smoke alpha --timeout 45

./gnat send codex-smoke alpha -m 'Run sleep 12, then follow any new operator message.'
./gnat pause codex-smoke alpha -m 'Reply exactly PAUSE-SEEN after this command.'
./gnat send codex-smoke alpha -m 'Reply exactly RESUMED.'
./gnat status codex-smoke alpha
./gnat resume codex-smoke alpha
./gnat wait codex-smoke alpha --timeout 45

./gnat send codex-smoke alpha -m 'Run sleep 30.'
./gnat stop codex-smoke alpha
./gnat wait codex-smoke alpha --timeout 15
./gnat send codex-smoke alpha --wait -m 'Reply exactly AFTER-STOP.'
./gnat send codex-smoke alpha --new --wait -m 'Reply exactly NEW-THREAD.'
./gnat delete codex-smoke
```

Observed urgent delivery recorded with `mid_turn: true`, pause instructions
delivered to the active turn, and the queued turn held until resume. Stop made
`wait` exit 1, the next turn continued successfully, and `--new` changed the
saved thread ID. The access token was absent from recorded logs and events.
The test swarm, containers, volumes, and temporary credential files were removed.
