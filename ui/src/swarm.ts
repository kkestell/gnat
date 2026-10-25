// Transcript projection and formatting shared by every agent card.
import type { Agent, Message, Snapshot } from "./api";
export type { Agent } from "./api";
export type Item =
  | { kind: "message"; text: string; sender?: string; cancelled?: boolean }
  | { kind: "text"; text: string }
  | {
      kind: "tool";
      summary: string;
      items: Item[];
      output?: string;
      error: boolean;
    }
  | { kind: "notice"; text: string };
export type Tool = Item & { kind: "tool" };
// Consecutive calls collapse identically in every agent transcript.
export function groupItems(items: Item[]): (Item | Tool[])[] {
  const groups: (Item | Tool[])[] = [];
  for (const item of items) {
    const last = groups.at(-1);
    if (item.kind === "tool") {
      if (Array.isArray(last)) last.push(item);
      else groups.push([item]);
    } else groups.push(item);
  }
  return groups;
}

export const USAGE_FIELDS = [
  "input_tokens",
  "cache_read_tokens",
  "cache_write_tokens",
  "output_tokens",
  "cost",
] as const;
export type Usage = Partial<Record<(typeof USAGE_FIELDS)[number], number>>;
const SUMMARY_KEYS = [
  "command",
  "file_path",
  "path",
  "pattern",
  "url",
  "query",
  "description",
];
const MAX_WIDTH = 200;

export class AgentState {
  items: Item[] = [];
  usage: Usage = {};
  private tools = new Map<string, Tool>();
  private delta?: {
    item: Item & { kind: "text" };
    parent: string;
    turn: string;
  };

  apply(e: Record<string, any>, turn: string, input?: Message): void {
    const parentID = `${turn}:${e.parent_tool_use_id || ""}`;
    const parent = this.tools.get(parentID)?.items ?? this.items;
    if (e.type !== "text_delta") this.delta = undefined;
    switch (e.type) {
      case "usage":
        for (const field of USAGE_FIELDS) {
          const value =
            e[field] ??
            (field === "cache_read_tokens"
              ? e.cache_read_input_tokens
              : field === "cache_write_tokens"
                ? e.cache_creation_input_tokens
                : undefined);
          if (typeof value === "number")
            this.usage[field] = (this.usage[field] ?? 0) + value;
        }
        break;
      case "turn_started":
        if (input)
          this.items.push({
            kind: "message",
            text: input.text,
            sender: input.sender,
          });
        break;
      case "message_cancelled":
        if (input)
          this.items.push({
            kind: "message",
            text: input.text,
            sender: input.sender,
            cancelled: true,
          });
        break;
      case "text_delta": {
        if (
          !this.delta ||
          this.delta.parent !== parentID ||
          this.delta.turn !== turn
        ) {
          const item = { kind: "text" as const, text: "" };
          parent.push(item);
          this.delta = { item, parent: parentID, turn };
        }
        this.delta.item.text += e.text || "";
        break;
      }
      case "message":
        parent.push({ kind: "text", text: String(e.text ?? "") });
        break;
      case "tool_call": {
        const item: Tool = {
          kind: "tool",
          summary: `${e.name}: ${summarize(e)}`,
          items: [],
          error: false,
        };
        parent.push(item);
        this.tools.set(`${turn}:${e.id}`, item);
        break;
      }
      case "tool_result": {
        const tool = this.tools.get(`${turn}:${e.id}`);
        if (tool) {
          tool.output = String(e.output ?? "").trim() || "(no output)";
          tool.error = Boolean(e.error);
        }
        break;
      }
      case "turn_ended":
        if (e.error || e.state === "interrupted")
          this.items.push({
            kind: "notice",
            text: e.error || "The turn was interrupted.",
          });
        break;
      case "error":
        parent.push({ kind: "notice", text: String(e.text ?? "") });
        break;
    }
  }
}

export function agentState(
  snapshot: Snapshot,
  agent: Agent,
  conversation = agent.conversation_id,
): AgentState {
  const state = new AgentState();
  const messages = new Map(snapshot.messages.map((m) => [m.id, m]));
  const names = new Map(
    [
      snapshot.orchestrator,
      ...snapshot.workers,
      ...snapshot.removed_workers,
    ].map((a) => [a.id, a.name]),
  );
  for (const e of snapshot.events) {
    if (e.agent_id !== agent.id || e.conversation_id !== conversation) continue;
    const message = messages.get(e.payload.message_id);
    const input = message && {
      ...message,
      sender:
        message.sender === "user"
          ? "you"
          : (names.get(message.sender) ?? message.sender),
    };
    state.apply(e.payload, e.turn_id, input);
  }
  return state;
}

// One line describing a tool call: its most telling input, else its title.
export function summarize(event: Record<string, any>): string {
  const input =
    event.input && typeof event.input === "object" ? event.input : {};
  const key = SUMMARY_KEYS.find((key) => typeof input[key] === "string");
  return clip(
    key
      ? input[key]
      : event.title
        ? String(event.title)
        : JSON.stringify(input),
  );
}

function clip(text: string): string {
  const lines = text.split("\n");
  const first = lines[0] + (lines.length > 1 ? " …" : "");
  return first.length <= MAX_WIDTH
    ? first
    : first.slice(0, MAX_WIDTH - 1) + "…";
}

// A token count, such as 950, 12.3k, or 1.2M.
export function tokens(count: number): string {
  if (count < 1000) return String(count);
  if (count < 999950)
    return (count / 1000).toFixed(1).replace(/\.0$/, "") + "k";
  return (count / 1000000).toFixed(1).replace(/\.0$/, "") + "M";
}

// A cost in US dollars, to the cent, such as $0.00, $0.04, or $1.23.
export function dollars(cost: number): string {
  return "$" + cost.toFixed(2);
}

// One line of usage, such as "1.2M in (1.1M cached), 34k out, $1.23"; the fields the
// harness reported, empty if none.
export function usageSummary(usage: Usage): string {
  const parts = [];
  const counts = [
    usage.input_tokens,
    usage.cache_read_tokens,
    usage.cache_write_tokens,
  ];
  if (counts.some((count) => typeof count === "number")) {
    const cached = usage.cache_read_tokens
      ? ` (${tokens(usage.cache_read_tokens)} cached)`
      : "";
    parts.push(
      `${tokens(counts.reduce<number>((sum, count) => sum + (count ?? 0), 0))} in${cached}`,
    );
  }
  if (typeof usage.output_tokens === "number")
    parts.push(`${tokens(usage.output_tokens)} out`);
  if (typeof usage.cost === "number") parts.push(dollars(usage.cost));
  return parts.join(", ");
}

// What an agent is doing: its membership while it is not an active team member,
// otherwise its execution lifecycle.
export function agentStatus(agent: Agent): string {
  return agent.membership === "active"
    ? agent.lifecycle
    : agent.membership.replaceAll("_", " ");
}

// The agent's setup in one line, such as "claude haiku (low)"; what's left out is the
// harness's default.
export function setupLine(agent: Agent): string {
  const setup = [agent.settings.harness, agent.settings.model]
    .filter(Boolean)
    .join(" ");
  return agent.settings.effort ? `${setup} (${agent.settings.effort})` : setup;
}

// The agents laid out in columns, in order, top to bottom: a column each for up to
// three, then a grid at least as wide as it is tall, ceil(√n) columns, with any extra
// agents in the later ones (4 is 2 and 2, 5 is 1, 2, and 2, 16 is 4 of 4).
export function columns<T>(agents: T[]): T[][] {
  const count =
    agents.length <= 3
      ? Math.max(1, agents.length)
      : Math.ceil(Math.sqrt(agents.length));
  const base = Math.floor(agents.length / count);
  const extra = agents.length % count;
  const columns: T[][] = [];
  let next = 0;
  for (let i = 0; i < count; i++) {
    const size = base + (i >= count - extra ? 1 : 0);
    columns.push(agents.slice(next, next + size));
    next += size;
  }
  return columns;
}

// How long the swarm has been up, such as "45s", "12m", "2h 14m", or "3d 4h".
export function uptime(startedAt: string, now: number): string {
  const seconds = Math.floor((now - Date.parse(startedAt)) / 1000);
  if (!Number.isFinite(seconds)) return "";
  if (seconds < 60) return `${Math.max(0, seconds)}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ${minutes % 60}m`;
  return `${Math.floor(hours / 24)}d ${hours % 24}h`;
}
