# gnat

Run a swarm of coding agents, each in its own Docker container on its own clone of a GitHub repository. Agents run [Claude Code](https://code.claude.com) or [Ox](https://github.com/kkestell/ox), commit, push, and open pull requests.

## Install

Requires Go 1.26, Docker, and the [GitHub CLI](https://cli.github.com/).

```sh
make install
```

Set `CLAUDE_CODE_OAUTH_TOKEN` (from `claude setup-token`) for Claude Code agents, or `OPENROUTER_API_KEY` for Ox agents, in the environment or `~/.config/gnat/.env`. Agents use `GH_TOKEN`, or else `gh auth token`. Agents run with permissions skipped, so give them a token scoped to the one repository; see [docs/setup.md](docs/setup.md).

## Usage

```sh
gnat create demo --repo OWNER/NAME
gnat agent create demo alpha --instructions "You review code."
gnat up demo
gnat send demo alpha --wait -m "Summarize the README in one line"
gnat logs demo -f
gnat down demo
```

`gnat COMMAND -h` lists each command's flags.

For reusable agent settings, save `instructions`, `harness`, `model`, and
`effort` in `~/.config/gnat/profiles/coder.yaml`, then run
`gnat agent create demo alpha --profile coder`. Explicit flags override profile
settings, for example `--profile coder --effort max`. `gnat profiles list`
lists available names; see [agent profiles](docs/usage.md#agent-profiles) for
the format.

## Docs

- [docs/setup.md](docs/setup.md): credentials, images and Dockerfiles, files outside source control, Ox agents, and the Claude Code skill.
- [docs/usage.md](docs/usage.md): commands, how messages are delivered, state and logs, and worked examples.

## Development

`make build` writes `./gnat`, `make test` runs the tests, and `make lint` runs `go vet`, `staticcheck`, and a `gofmt` check.
