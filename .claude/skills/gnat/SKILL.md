---
name: gnat
description: Operate `gnat`, which runs coding agents (Claude Code or Ox) in Docker containers that each work on their own clone of a GitHub repo. Use when the user wants to run work across a swarm of agents, create or manage a swarm or its agents, send agents tasks, watch or wait on them, review what they produced (branches, pull requests), or tear a swarm down.
---

# Operating gnat

`gnat` runs a set of coding agents, one Docker container each, on separate clones of one GitHub repository. Each agent runs Claude Code or Ox (`--harness`), and a swarm can mix them. You drive it from the developer's machine; the agents commit, push, and open pull requests. Assume `gnat` is on PATH (`make install` in the gnat checkout; rerun it after pulling gnat to update). `gnat COMMAND -h` is authoritative on flags; the gnat checkout's docs/ has the full reference.

gnat is in active development, and every run is also a test of it. When you finish using it, file a feedback report as a GitHub issue (see [Feedback](#feedback)).

## Before anything

- Docker must be running. Check `docker info` and tell the user if it isn't; don't try to start it.
- `GH_TOKEN`, plus the credential of each harness the agents use, must be in the environment or in gnat's own `~/.config/gnat/.env` (read whatever directory you run `gnat` from): `CLAUDE_CODE_OAUTH_TOKEN` (from `claude setup-token`) for Claude agents, `OPENROUTER_API_KEY` for Ox agents. `GH_TOKEN` falls back to the active `gh` account. Check that they are set without printing them (`grep -c '^GH_TOKEN=' ~/.config/gnat/.env`, `test -n "$GH_TOKEN"`); never echo, print, or commit tokens or `.env` files.
- Agents run with permissions skipped and hold these tokens, so anything that steers an agent can use them. Recommend a fine-grained token limited to the one repository (Contents and Pull requests: read and write; add Issues or Workflows only if needed), or better, a separate bot account's token. If the user's only credential is their everyday `gh` login, say so and let them decide.

## Set up

```sh
gnat create NAME --repo OWNER/NAME [--dockerfile FILE] [--file HOST_PATH[:WORKSPACE_PATH]]...
gnat agent create NAME AGENT --instructions TEXT [--harness claude|ox] [--model MODEL] [--effort LEVEL]
gnat up NAME
```

- `gnat list` and `gnat agent list NAME` (name, harness, model, effort) show what exists.
- `--harness` defaults to `claude` (Claude Code). `ox` runs Ox: `--model` takes a qualified ID such as `openrouter:deepseek/deepseek-v4.1-flash`. Efforts: for claude, `low`, `medium`, `high`, `xhigh`, `max`; for ox, `default`, `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` (the model must support it). Both default to the harness's own.
- `--dockerfile` is optional, and must start `FROM gnat-agent`. Use it only for packages the repo's build or tests need that the base image lacks. The base image has build-essential, python3, uv, git, gh, and common CLI tools (`rg`, `fd`, `jq`, `sqlite3`, `ps`, `lsof`, ...). Once it exists (after any `gnat up`), check whether it already builds and tests the repo: `docker run --rm -e GH_TOKEN gnat-agent bash -c 'gh repo clone OWNER/NAME r && cd r && TEST_COMMAND'` with `GH_TOKEN` exported. If that passes, leave `--dockerfile` out; if not, install only what failed. The build context is the Dockerfile's directory.
- **Agents (and files) can only be added or removed while the swarm is down.** To change them: `gnat down NAME`, then `gnat agent create|delete`, then `gnat up NAME`. `gnat down` keeps agents' clones (including uncommitted work), sessions, queued messages, and logs, and `gnat up` carries on from them; a turn running at `down` is reported as interrupted, so send the agent a message to resume it. `gnat agent delete` deletes that agent's clone and sessions, so make sure its work is pushed first.
- `gnat up` needs at least one agent, builds images, and returns once the control plane answers, so chaining `gnat up NAME && gnat send ...` is safe. It prints the web UI's URL when it finishes. Agents may still be cloning (`gnat status` shows `starting`); messages queue until they're ready.
- Running containers keep the swarm's code from `gnat up`. After updating gnat (`make install`), `gnat down` and `gnat up` to use it.

### Agent instructions

gnat already tells each agent its teammates, how to message them, that messages arriving mid-turn (`--now`) are genuine, the tools its container has, and never to reveal credentials. Put the role and working rules in `--instructions`. Good instructions say:

- Work only in your own clone; for each task create a new branch off the latest `origin/main`, never push to `main`.
- Commit in the repo's style, push the branch, and open a pull request with `gh`.
- Run the project's tests and linter before pushing (give the commands).
- End every turn with a short report: the PR URL, what changed, and anything blocked or needing a decision. If blocked on a decision, say so instead of guessing.
- Their area, if split by role (e.g. "you own `src/api`; don't edit other directories").

Use `--model` and `--effort` to make cheap agents for simple work (e.g. `--model haiku --effort low`, or an Ox agent on a cheap OpenRouter model) and stronger ones for hard work.

## Dividing work

Each agent has its own clone, so collisions show up as merge conflicts in PRs, not live. Prevent them:

- Give each agent a distinct task, and distinct files or directories where possible. Name the files each owns in the message.
- Don't give two agents work that touches the same files, a shared lockfile, or the same generated output without telling them to coordinate (they can message each other, but two agents can reply to each other forever).
- Sequence dependent work: wait for the first PR to merge, then tell the next agent to pull `main` and start.
- Prefer one task per message with an explicit goal and a "done" condition. Agents can't see your conversation; include the issue text, file paths, and constraints.

## Sending work

```sh
gnat send NAME AGENT... -m "TEXT"          # named agents; every agent if none named
echo "long task" | gnat send NAME AGENT    # message from stdin when -m is omitted
```

- Messages queue per agent and run one at a time in the same session, so an agent remembers earlier messages.
- `--new`: start in a fresh session (use between unrelated tasks so old context doesn't bleed in; later messages continue the new session). Can't be combined with `--now`.
- `--now`: deliver into the agent's running turn at its next tool call, for either harness (to redirect or add a constraint). If the turn ends first, it starts the next turn ahead of the queue. Don't use it for ordinary tasks.
- `--wait [--timeout SECONDS]`: send, then block until the agents finish and print their final replies. Use it for short tasks; for long ones, send without it and poll.
- Different tasks per agent need separate `send` commands, one per agent.

## Watching

```sh
gnat status NAME [AGENT...]   # state, queued, turns, time since last event, usage, how the last turn ended
gnat wait NAME [AGENT...] [--timeout SECONDS]   # block until idle, print final replies
gnat logs NAME [AGENT...] [-n LINES] [-f]       # messages and agents' output; works while down
gnat web NAME [--no-open]     # print the web UI URL (a column per agent: setup, usage, events)
```

- `gnat status` shows, for a busy agent, how long since its last event (`last event 3m12s ago`). Agents think for minutes at high effort, but a long silence is worth a look with `gnat logs NAME AGENT -n 20`; `gnat stop` it if it's stuck.
- `gnat status` and the web UI also show each agent's usage so far: tokens and cost in US dollars (Claude's at API list prices). Use them to answer questions about cost or to compare models. They're still there after `gnat down`.
- `gnat wait` exits 1 if an agent's last turn failed or was stopped, and 124 on timeout. A timeout isn't a failure: the agents keep working; check `gnat status` and wait again.
- `gnat logs -f` never exits; don't run it in the foreground. Use `-n 50` for a snapshot, or run it in the background.
- Foreground commands time out in your shell: for turns that take many minutes, run `gnat wait` with `--timeout` in a loop or in the background.
- The agents' final replies from `gnat wait` are the first thing to read, then logs for detail.

## Stopping

- `gnat stop NAME [AGENT...]` ends an agent's running turn (for Claude, killing the processes its tools started; for Ox, cancelling the prompt). Its queue, clone, and session survive and it moves to its next message. Use it for runaway or wrong-direction work; then send a corrected message (add `--new` if the session is polluted).
- If two agents are bouncing messages at each other, `gnat stop` both, then `gnat down` if needed.

## Files outside source control

For things the repo doesn't contain, such as a `.env` with an API key:

```sh
gnat create NAME ... --file .env --file ~/keys/ci.json:config/ci.json
gnat file add NAME HOST_PATH[:WORKSPACE_PATH]   # swarm must be down
gnat file delete NAME WORKSPACE_PATH            # swarm must be down
gnat file list NAME
```

- Each file lands in every agent's clone (path relative to `/workspace`, default the file's name), mode 0600, excluded from git. A path the repo tracks is refused; `gnat up` fails if a host file is missing.
- Files are read at `gnat up`, which replaces the agents' copies; to change what agents get, edit the host file, then `gnat down` and `gnat up`.
- Agents can read them, so share only narrowly scoped keys, never the user's own.

## Reviewing the result

Agents' work is in the pull requests they open, not on the host. Anything an agent didn't push stays in its clone, on a Docker volume that survives `gnat down`. `gnat cp NAME AGENT:PATH HOST_PATH` copies a file or directory out of it, up or down (a relative `PATH` is in the clone; while the swarm is down, only paths in the clone and sessions). Read each agent's reply (`gnat wait`), then review the PRs with `gh pr list` / `gh pr view` / `gh pr diff` in the repo, and have the user merge. If a PR needs changes, `gnat send` the agent what to fix (without `--new`, so it keeps its context).

A strong agent can review a cheaper agent's PR: send it the PR number and ask it to check out the branch, test what the PR claims, and post its findings with `gh pr review --comment`.

### When the user has you merge

If the user delegates merging, or the work is a chain of dependent PRs where merging is part of the loop:

- Before merging, test the combined result in a scratch clone on the host: fetch the PR branches, merge them locally in the order you'll merge them, and build and test from scratch on the host (anything built inside an agent's container is a Linux build).
- Stacked PRs (B based on A's branch): retarget the top one at `main` (`gh pr edit B --base main`), merge it with `gh pr merge B --rebase`, which brings A's commits along, then close A (`gh pr close A`) with a comment naming B.
- After each merge, tell agents whose work depends on it to pull `main` (or rebase) before continuing.

## Review-only swarms

When the agents' job is to review, investigate, or measure rather than change the repo, the deliverable is each agent's final reply, not a PR:

- Tell agents not to commit, push, or open PRs, and to end with their report in the final reply (give a length limit).
- Tell them to keep scratch files, prototypes, and measurement scripts in their clone (untracked), not `/tmp`, so they survive `gnat down` and you can copy them out with `gnat cp`.
- Run `gnat wait NAME AGENT` once per agent, so one slow or stuck agent doesn't keep you from collecting the others, and save each reply to a file as it arrives.

## Tear down

- `gnat down NAME` stops the containers and keeps everything else: clones, sessions, queued messages, logs, agents, and files.
- `gnat delete NAME` also deletes the swarm's definition, image, logs, and agents' clones and sessions. Confirm with the user before deleting, and check first that nothing worth keeping is unpushed: logs are the only record of the run, and anything not pushed is gone.
- Check `gnat list` afterwards for swarms left up.

## Feedback

After the run, whether the task succeeded, failed, or was abandoned, evaluate how well the swarm solved the problem and file the report as a new issue on the gnat repository: `gh issue create --repo kkestell/gnat --title "Feedback: NAME, YYYY-MM-DD HH:MM" --body-file REPORT.md` (swarm name, local time). The issue is public: leave out secrets, tokens, and anything from the target repo that isn't already public. Write it before `gnat delete`, which removes the logs. Base it on evidence: the agents' final replies, `gnat logs`, `gnat status`, the PRs, and what you had to do as the operator. Cover:

- The task, the swarm's setup (agents, harnesses, models, effort, Dockerfile, files), and the outcome: what was delivered, what wasn't, and whether the PRs were correct and mergeable.
- How the work was divided and whether that worked: merge conflicts, duplicated or idle agents, dependency waits, agent-to-agent messaging.
- Where time and turns went, and anything that stalled, failed, crashed, restarted, or needed `gnat stop`.
- Every point where you had to intervene, guess, retry, or work around gnat, including confusing CLI output, missing commands or flags, and gaps or errors in this skill or the docs.
- Whether a single agent, or no swarm at all, would have done the job as well.

End with concrete recommendations, each naming the change (a command, flag, control-plane or agent behavior, default instructions, or a section of this skill), the problem from this run it addresses, and the expected effect. Rank them by impact. Leave out generic advice that this run's evidence doesn't support.
