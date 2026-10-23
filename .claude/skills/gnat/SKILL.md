---
name: gnat
description: Operate `gnat`, which runs coding agents (Claude Code or Ox) in Docker containers that each work on their own clone of a GitHub repo. Use when the user wants to run work across a swarm of agents, create or manage a swarm or its agents, send agents tasks, watch or wait on them, review what they produced (branches, pull requests), or tear a swarm down.
---

# Operating gnat

`gnat` runs a set of coding agents, one Docker container each, on separate clones of one GitHub repository. Each agent runs Claude Code or Ox (`--harness`), and a swarm can mix them. You drive it from the developer's machine; the agents commit, push, and open pull requests. Assume `gnat` is on PATH (`make install` in the gnat checkout; rerun it after pulling gnat to update). `gnat COMMAND -h` is authoritative on flags; the gnat checkout's docs/ has the full reference.

gnat is in active development, and every run is also a test of it. When you finish using it on a repository owned by `kkestell`, file a feedback report as a GitHub issue (see [Feedback](#feedback)).

## Before anything

- Docker must be running. Check `docker info` and tell the user if it isn't; don't try to start it.
- Each harness the agents use needs a credential saved in the system keyring with `gnat auth login claude` (a token from `claude setup-token`) or `gnat auth login ox` (an OpenRouter API key). The user runs these themselves, since they prompt for the secret; `gnat up` says which is missing. `GH_TOKEN` comes from the environment and falls back to the active `gh` account. Never echo, print, or commit tokens.
- Only the control container holds `GH_TOKEN`. Agents have none: they clone, fetch, push, and use pull requests (`gnat-container pr`) through the control plane, which limits them to the swarm's repository and their own branches, `SWARM/AGENT/NAME`, refuses deleting branches and force-pushing, and lets an agent merge only if it was created with `--merge`. Agents do hold their harness's credential.

## Set up

```sh
gnat create NAME --repo OWNER/NAME [--profile SWARM_PROFILE|PATH] [--dockerfile FILE] [--file HOST_PATH[:WORKSPACE_PATH]]...
gnat agent create NAME AGENT [--profile PROFILE|PATH] [--instructions TEXT] [--harness claude|ox] [--model MODEL] [--effort LEVEL] [--merge]
gnat up NAME
```

- `gnat list` and `gnat agent list NAME` (name, harness, model, effort, merge) show what exists.
- `gnat profiles list` and `gnat profiles list --swarm` show the user's agent profiles (role instructions and settings) and swarm profiles (a whole set of agents). Use one when it fits what the user asked for; flags override an agent profile's settings. `--profile` also takes a profile YAML file's path (anything with a slash or ending in `.yaml`/`.yml`).
- `make install` provides four agent profiles: `coder` implements changes, `reviewer` reports code review findings, `researcher` investigates questions and compares options, and `orchestrator` hands out work to its teammates, has their pull requests reviewed, and merges them.
- **You are the orchestrator.** When you drive gnat, you divide the work, send it, and decide what gets reviewed and merged, so don't create an `orchestrator` agent. Build the swarm from `coder`, `reviewer`, and `researcher` agents. Of the bundled swarm profiles, use `small` (two coders, two reviewers) or `large` (four of each), not `small-orchestrated` or `large-orchestrated`, which add an orchestrator.
- `--harness` defaults to `claude` (Claude Code). `ox` runs Ox: `--model` takes a qualified ID such as `openrouter:deepseek/deepseek-v4.1-flash`. Efforts: for claude, `low`, `medium`, `high`, `xhigh`, `max`; for ox, `default`, `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` (the model must support it). Both default to the harness's own.
- A profile can set the harness, model, and effort too, and its settings win over these defaults: the bundled `reviewer` runs Ox on DeepSeek. If the user named a harness or model, pass it as a flag, and check `gnat agent list NAME` after creating agents.
- `--dockerfile` is optional, and must start `FROM gnat-agent`. Use it only for packages the repo's build or tests need that the base image lacks. The base image has build-essential, python3, uv, git, and common CLI tools (`rg`, `fd`, `jq`, `sqlite3`, `ps`, `lsof`, ...). Once it exists (after any `gnat up`), check whether it already builds and tests the repo: `docker run --rm -e GH_TOKEN gnat-agent bash -c 'git clone -q https://x-access-token:$GH_TOKEN@github.com/OWNER/NAME r && cd r && TEST_COMMAND'` with `GH_TOKEN` exported. If that passes, leave `--dockerfile` out; if not, install only what failed. The build context is the Dockerfile's directory. If a test fails or flakes on `main` in the container, tell agents which one and why when you send work; otherwise each agent diagnoses it separately.
- **Agents (and files) can only be added, changed, or removed while the swarm is down.** To change them: `gnat down NAME`, then `gnat agent create|set|delete`, then `gnat up NAME`. `gnat agent set NAME AGENT [--profile] [--instructions] [--harness] [--model] [--effort] [--merge]` changes only the settings given and keeps the agent's clone, sessions, queue, and history; changing its harness starts it in a fresh session. `gnat down` keeps agents' clones (including uncommitted work), sessions, queued messages, and logs, and `gnat up` carries on from them; a turn running at `down` is reported as interrupted, so send the agent a message to resume it. `gnat agent delete` deletes that agent's clone, sessions, and history, so make sure its work is pushed first; an agent created again by the same name starts fresh.
- `gnat up` needs at least one agent, builds images, and returns once the control plane answers, so chaining `gnat up NAME && gnat send ...` is safe. It prints the web UI's URL when it finishes; the user doesn't see your tool output, so give them the URL. Agents may still be cloning (`gnat status` shows `starting`); messages queue until they're ready.
- Running containers keep the swarm's code from `gnat up`. After updating gnat (`make install`), `gnat down` and `gnat up` to use it.

### Agent instructions

gnat already tells each agent its teammates, how to message them, that messages arriving mid-turn (`--now`) are genuine, the tools its container has, and never to reveal credentials. Put the role and working rules in `--instructions`. Good instructions say:

- For each task, create a new branch off the latest `origin/main` (gnat already tells them their branches must be named `SWARM/AGENT/NAME`).
- Commit in the repo's style, push the branch, and open a pull request.
- Force-pushing is refused, so to bring a branch up to date with `main`, merge `origin/main` into it; don't rebase.
- Run the project's tests and linter before pushing (give the commands).
- Work only on what they were given. Suggestions for a teammate's work go in their final reply, not to the teammate, so you decide who does them; otherwise teammates start the same follow-up at once and spend turns acknowledging each other.
- End every turn with a short report: the PR URL, what changed, and anything blocked or needing a decision. If blocked on a decision, say so instead of guessing.
- Their area, if split by role (e.g. "you own `src/api`; don't edit other directories").

Use `--model` and `--effort` to make cheap agents for simple work (e.g. `--model haiku --effort low`, or an Ox agent on a cheap OpenRouter model) and stronger ones for hard work.

## Dividing work

Each agent has its own clone, so collisions show up as merge conflicts in PRs, not live. Prevent them:

- Size the swarm to the work. When most of it runs through one critical path, a second coder mostly waits on the first; one coder and a reviewer will do nearly as much with fewer coordination turns.
- Give each agent a distinct task, and distinct files or directories where possible. Name the files each owns in the message.
- Split by feature, not by layer, when one layer can't be tested without the other. An agent that owns a library its teammate's code must first be able to call writes code it can't test, and reports itself blocked; one that takes a feature from end to end produces work that can be tested and merged.
- Don't give two agents work that touches the same files, a shared lockfile, or the same generated output without telling them to coordinate (they can message each other, but two agents can reply to each other forever).
- Separate files don't prevent conflicts in files every change registers itself in (a module list, a registry, a spec's index). Before sending, list those insertion points, and either give each agent a distinct place in them or plan to resolve them yourself and test the combined result in a scratch clone, even when each PR passes on its own.
- Sequence dependent work: wait for the first PR to merge, then tell the next agent to pull `main` and start.
- Prefer a short idle to a stack. Branch new work off `main`, not off an unmerged PR. When an agent's next task depends on its open PR, give it independent work or let it wait for the merge: each layer of a stack costs draft and ready changes, and a merge of `origin/main` at every layer below it, all of which cost more than one idle review.
- Prefer one task per message with an explicit goal and a "done" condition. Agents can't see your conversation; include the issue text, file paths, and constraints.

## Sending work

```sh
gnat send NAME AGENT... -m "TEXT"          # named agents; every agent if none named
echo "long task" | gnat send NAME AGENT    # message from stdin when -m is omitted
```

- Messages queue per agent and run one at a time in the same session, so an agent remembers earlier messages.
- `--new`: start in a fresh session (use between unrelated tasks so old context doesn't bleed in; later messages continue the new session). Can't be combined with `--now`.
- `--now`: deliver into the agent's running turn at its next tool call, for either harness (to redirect or add a constraint). If the turn ends first, it starts the next turn ahead of the queue. `gnat status` counts it as `(1 now)` until it's delivered. Don't use it for ordinary tasks.
- `--wait [--timeout SECONDS]`: send, then block until the agents finish and print their final replies. Use it for short tasks; for long ones, send without it and poll.
- Different tasks per agent need separate `send` commands, one per agent.
- Every message is a turn of its own, at the agent's model and effort, with its whole context. Don't send notes that need no work now ("#38 merged; merge `origin/main` when convenient"): fold them into the agent's next task.

## Watching

```sh
gnat status NAME [AGENT...]   # state, queued, turns, time since last event, usage, how the last turn ended
gnat wait NAME [AGENT...] [--any] [--timeout SECONDS]   # block until idle (--any: until one is), print final replies
gnat logs NAME [AGENT...] [-n LINES] [-f]       # messages and agents' output; works while down
gnat web NAME [--no-open]     # print the web UI URL (a card per agent: setup, cost, latest output; maximize for the full transcript)
```

- `gnat status` breaks each agent's queue down by sender, such as `3 queued: 1 from you (1 now), 2 from coder2`: messages from teammates, and `--now` messages not yet delivered, are shown apart from yours.
- `gnat status` shows, for a busy agent, how long since its last event (`last event 3m12s ago`). Agents think for minutes at high effort, but a long silence is worth a look with `gnat logs NAME AGENT -n 20`; `gnat stop` it if it's stuck.
- `gnat status` and the web UI also show each agent's usage so far: tokens and cost in US dollars (Claude's at API list prices; on a subscription, cost is only a comparison), and how full its context window is (`context 23% of 1M`). `gnat status` ends with the Claude account's usage limits (`usage limits: 5h 7% (resets 13:20), 7d 49% ...`), which all Claude agents and the user's own sessions share; check them before starting a large run, and tell the user when a window is nearly used up. They're all still there after `gnat down`.
- `gnat wait --any` returns as soon as one of the agents is idle, printing the replies of those that are, each under its name, and on stderr `still waiting on: ...` for the rest. An agent already idle counts at once, so run it again naming only the agents still waiting. Use it to collect replies as agents finish at different times.
- `gnat wait` exits 1 if an agent's last turn failed or was stopped, and 124 on timeout. A timeout isn't a failure: the agents keep working; check `gnat status` and wait again.
- `gnat logs -f` never exits; don't run it in the foreground. Use `-n 50` for a snapshot, or run it in the background.
- Foreground commands time out in your shell: for turns that take many minutes, run `gnat wait` with `--timeout` in a loop or in the background.
- The agents' final replies from `gnat wait` are the first thing to read, then logs for detail.

## Stopping

- `gnat stop NAME [AGENT...]` ends an agent's running turn (for Claude, killing the processes its tools started; for Ox, cancelling the prompt). Its queue, clone, and session survive and it moves to its next message. Use it for runaway or wrong-direction work; then send a corrected message (add `--new` if the session is polluted).
- If two agents are bouncing messages at each other, `gnat stop` both, then `gnat down` if needed.
- When the user asks to stop and the agents' work so far is worth keeping, first `gnat send --now` them to stop, save what they have (commit it, or write the report or patch the task asked for), and reply with it, and wait for that reply. `gnat stop` and `gnat down` end the turn without a reply.

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

Agents' work is in the pull requests they open, not on the host. Anything an agent didn't push stays in its clone, on a Docker volume that survives `gnat down`. `gnat cp NAME AGENT:PATH HOST_PATH` copies a file or directory out of it, up or down (a relative `PATH` is in the clone; while the swarm is down, only paths in the clone and sessions). The destination works as in `docker cp`: a directory copied to an existing directory lands inside it, so `AGENT:.` or `AGENT:dir/.` copies the contents instead; to a path that doesn't exist, that path is created with the directory's contents. Read each agent's reply (`gnat wait`), then review the PRs with `gh pr list` / `gh pr view` / `gh pr diff` in the repo, and have the user merge. If a PR needs changes, `gnat send` the agent what to fix (without `--new`, so it keeps its context).

A strong agent can review a cheaper agent's PR: send it the PR number and ask it to check out the branch, test what the PR claims, and post its findings with a verdict: `gnat-container pr review --approve` or `--request-changes`. Either labels the PR `gnat:approved` or `gnat:changes-requested` (on PRs opened by the operator's account, which GitHub won't let approve itself, the review is posted as a comment saying which), so `gh pr list --label gnat:approved` lists what's been approved. A push after the review doesn't remove the label. When several PRs arrive at once, don't queue them all on one reviewer: have idle coders review each other's PRs, telling them they're reviewing and not to change files.

Passing tests show a change does what its own tests check, not that it keeps the contracts the rest of the code relies on. Have reviewers exercise those too: for a new type, that existing type checks and dispatch accept it; for a new command, that it behaves like its siblings.

### When the user has you merge

If the user delegates merging, or the work is a chain of dependent PRs where merging is part of the loop:

- Merge each PR as soon as it's approved and passes on the host, before sending more work. Approved PRs left open grow stacks and merge conflicts.
- Before merging, test the combined result in a scratch clone on the host: start from a freshly fetched `origin/main` (not the user's checkout, which may have diverged), fetch the PR branches, merge them locally in the order you'll merge them, and build and test from scratch on the host (anything built inside an agent's container is a Linux build).
- Stacked PRs (B contains A's commits): check which merge methods the repository allows (`gh repo view --json mergeCommitAllowed,rebaseMergeAllowed,squashMergeAllowed`). With merge commits, merge A, then B, each with `gh pr merge --merge`; if B targets A's branch rather than `main`, retarget it first (`gh pr edit B --base main`). With rebase merging only, retarget B at `main`, merge it with `gh pr merge B --rebase`, which brings A's commits along, then close A (`gh pr close A`) with a comment naming B.
- After a merge, tell agents whose work depends on it to merge `origin/main` into their branch, in their next task rather than in a message of its own.

## Review-only swarms

When the agents' job is to review, investigate, or measure rather than change the repo, the deliverable is each agent's final reply, not a PR:

- Tell agents not to commit, push, or open PRs, and to end with their report in the final reply (give a length limit).
- Tell them to end with an explicit outcome: confirmed findings, nothing found, or blocked (and on what). A reply that only acknowledges a `--now` message isn't a result; tell agents to keep working after acknowledging one. Have them cite file and line from the source as it is, not from memory.
- Tell them to build and test with the repo's documented commands, and if those fail, to report the exact failure rather than substitute their own setup.
- Tell them to keep scratch files, prototypes, and measurement scripts in their clone (untracked), not `/tmp`, so they survive `gnat down` and you can copy them out with `gnat cp`.
- When a deliverable is a patch or diff and an agent gets a second task, say what to base it on (`origin/main` or the previous task's branch) and what to diff against, or the second patch will contain the first.
- For benchmarks, tell agents they share the machine with their teammates, so timings are noisy: interleave before and after runs and repeat them, and confirm small effects on the host.
- Collect replies with `gnat wait NAME AGENT... --any`, so one slow or stuck agent doesn't keep you from the others, and save each reply to a file as it arrives.

## Tear down

- `gnat down NAME` stops the containers and keeps everything else: clones, sessions, queued messages, logs, agents, and files.
- `gnat delete NAME` also deletes the swarm's definition, image, logs, and agents' clones and sessions. Confirm with the user before deleting, and check first that nothing worth keeping is unpushed: logs are the only record of the run, and anything not pushed is gone.
- Check `gnat list` afterwards for swarms left up.

## Feedback

File a report only when the swarm's repository is owned by `kkestell` (`--repo kkestell/...`); for any other repository, skip this section. Post it, and every other comment or issue on `kkestell/gnat`, as the `kkestell` account, whichever `gh` account is active: prefix each `gh` command with `GH_TOKEN=$(gh auth token --user kkestell)`. If that account isn't logged in, don't post; tell the user.

After the run, whether the task succeeded, failed, or was abandoned, evaluate how well the swarm solved the problem and file the report as a new issue on the gnat repository: `GH_TOKEN=$(gh auth token --user kkestell) gh issue create --repo kkestell/gnat --title "Feedback: NAME, YYYY-MM-DD HH:MM" --body-file REPORT.md` (swarm name, local time). The issue is public: leave out secrets, tokens, and anything from the target repo that isn't already public. Write it before `gnat delete`, which removes the logs. Base it on evidence: the agents' final replies, `gnat logs`, `gnat status`, the PRs, and what you had to do as the operator.

The report is only as useful as what's new in it. Before writing, read the open feedback issues (`gh issue list --repo kkestell/gnat --search "Feedback in:title"`). A problem already reported gets one line naming that issue, and only if this run adds evidence. Before recommending a change, check `gnat COMMAND -h` and this skill to see whether it already exists.

Use these headings:

- **Task and setup**: the task, agents (harness, model, effort), Dockerfile, files.
- **Outcome**: what was delivered and what wasn't, whether the PRs were correct and mergeable, and cost per `gnat status`.
- **Division of work**: whether it worked: merge conflicts, duplicated or idle agents, dependency waits, agent-to-agent messaging.
- **Interventions and friction**: every point where you had to intervene, guess, retry, or work around gnat, including confusing CLI output, missing commands or flags, and gaps or errors in this skill or the docs. Label each as gnat, this skill, agent behavior, or your own mistake; include the last two only if gnat or the skill could have prevented them.
- **Would one agent have done as well?**: or no swarm at all.
- **Recommendations**: ranked by impact, each naming the change (a command, flag, control-plane or agent behavior, default instructions, or a section of this skill), the problem from this run it addresses, and the expected effect.

Leave out what worked without incident, and generic advice that this run's evidence doesn't support.
