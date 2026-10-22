# gnat

Run a swarm of coding agents, each in its own Docker container on its own clone of a GitHub repository. Agents run [Claude Code](https://code.claude.com) or [Ox](https://github.com/kkestell/ox), commit, push, and open pull requests.

## Install

Requires Go 1.26, Docker, and the [GitHub CLI](https://cli.github.com/).

```sh
make install
```

Save a credential for each harness your agents use: `gnat auth login claude` (paste the token from `claude setup-token`) or `gnat auth login ox` (an OpenRouter API key). Only the control container uses `GH_TOKEN` (or else `gh auth token`): agents have no GitHub credential, and clone, fetch, push, and use pull requests through it, limited to the swarm's repository and their own branches. See [docs/setup.md](docs/setup.md).

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
`effort` in `~/.config/gnat/profiles/agents/coder.yaml`, then run
`gnat agent create demo alpha --profile coder`. Explicit flags override profile
settings, for example `--profile coder --effort max`. `--profile` also takes a
profile file's path. `gnat profiles list` lists available names; see
[agent profiles](docs/usage.md#agent-profiles) for the format. A [swarm profile](docs/usage.md#swarm-profiles) in
`~/.config/gnat/profiles/swarms/small.yaml` lists a swarm's agents, each from an
agent profile with its own settings, so
`gnat create demo --repo OWNER/NAME --profile small` creates them all.

## Docs

- [docs/setup.md](docs/setup.md): credentials, images and Dockerfiles, files outside source control, Ox agents, and the Claude Code skill.
- [docs/usage.md](docs/usage.md): commands, how messages are delivered, state and logs, and worked examples.

## Development

`make build` writes `./gnat`, `make test` runs the tests, and `make lint` runs `go vet`, `staticcheck`, and a `gofmt` check.
