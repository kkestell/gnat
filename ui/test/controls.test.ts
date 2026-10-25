import { test, expect } from "bun:test";
import { canAct } from "../src/controls";
import { initial } from "./fixtures";

test("idle agents cannot be interrupted, and only held scheduling can resume", () => {
  const snapshot = initial();
  for (const scheduling of ["running", "paused", "stopped"]) {
    snapshot.scheduling = scheduling;
    expect(canAct(snapshot, "interrupt")).toBe(false);
    expect(canAct(snapshot, "resume")).toBe(scheduling !== "running");
    expect(canAct(snapshot, "pause")).toBe(scheduling === "running");
    expect(canAct(snapshot, "stop")).toBe(scheduling !== "stopped");
  }
});

test("interrupt targets the orchestrator's active turn, including while paused", () => {
  const snapshot = initial();
  snapshot.turns = [
    { id: "worker-turn", agent_id: "worker", state: "running" },
  ];
  expect(canAct(snapshot, "interrupt")).toBe(false);
  snapshot.turns.push({
    id: "current",
    agent_id: snapshot.orchestrator.id,
    state: "running",
  });
  expect(canAct(snapshot, "interrupt")).toBe(true);
  snapshot.turns[1]!.state = "completed";
  expect(canAct(snapshot, "interrupt")).toBe(false);
});

test("harness recheck is available only for a setup error while scheduling is enabled", () => {
  const snapshot = initial();
  snapshot.scheduling = "running";
  expect(canAct(snapshot, "retry")).toBe(false);
  snapshot.orchestrator.lifecycle = "error";
  snapshot.orchestrator.error = "Harness unavailable";
  expect(canAct(snapshot, "retry")).toBe(true);
  snapshot.scheduling = "paused";
  expect(canAct(snapshot, "retry")).toBe(false);
  expect(canAct(snapshot, "resume")).toBe(true);
  snapshot.scheduling = "running";
  snapshot.orchestrator.lifecycle = "interrupted";
  expect(canAct(snapshot, "retry")).toBe(false);
});
