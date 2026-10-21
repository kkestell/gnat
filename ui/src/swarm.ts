// The swarm as the page shows it: each agent's transcript, usage, and latest entries, built
// from gnat's events (see internal/events, and the control plane's swarm_message and
// swarm_done).

// An agent's name, and its harness, model, and effort (null for the harness's default).
// An agent without a setup has only its name.
export type Agent = {
  name: string;
  harness?: string;
  model?: string | null;
  effort?: string | null;
};

// The swarm itself, as /swarm serves it: its repository, OWNER/NAME, and when it was
// brought up, as an ISO 8601 time.
export type Swarm = { repo: string; started_at: string };

// One entry in a transcript.
export type Item =
  // A message sent to the agent.
  | { kind: "message"; text: string; sender?: string; midTurn: boolean }
  // Text the agent wrote.
  | { kind: "text"; text: string }
  // A tool call, with any subagent's events inside it, and its output once it's done.
  | { kind: "tool"; summary: string; items: Item[]; output?: string; error: boolean }
  | { kind: "notice"; text: string };

// One of a transcript's latest entries, as a card shows them.
export type Recent = {
  item: Item;
  // Whether a subagent's event wrote it.
  nested: boolean;
};

export const USAGE_FIELDS = ["input_tokens", "cache_read_tokens", "cache_write_tokens", "output_tokens", "cost"] as const;
export type Usage = Partial<Record<(typeof USAGE_FIELDS)[number], number>>;

// How many of the transcript's latest entries a card keeps, enough to fill the tallest
// card; it shows as much of their end as fits.
export const RECENT_ITEMS = 40;

// The input field that best describes a call, for tools that have one.
const SUMMARY_KEYS = ["command", "file_path", "path", "pattern", "url", "query", "description"];
const MAX_WIDTH = 200;

export class AgentState {
  items: Item[] = [];
  usage: Usage = {};
  recent: Recent[] = [];
  // Each tool call, by id, so its result and any subagent's events can go in it.
  private tools = new Map<string, Item & { kind: "tool" }>();

  // Adds one event. Records in other formats, from older versions of gnat, are skipped.
  apply(event: unknown): void {
    if (typeof event !== "object" || event === null) return;
    const e = event as Record<string, any>;
    // A subagent's events go inside the tool call that started it.
    const parent = this.tools.get(e.parent)?.items ?? this.items;
    const nested = this.tools.has(e.parent);
    switch (e.type) {
      case "usage":
        for (const field of USAGE_FIELDS) {
          if (typeof e[field] === "number") this.usage[field] = (this.usage[field] ?? 0) + e[field];
        }
        break;
      case "swarm_message": {
        if (e.new) this.notice("new session");
        this.add(this.items, { kind: "message", text: String(e.text ?? ""), sender: e.sender || undefined, midTurn: Boolean(e.mid_turn) }, false);
        break;
      }
      case "message": {
        this.add(parent, { kind: "text", text: String(e.text ?? "").trim() }, nested);
        break;
      }
      case "tool_call": {
        const item = { kind: "tool" as const, summary: `${e.name}: ${summarize(e)}`, items: [], error: false };
        this.add(parent, item, nested);
        this.tools.set(e.id, item);
        break;
      }
      case "tool_result": {
        const tool = this.tools.get(e.id);
        if (!tool) break;
        tool.output = String(e.output ?? "").trim() || "(no output)";
        tool.error = Boolean(e.error);
        break;
      }
      case "turn_end":
        if (e.error) this.notice(`error: ${e.error}`);
        break;
      case "error":
        this.notice(String(e.text ?? ""));
        break;
      case "swarm_done":
        if (e.stopped) this.notice("the turn was stopped");
        else if (e.exit === null) this.notice("the agent restarted; its turn was interrupted");
        else if (e.exit) this.notice(`the turn failed with status ${e.exit}`);
        break;
    }
  }

  private notice(text: string): void {
    this.add(this.items, { kind: "notice", text }, false);
  }

  // Adds the item to the transcript (or the tool call) it belongs in, and to the latest.
  private add(parent: Item[], item: Item, nested: boolean): void {
    parent.push(item);
    this.recent.push({ item, nested });
    this.recent.splice(0, this.recent.length - RECENT_ITEMS);
  }
}

// One line describing a tool call: its most telling input, else its title.
export function summarize(event: Record<string, any>): string {
  const input = event.input && typeof event.input === "object" ? event.input : {};
  const key = SUMMARY_KEYS.find(key => typeof input[key] === "string");
  return clip(key ? input[key] : event.title ? String(event.title) : JSON.stringify(input));
}

function clip(text: string): string {
  const lines = text.split("\n");
  const first = lines[0] + (lines.length > 1 ? " …" : "");
  return first.length <= MAX_WIDTH ? first : first.slice(0, MAX_WIDTH - 1) + "…";
}

// A token count, such as 950, 12.3k, or 1.2M.
export function tokens(count: number): string {
  if (count < 1000) return String(count);
  if (count < 999950) return (count / 1000).toFixed(1).replace(/\.0$/, "") + "k";
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
  const counts = [usage.input_tokens, usage.cache_read_tokens, usage.cache_write_tokens];
  if (counts.some(count => typeof count === "number")) {
    const cached = usage.cache_read_tokens ? ` (${tokens(usage.cache_read_tokens)} cached)` : "";
    parts.push(`${tokens(counts.reduce<number>((sum, count) => sum + (count ?? 0), 0))} in${cached}`);
  }
  if (typeof usage.output_tokens === "number") parts.push(`${tokens(usage.output_tokens)} out`);
  if (typeof usage.cost === "number") parts.push(dollars(usage.cost));
  return parts.join(", ");
}

// The agent's setup in one line, such as "claude haiku (low)"; what's left out is the
// harness's default.
export function setupLine(agent: Agent): string {
  const setup = [agent.harness, agent.model].filter(Boolean).join(" ");
  return agent.effort ? `${setup} (${agent.effort})` : setup;
}

// The agents laid out in columns, in order, top to bottom: a column each for up to
// three, then a grid at least as wide as it is tall, ceil(√n) columns, with any extra
// agents in the later ones (4 is 2 and 2, 5 is 1, 2, and 2, 16 is 4 of 4).
export function columns<T>(agents: T[]): T[][] {
  const count = agents.length <= 3 ? Math.max(1, agents.length) : Math.ceil(Math.sqrt(agents.length));
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
