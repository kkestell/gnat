// The pretend swarm's repository's pull requests. Some name the agent that opened them,
// to show how that will look; for now the real ones don't.
import type { PullRequest } from "../src/pulls";

const minutesAgo = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString();

const repo = "https://github.com/kkestell/swarm-test/pull/";

export function pulls(): PullRequest[] {
  return [
    { number: 12, title: "Add a --json flag to hello.py", state: "open", draft: false, branch: "add-json-flag",
      author: "kkestell", agent: "beta", updated_at: minutesAgo(3) },
    { number: 14, title: "Document the workspace fixture layout in README", state: "open", draft: true, branch: "readme-layout",
      author: "kkestell", agent: "alpha", updated_at: minutesAgo(18) },
    { number: 13, title: "Add a pytest smoke test for hello.py and wire it into a Makefile target so CI can run it",
      state: "open", draft: false, branch: "pytest-smoke-test-and-makefile-target",
      author: "kkestell", agent: null, updated_at: minutesAgo(95) },
    { number: 11, title: "Ignore .venv", state: "merged", draft: false, branch: "ignore-venv",
      author: "kkestell", agent: "gamma", updated_at: minutesAgo(60 * 5) },
    { number: 10, title: "Rename the greeting constant", state: "closed", draft: false, branch: "rename-greeting",
      author: "kkestell", agent: null, updated_at: minutesAgo(60 * 30) },
    { number: 9, title: "Print the greeting with a trailing newline", state: "merged", draft: false, branch: "newline",
      author: "kkestell", agent: null, updated_at: minutesAgo(60 * 72) },
  ].map(pull => ({ ...pull, url: repo + pull.number }) as PullRequest);
}
