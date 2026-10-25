import { test, expect } from "bun:test";
import {
  AgentState,
  groupItems,
  agentState,
  agentStatus,
  columns,
  setupLine,
  usageSummary,
} from "../src/swarm";
import { initial, event, agent } from "./fixtures";

test("streamed Markdown is coalesced and native child activity stays inside its tool", () => {
  const state = new AgentState();
  state.apply({ type: "text_delta", text: "**Hello" }, "turn");
  state.apply({ type: "text_delta", text: "**" }, "turn");
  state.apply(
    {
      type: "tool_call",
      id: "child",
      name: "Agent",
      input: { description: "Inspect code" },
    },
    "turn",
  );
  state.apply(
    { type: "text_delta", text: "Nested", parent_tool_use_id: "child" },
    "turn",
  );
  state.apply({ type: "tool_result", id: "child", output: "Done" }, "turn");
  expect(state.items[0]).toEqual({ kind: "text", text: "**Hello**" });
  expect(state.items[1]).toEqual({
    kind: "tool",
    summary: "Agent: Inspect code",
    items: [{ kind: "text", text: "Nested" }],
    output: "Done",
    error: false,
  });
});

test("a restored conversation renders its own delivered messages and usage once", () => {
  const snapshot = initial();
  snapshot.workers = [agent("worker")];
  snapshot.messages = [
    {
      id: "m",
      agent_id: "orchestrator",
      sender: "worker",
      text: "Result",
      state: "delivered",
    },
  ];
  snapshot.events = [
    event(5, { type: "turn_started", message_id: "m" }),
    event(6, { type: "text_delta", text: "Thanks" }),
    event(7, {
      type: "usage",
      input_tokens: 5,
      cache_read_input_tokens: 10,
      output_tokens: 9,
      cost: 0.05,
    }),
    {
      ...event(8, { type: "message", text: "Earlier" }),
      conversation_id: "old",
    },
    event(9, { type: "message", text: "Other agent" }, "worker"),
  ];
  const state = agentState(snapshot, snapshot.orchestrator);
  expect(state.items).toEqual([
    { kind: "message", sender: "worker", text: "Result" },
    { kind: "text", text: "Thanks" },
  ]);
  expect(usageSummary(state.usage)).toBe("15 in (10 cached), 9 out, $0.05");
  expect(agentState(snapshot, snapshot.orchestrator, "old").items).toEqual([
    { kind: "text", text: "Earlier" },
  ]);
});

test("tool identifiers reused in a later turn do not attach results to the earlier call", () => {
  const state = new AgentState();
  state.apply(
    { type: "tool_call", id: "tool", name: "Read", input: { path: "first" } },
    "one",
  );
  state.apply(
    { type: "tool_call", id: "tool", name: "Read", input: { path: "second" } },
    "two",
  );
  state.apply(
    { type: "tool_result", id: "tool", output: "first output" },
    "one",
  );
  expect(state.items[0]).toMatchObject({ output: "first output" });
  expect(state.items[1]).not.toHaveProperty("output");
});

test("missing metrics remain unknown and tools group in the shared transcript", () => {
  const state = new AgentState();
  state.apply({ type: "usage", cost: null }, "turn");
  state.apply(
    { type: "tool_call", id: "one", name: "Read", input: {} },
    "turn",
  );
  state.apply(
    { type: "tool_call", id: "two", name: "Read", input: {} },
    "turn",
  );
  expect(state.usage).toEqual({});
  const groups = groupItems(state.items);
  expect(groups.length).toBe(1);
  expect(Array.isArray(groups[0]) && groups[0].length).toBe(2);
  expect(setupLine(agent())).toBe("claude claude-opus-5-5 (high)");
  expect(columns([1, 2, 3, 4, 5]).map((c) => c.length)).toEqual([1, 2, 2]);
});

test("membership labels a worker until it is an active member, then lifecycle does", () => {
  const worker = agent("worker");
  const status = (membership: string, lifecycle = "pending") =>
    agentStatus({ ...worker, membership, lifecycle });
  expect(status("provisioning")).toBe("provisioning");
  expect(status("provisioning_failed")).toBe("provisioning failed");
  expect(status("removing", "ready")).toBe("removing");
  expect(status("removed", "ready")).toBe("removed");
  expect(status("active", "running")).toBe("running");
  expect(status("active", "error")).toBe("error");
});

test("cancelled input appears once with its sender and is never shown as delivered", () => {
  const snapshot = initial();
  const worker = agent("worker");
  snapshot.removed_workers = [{ ...worker, membership: "removed" }];
  snapshot.messages = [
    {
      id: "queued",
      agent_id: "worker",
      sender: "orchestrator",
      text: "Not started",
      state: "cancelled",
    },
  ];
  snapshot.events = [
    event(5, { type: "message_accepted", message_id: "queued" }, "worker"),
    event(6, { type: "message_cancelled", message_id: "queued" }, "worker"),
    event(7, { type: "agent_state", membership: "removed" }, "worker"),
  ];
  expect(agentState(snapshot, snapshot.removed_workers[0]!).items).toEqual([
    {
      kind: "message",
      sender: "orchestrator",
      text: "Not started",
      cancelled: true,
    },
  ]);
});

test("a merged message projects the same items as its streamed fragments", () => {
  const streamed = new AgentState(),
    merged = new AgentState();
  for (const text of ["Hel", "lo"])
    streamed.apply({ type: "text_delta", text }, "turn");
  streamed.apply(
    { type: "tool_call", id: "t", name: "Bash", input: {} },
    "turn",
  );
  streamed.apply(
    { type: "text_delta", text: "Nested", parent_tool_use_id: "t" },
    "turn",
  );
  merged.apply({ type: "message", text: "Hello" }, "turn");
  merged.apply({ type: "tool_call", id: "t", name: "Bash", input: {} }, "turn");
  merged.apply(
    { type: "message", text: "Nested", parent_tool_use_id: "t" },
    "turn",
  );
  expect(merged.items).toEqual(streamed.items);
});
