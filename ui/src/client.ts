import { activeTurn, canAct } from "./controls";
import type { Action, ActionRequest, Event, Snapshot, Workspace } from "./api";

type IO = Pick<
  typeof globalThis,
  "fetch" | "EventSource" | "setTimeout" | "clearTimeout"
>;
const transitions = new Set([
  "message_accepted",
  "turn_started",
  "turn_ended",
  "agent_state",
  "scheduling_changed",
  "conversation_created",
  "agent_created",
  "agent_removing",
  "message_cancelled",
]);

// Snapshot/replay and durable send acknowledgements are independent of the view.
export class WorkspaceClient {
  snapshot?: Snapshot;
  workspaces: Workspace[] = [];
  connection = "Connecting…";
  error = "";
  pending?: ActionRequest;
  busy = false;
  acknowledged?: ActionRequest;
  private source?: EventSource;
  private timer?: ReturnType<typeof setTimeout>;
  private attempt = 0;
  private hidden = false;
  private updates = Promise.resolve();
  private readonly base: string;

  constructor(
    readonly swarm: string,
    private changed: () => void,
    private io: IO = globalThis,
  ) {
    this.base = `/api/swarms/${encodeURIComponent(swarm)}`;
  }

  async refresh(attempt = this.attempt) {
    const response = await this.io.fetch(this.base);
    if (!response.ok) throw new Error(await response.text());
    const next: Snapshot = await response.json();
    if (
      this.hidden ||
      attempt !== this.attempt ||
      next.cursor < (this.snapshot?.cursor ?? 0)
    )
      return;
    this.snapshot = next;
    this.changed();
  }

  // Records other than history, merged with the events this client already applied.
  async refreshState(attempt = this.attempt) {
    const response = await this.io.fetch(`${this.base}/state`);
    if (!response.ok) throw new Error(await response.text());
    const next: Snapshot = await response.json();
    const current = this.snapshot;
    if (
      this.hidden ||
      attempt !== this.attempt ||
      !current ||
      next.cursor < current.cursor
    )
      return;
    this.snapshot = { ...next, cursor: current.cursor, events: current.events };
    this.changed();
  }

  async refreshWorkspaces() {
    try {
      const response = await this.io.fetch("/api/swarms");
      if (!response.ok) throw new Error(await response.text());
      this.workspaces = await response.json();
      this.changed();
    } catch (error) {
      this.report(error);
    }
  }

  private report(error: unknown) {
    this.error = error instanceof Error ? error.message : String(error);
    this.changed();
  }

  private reconnect() {
    this.attempt++;
    this.source?.close();
    this.io.clearTimeout(this.timer);
    this.connection = "Disconnected · reconnecting…";
    this.changed();
    if (!this.hidden)
      this.timer = this.io.setTimeout(() => void this.connect(), 1000);
  }

  async connect() {
    const attempt = ++this.attempt;
    this.hidden = false;
    this.source?.close();
    this.io.clearTimeout(this.timer);
    this.updates = Promise.resolve();
    try {
      await this.refresh(attempt);
      if (this.hidden || attempt !== this.attempt) return;
      const source = (this.source = new this.io.EventSource(
        `${this.base}/events?after=${this.snapshot!.cursor}`,
      ));
      source.onopen = () => {
        if (attempt !== this.attempt) return;
        this.connection = "Connected";
        this.changed();
      };
      source.onerror = () => {
        if (attempt === this.attempt) this.reconnect();
      };
      source.onmessage = (message) => {
        this.updates = this.updates
          .then(async () => {
            if (this.hidden || attempt !== this.attempt) return;
            const e: Event = JSON.parse(message.data);
            const snapshot = this.snapshot!;
            if (e.swarm_id !== this.swarm || e.sequence <= snapshot.cursor)
              return;
            // Finished turns merge their streamed text, so they reload history.
            if (e.payload.type === "turn_ended") return this.refresh(attempt);
            this.snapshot = {
              ...snapshot,
              cursor: e.sequence,
              events: [...snapshot.events, e],
            };
            if (transitions.has(e.payload.type))
              await this.refreshState(attempt);
            else this.changed();
          })
          .catch((error) => {
            if (this.hidden || attempt !== this.attempt) return;
            this.report(error);
            this.reconnect();
          });
      };
    } catch (error) {
      if (this.hidden || attempt !== this.attempt) return;
      this.report(error);
      this.reconnect();
    }
  }

  hide() {
    this.hidden = true;
    this.attempt++;
    this.io.clearTimeout(this.timer);
    this.source?.close();
  }

  async submit(action: Action, text = "") {
    if (this.pending || !this.snapshot || !canAct(this.snapshot, action))
      return;
    const turn = activeTurn(this.snapshot);
    if (action === "reset" && turn) action = "interrupt";
    this.pending = {
      request_id: crypto.randomUUID(),
      action,
      text,
      turn_id: action === "interrupt" ? turn?.id || "" : "",
    };
    await this.deliver();
  }

  async deliver() {
    if (!this.pending || this.busy) return;
    this.busy = true;
    this.error = "";
    this.changed();
    try {
      const response = await this.io.fetch(`${this.base}/actions`, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-Gnat-Browser": "1" },
        body: JSON.stringify(this.pending),
      });
      if (!response.ok) {
        const reason = await response.text();
        if (response.status < 500) this.pending = undefined;
        throw new Error(reason);
      }
      const result = await response.json();
      if (!result.ok) throw new Error("Request was not acknowledged");
      this.acknowledged = this.pending;
      this.pending = undefined;
      await this.refreshState();
    } catch (error) {
      this.report(error);
    } finally {
      this.busy = false;
      this.changed();
    }
  }
}
