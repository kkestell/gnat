import type { Snapshot, Agent, Event } from "../src/api";
export const agent = (id = "orchestrator"): Agent => ({
  id,
  role: id === "orchestrator" ? "orchestrator" : "worker",
  name: id,
  settings: { harness: "claude", model: "claude-opus-5-5", effort: "high" },
  lifecycle: "ready",
  working_dir: `/repo/${id}`,
  branch: null,
  conversation_id: `conversation-${id}`,
  error: "",
  membership: "active",
  stage: "",
});
export const initial = (): Snapshot => ({
  id: "one",
  cursor: 4,
  scheduling: "paused",
  created_at: "2026-10-24T20:00:00Z",
  workspace: { name: "First", root: "/repo", common_dir: "/repo/.git" },
  orchestrator: agent(),
  messages: [],
  turns: [],
  events: [],
  workers: [],
  removed_workers: [],
  conversations: [
    {
      id: "conversation-orchestrator",
      agent_id: "orchestrator",
      created_at: "2026-10-24T20:00:00Z",
    },
  ],
});
export const event = (
  sequence: number,
  payload: Record<string, any>,
  owner = "orchestrator",
): Event => ({
  sequence,
  payload,
  swarm_id: "one",
  agent_id: owner,
  conversation_id: `conversation-${owner}`,
  turn_id: "turn",
});
