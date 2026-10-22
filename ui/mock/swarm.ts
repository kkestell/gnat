// A pretend swarm for working on the UI without one: its agents, what they've done so
// far, and turns that keep arriving while the page is open.
import type { Agent } from "../src/swarm";

// The swarm: its repository, and when it came up, a couple of hours before the mock did.
export const swarm = { repo: "kkestell/swarm-test", started_at: new Date(Date.now() - 134 * 60_000).toISOString() };

const setups: Agent[] = [
  { name: "alpha", harness: "claude", model: "haiku", effort: "low" },
  { name: "beta", harness: "claude", model: "opus", effort: "high" },
  { name: "gamma", harness: "ox", model: "openrouter:deepseek/deepseek-v4.1-flash", effort: null },
  { name: "delta", harness: "claude", model: null, effort: null },
];

const NAMES = ["alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "theta", "iota", "kappa",
  "lambda", "mu", "nu", "xi", "omicron", "pi"];

// The swarm's first n agents (4 unless asked for another, up to 16). Past delta, each is
// a copy of alpha, beta, gamma, or delta in turn, and does what that one does.
export function agents(n = 4): Agent[] {
  return NAMES.slice(0, Math.min(Math.max(n, 1), NAMES.length))
    .map((name, i) => ({ ...setups[i % setups.length]!, name }));
}

// The original of each agent: whose events it has.
const original = (name: string) => setups[NAMES.indexOf(name) % setups.length]!.name;

type Event = Record<string, unknown>;
type Recorded = { agent: string; event: Event };

let ids = 0;
const id = () => `toolu_mock${++ids}`;

function usage(cost: number, output: number): Event {
  return { type: "usage", input_tokens: 6, cache_read_tokens: 48666, cache_write_tokens: 7158, output_tokens: output, cost };
}

function done(exit: number | null = 0, stopped = false): Event {
  return { type: "swarm_done", exit, stopped };
}

// A tool call and its result.
function tool(name: string, input: Record<string, unknown>, output: string, extra: Event = {}): Event[] {
  const call = id();
  return [
    { type: "tool_call", id: call, name, input, ...extra },
    { type: "tool_result", id: call, error: false, output },
  ];
}

// A message to another agent: the call that sends it, the control plane's record of it,
// and the call's result.
function sent(to: string, text: string): Event[] {
  const call = id();
  return [
    { type: "tool_call", id: call, name: "Bash", input: { command: `/opt/gnat/gnat-container send ${to} -m '${text}'` } },
    { type: "swarm_sent", to, text },
    { type: "tool_result", id: call, error: false, output: `sent to ${to}` },
  ];
}

// A turn like a real one: a message, some tool calls, a reply.
function summaryTurn(): Event[] {
  return [
    { type: "swarm_message", new: false, mid_turn: false, sender: null,
      text: "Read the repository and summarize its layout and purpose in a few bullet points. Run a couple of shell commands to look around. Don't change anything." },
    ...tool("Bash", { command: 'git ls-files | head -100 && echo "---" && git ls-files | wc -l', description: "List tracked files" },
      ".gitignore\nAGENTS.md\nREADME.md\nsrc/hello.py\n---\n4"),
    ...tool("Bash", { command: "cat README* 2>/dev/null | head -60; echo ---; cat AGENTS.md", description: "Show README and AGENTS.md" },
      "# Test workspace\n\nFixture for testing what `swarm` copies into agent containers.\n---\n# Workspace instructions"),
    ...tool("Read", { file_path: "/workspace/src/hello.py" }, 'print("hello from the workspace")'),
    { type: "message", text:
      "This is a small test fixture, not a real project. Nothing was changed.\n\n" +
      "- **Purpose:** a fixture for testing what `swarm` copies into agent containers.\n" +
      "- **Layout:** four tracked files: `README.md`, `AGENTS.md`, `.gitignore`, and `src/hello.py`.\n" +
      "- **Code:** `src/hello.py` prints one line." },
    usage(0.0022, 587),
    done(),
  ];
}

// A longer turn with a subagent, a failed tool call, and Markdown of most kinds.
function featureTurn(): Event[] {
  const task = id();
  const failed = id();
  return [
    { type: "swarm_message", new: true, mid_turn: false, sender: null,
      text: "Add a `--json` flag to `hello.py` that prints the greeting as JSON. Open a PR." },
    { type: "message", text: "I'll look at how the script is structured first." },
    { type: "tool_call", id: task, name: "Task", input: { description: "Explore the CLI entry points" } },
    { type: "message", parent: task, text: "Searching for argument parsing." },
    ...tool("Grep", { pattern: "argparse|sys.argv", path: "/workspace" }, "(no matches)", { parent: task }),
    { type: "tool_result", id: task, error: false, output: "There's no argument parsing yet; `hello.py` is a single print." },
    ...tool("Edit", { file_path: "/workspace/src/hello.py" }, "The file has been updated."),
    { type: "tool_call", id: failed, name: "Bash", input: { command: "python3 -m pytest -q", description: "Run the tests" } },
    { type: "tool_result", id: failed, error: true, output: "/usr/bin/python3: No module named pytest" },
    ...tool("Bash", { command: "python3 src/hello.py --json", description: "Try the flag" }, '{"greeting": "hello from the workspace"}'),
    ...tool("Bash", { command: "git push -u origin add-json-flag && gh pr create --fill" },
      "https://github.com/kkestell/swarm-test/pull/12"),
    ...sent("gamma", "Can you review PR #12 when you get a chance?"),
    { type: "message", text:
      "Opened **#12**: https://github.com/kkestell/swarm-test/pull/12\n\n" +
      "| Flag | Output |\n|:--|:--|\n| (none) | `hello from the workspace` |\n| `--json` | `{\"greeting\": ...}` |\n\n" +
      "```python\nif args.json:\n    print(json.dumps({\"greeting\": GREETING}))\n```\n\n" +
      "> pytest isn't installed in the container, so I checked the flag by hand." },
    usage(0.4183, 4210),
    done(),
  ];
}

function failedTurn(): Event[] {
  return [
    { type: "swarm_message", new: false, mid_turn: false, sender: "beta", text: "Can you review PR #12 when you get a chance?" },
    ...tool("Bash", { command: "gh pr view 12 --json title,files" }, '{"title":"Add --json flag","files":[{"path":"src/hello.py"}]}'),
    { type: "error", text: "API Error: 529 overloaded" },
    { type: "turn_end", reply: null, error: "rate limited" },
    done(1),
  ];
}

const history: Recorded[] = [
  ...summaryTurn().map(event => ({ agent: "alpha", event })),
  ...featureTurn().map(event => ({ agent: "beta", event })),
  ...summaryTurn().map(event => ({ agent: "gamma", event })),
  ...failedTurn().map(event => ({ agent: "gamma", event })),
];

// Each record so far, and what to tell of each new one.
const log: Recorded[] = [...history];
const listeners = new Set<(record: Recorded) => void>();

// Every record so far for the swarm's first n agents, then each new one, until unwatched.
export function watch(n: number, send: (record: Recorded) => void): () => void {
  const names = agents(n).map(agent => agent.name);
  const forEach = (record: Recorded) => {
    for (const agent of names) if (original(agent) === record.agent) send({ agent, event: record.event });
  };
  for (const record of log) forEach(record);
  listeners.add(forEach);
  return () => listeners.delete(forEach);
}

// Keeps the agent busy: a turn's events a few seconds apart, then again after a pause.
function keepBusy(agent: string, turn: () => Event[], interval: number, pause: number) {
  let events: Event[] = [];
  const tick = () => {
    if (!events.length) events = turn();
    const event = events.shift()!;
    const record = { agent, event };
    log.push(record);
    for (const listener of listeners) listener(record);
    setTimeout(tick, events.length ? interval : pause);
  };
  setTimeout(tick, pause);
}

// alpha and delta keep working while the page is open.
keepBusy("alpha", featureTurn, 1500, 4000);
keepBusy("delta", summaryTurn, 2500, 3000);
