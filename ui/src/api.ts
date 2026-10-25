export type Settings = { harness: string; model: string; effort: string };
export type Agent = {
  id: string;
  role: string;
  name: string;
  settings: Settings;
  lifecycle: string;
  working_dir: string;
  branch: string | null;
  conversation_id: string;
  error: string;
  membership: string;
  stage: string;
};
export type Message = {
  id: string;
  agent_id: string;
  sender: string;
  text: string;
  state: string;
};
export type Conversation = { id: string; agent_id: string; created_at: string };
export type Turn = { id: string; agent_id: string; state: string };
export type Event = {
  sequence: number;
  swarm_id: string;
  agent_id: string;
  conversation_id: string;
  turn_id: string;
  payload: Record<string, any>;
};
export type Workspace = {
  id: string;
  workspace: { name: string; root: string; common_dir: string };
};
export type Snapshot = Workspace & {
  cursor: number;
  scheduling: string;
  created_at: string;
  orchestrator: Agent;
  workers: Agent[];
  removed_workers: Agent[];
  messages: Message[];
  conversations: Conversation[];
  turns: Turn[];
  events: Event[];
};
export type Action =
  "send" | "pause" | "resume" | "stop" | "interrupt" | "retry" | "reset";
export type ActionRequest = {
  request_id: string;
  action: Action;
  text: string;
  turn_id: string;
};
