import { expect, test } from "bun:test";
import { AgentState, RECENT_ITEMS, columns, contextSummary, dollars, limitWindows, parseLimits, percent, resetTime, setupLine, uptime, usageSummary, type Tool } from "../src/swarm";

function state(events: unknown[]): AgentState {
  const agent = new AgentState();
  for (const event of events) agent.apply(event);
  return agent;
}

test("each kind of event goes in the transcript", () => {
  const agent = state([
    { type: "swarm_message", text: "**go**", sender: "beta", new: true, mid_turn: false },
    { type: "thought", text: "hidden" },
    { type: "message", text: "Let me *look*." },
    { type: "swarm_sent", to: "gamma", text: "on it" },
    { type: "tool_call", id: "t1", name: "Task", input: { description: "explore" } },
    { type: "message", text: "inside", parent: "t1" },
    { type: "tool_call", id: "t2", name: "edit", title: "Edit a.py", input: {}, parent: "t1" },
    { type: "tool_result", id: "t2", error: true, output: "failed" },
    { type: "tool_result", id: "t1", error: false, output: "" },
    { type: "turn_end", reply: null, error: "rate limited" },
    { type: "error", text: "Error: no auth" },
    { type: "swarm_done", exit: 2, stopped: false },
  ]);
  expect(agent.items).toEqual([
    { kind: "session" },
    { kind: "message", text: "**go**", sender: "beta", midTurn: false },
    { kind: "text", text: "Let me *look*." },
    { kind: "sent", text: "on it", to: "gamma" },
    {
      kind: "tool", summary: "Task: explore", output: "(no output)", error: false, items: [
        { kind: "text", text: "inside" },
        { kind: "tool", summary: "edit: Edit a.py", output: "failed", error: true, items: [] },
      ],
    },
    { kind: "notice", text: "error: rate limited" },
    { kind: "notice", text: "Error: no auth" },
    { kind: "notice", text: "the turn failed with status 2" },
  ]);
});

test("the card keeps the transcript's latest entries", () => {
  const agent = state([
    { type: "swarm_message", text: "fix it", sender: "beta", new: true },
    { type: "message", text: "On it." },
    { type: "tool_call", id: "t1", name: "Task", input: { description: "explore" } },
    { type: "tool_call", id: "t2", name: "Bash", input: { command: "make" }, parent: "t1" },
    { type: "tool_result", id: "t2", error: true, output: "boom" },
    { type: "message", text: "Done." },
    { type: "swarm_done", stopped: true },
  ]);
  expect(agent.recent).toEqual([
    { item: { kind: "session" }, nested: false },
    { item: { kind: "message", text: "fix it", sender: "beta", midTurn: false }, nested: false },
    { item: { kind: "text", text: "On it." }, nested: false },
    { tools: [agent.items[3] as Tool, { kind: "tool", summary: "Bash: make", output: "boom", error: true, items: [] }] },
    { item: { kind: "text", text: "Done." }, nested: false },
    { item: { kind: "notice", text: "the turn was stopped" }, nested: false },
  ]);
});

test("the card keeps only the latest entries", () => {
  const agent = state(Array.from({ length: RECENT_ITEMS + 5 }, (_, i) => ({ type: "message", text: String(i) })));
  expect(agent.recent.length).toBe(RECENT_ITEMS);
  expect(agent.recent[0]).toEqual({ item: { kind: "text", text: "5" }, nested: false });
});

test("the card puts tool calls in a row on one line", () => {
  const agent = state([
    { type: "tool_call", id: "t1", name: "Read", input: { file_path: "a.py" } },
    { type: "tool_call", id: "t2", name: "Task", input: { description: "explore" } },
    { type: "message", text: "inside", parent: "t2" },
    { type: "tool_call", id: "t3", name: "Bash", input: { command: "make" }, parent: "t2" },
    { type: "tool_call", id: "t4", name: "Bash", input: { command: "make test" } },
    { type: "message", text: "Done." },
    { type: "tool_call", id: "t5", name: "Bash", input: { command: "git push" } },
  ]);
  expect(agent.recent.map(line => ("tools" in line ? line.tools.map(tool => tool.summary) : line.item))).toEqual([
    ["Read: a.py", "Task: explore"],
    { kind: "text", text: "inside" },
    ["Bash: make", "Bash: make test"],
    { kind: "text", text: "Done." },
    ["Bash: git push"],
  ]);
});

test("usage events are totalled, and a done turn's usage isn't counted again", () => {
  const usage = { input_tokens: 5, cache_read_tokens: 12300, cache_write_tokens: 700, output_tokens: 950, cost: 0.0421 };
  const agent = state([
    { type: "usage", ...usage },
    { type: "usage", cost: 1 },
    { type: "swarm_done", exit: 0, stopped: false, usage },
  ]);
  expect(usageSummary(agent.usage)).toBe("13k in (12.3k cached), 950 out, $1.04");
  expect(agent.items).toEqual([]);
});

test("records from older versions are skipped", () => {
  const agent = state([
    "a line Claude printed",
    { type: "assistant", message: { content: [{ type: "text", text: "old" }] } },
    { type: "user", message: { content: [{ type: "tool_result", tool_use_id: "x" }] } },
    { type: "result", is_error: true, result: "old error" },
    { type: "message", text: "new" },
  ]);
  expect(agent.items).toEqual([{ kind: "text", text: "new" }]);
});

test("the setup line names what isn't the harness's default", () => {
  expect(setupLine({ name: "alpha", harness: "claude", model: "haiku", effort: "low" })).toBe("claude haiku (low)");
  expect(setupLine({ name: "gamma", harness: "ox", model: null, effort: null })).toBe("ox");
});

test("agents are laid out in columns, later ones taking any extra", () => {
  const shape = (n: number) => columns(Array.from({ length: n }, (_, i) => i)).map(column => column.length);
  expect(shape(1)).toEqual([1]);
  expect(shape(2)).toEqual([1, 1]);
  expect(shape(3)).toEqual([1, 1, 1]);
  expect(shape(4)).toEqual([2, 2]);
  expect(shape(5)).toEqual([1, 2, 2]);
  expect(shape(6)).toEqual([2, 2, 2]);
  expect(shape(8)).toEqual([2, 3, 3]);
  expect(shape(10)).toEqual([2, 2, 3, 3]);
  expect(shape(16)).toEqual([4, 4, 4, 4]);
  expect(columns([0, 1, 2, 3, 4])).toEqual([[0], [1, 2], [3, 4]]);
});

test("uptime is in the two largest units", () => {
  const now = Date.parse("2026-10-21T12:00:00Z");
  expect(uptime("2026-10-21T11:59:15Z", now)).toBe("45s");
  expect(uptime("2026-10-21T11:48:00Z", now)).toBe("12m");
  expect(uptime("2026-10-21T09:46:00Z", now)).toBe("2h 14m");
  expect(uptime("2026-10-18T08:00:00Z", now)).toBe("3d 4h");
  expect(uptime("not a time", now)).toBe("");
});

test("costs are to the cent", () => {
  expect(dollars(0.0022)).toBe("$0.00");
  expect(dollars(0.0421)).toBe("$0.04");
  expect(dollars(1.2345)).toBe("$1.23");
});

test("the context is the latest reported, keeping the size when a report has none", () => {
  const agent = state([
    { type: "context", used: 1000, size: null },
    { type: "context", used: 230000, size: 1000000 },
    { type: "context", used: 250000, size: null },
  ]);
  expect(agent.context).toEqual({ used: 250000, size: 1000000 });
  expect(contextSummary(agent.context!)).toBe("25% of 1M");
  expect(contextSummary({ used: 230000 })).toBe("230k");
  expect(percent(0.001)).toBe("<1%");
  expect(percent(0)).toBe("0%");
});

test("limits list the windows that haven't reset, the short-named first", () => {
  const limits = parseLimits({
    type: "limits",
    windows: {
      seven_day_opus: { used: 0.1, resets_at: 3000 },
      seven_day: { used: 0.49, resets_at: 2000 },
      five_hour: { used: 0.07, resets_at: 1000 },
      old: { used: 0.9, resets_at: 10 },
      bogus: { used: 0.5 },
    },
  })!;
  expect(limitWindows(limits, 100_000).map(w => [w.name, w.used])).toEqual([["5h", 0.07], ["7d", 0.49], ["seven day opus", 0.1]]);
  expect(parseLimits({ type: "limits", windows: {} })).toBeNull();
  expect(parseLimits({ type: "limits" })).toBeNull();
});

test("reset times are as brief as now allows", () => {
  const now = new Date(2026, 9, 23, 9, 0);
  const today = new Date(2026, 9, 23, 13, 20);
  expect(resetTime(today, now)).toBe(today.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" }));
  expect(resetTime(new Date(2026, 9, 25, 22, 0), now).startsWith("Sun ")).toBe(true);
  expect(resetTime(new Date(2026, 9, 31, 8, 0), now)).toBe("Oct 31");
});
