import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
} from "react";
import { Items, TranscriptFade } from "./transcript";
import { WorkspaceClient } from "./client";
import { canAct } from "./controls";
import type { Action, Agent, Snapshot } from "./api";
import {
  AgentState,
  agentState,
  agentStatus,
  columns,
  dollars,
  setupLine,
  usageSummary,
} from "./swarm";
import "./index.css";

export function App() {
  const [, redraw] = useReducer((n: number) => n + 1, 0);
  const [client] = useState(
    () => new WorkspaceClient(document.body.dataset.swarm!, redraw),
  );
  const [shown, setShown] = useState<string | null>(null);
  const [width, setWidth] = useState(() => {
    try {
      const n = Number(localStorage.getItem("gnat:chat-width"));
      return n >= 320 && n <= 1000 ? n : 460;
    } catch {
      return 460;
    }
  });
  useEffect(() => {
    void client.connect();
    void client.refreshWorkspaces();
    const focus = () => void client.refreshWorkspaces();
    const hide = () => client.hide();
    const show = (event: PageTransitionEvent) => {
      if (event.persisted) void client.connect();
    };
    window.addEventListener("focus", focus);
    window.addEventListener("pagehide", hide);
    window.addEventListener("pageshow", show);
    return () => {
      client.hide();
      window.removeEventListener("focus", focus);
      window.removeEventListener("pagehide", hide);
      window.removeEventListener("pageshow", show);
    };
  }, [client]);
  const snapshot = client.snapshot;
  const states = useMemo(
    () =>
      new Map(snapshot?.workers.map((a) => [a.id, agentState(snapshot, a)])),
    [snapshot],
  );
  if (!snapshot)
    return (
      <div className="flex h-dvh flex-col bg-canvas">
        <header className="border-b border-white/10 bg-header px-4 py-3 text-zinc-400">
          Gnat{" "}
          <span className="ml-3 text-xs text-zinc-500">
            {client.connection}
          </span>
        </header>
        {client.error && (
          <p className="p-4 text-xs text-red-400">{client.error}</p>
        )}
      </div>
    );
  const all = [...snapshot.workers, ...snapshot.removed_workers];
  const selected = all.find((a) => a.id === shown);
  const costs = snapshot.events
    .filter(
      (e) => e.payload.type === "usage" && typeof e.payload.cost === "number",
    )
    .map((e) => e.payload.cost as number);
  const resize = (value: number) => {
    const next = Math.round(
      Math.max(320, Math.min(1000, window.innerWidth * 0.7, value)),
    );
    setWidth(next);
    try {
      localStorage.setItem("gnat:chat-width", String(next));
    } catch {
      /* storage may be unavailable */
    }
  };
  return (
    <div className="app-shell flex h-dvh flex-col overflow-hidden">
      <header className="flex shrink-0 items-center gap-3 border-b border-white/10 bg-header px-4 py-3">
        <WorkspaceSwitcher client={client} snapshot={snapshot} />
        <span
          className="hidden min-w-0 truncate text-xs text-zinc-500 sm:block"
          title={snapshot.workspace.root}
        >
          {snapshot.workspace.root}
        </span>
        {snapshot.removed_workers.length > 0 && (
          <select
            className="shrink-0 rounded bg-card px-2 py-1 text-xs text-zinc-400"
            aria-label="Removed workers"
            value=""
            onChange={(e) => setShown(e.target.value)}
          >
            <option value="">Removed workers</option>
            {snapshot.removed_workers.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </select>
        )}
        <div className="ml-auto flex shrink-0 items-baseline gap-6 text-xs">
          {snapshot.scheduling !== "running" && (
            <span className="flex items-baseline gap-2 text-amber-400">
              {snapshot.scheduling === "stopped" ? "Stopped" : "Paused"}
              <button
                className="rounded bg-zinc-700/70 px-2 py-0.5 text-zinc-200 hover:bg-zinc-600 disabled:opacity-35"
                disabled={!!client.pending}
                onClick={() => void client.submit("resume")}
              >
                Resume
              </button>
            </span>
          )}
          {costs.length > 0 && (
            <span className="tabular-nums text-zinc-400">
              {dollars(costs.reduce((a, b) => a + b, 0))} total
            </span>
          )}
          {client.connection !== "Connected" && (
            <span className="text-zinc-500">{client.connection}</span>
          )}
        </div>
      </header>
      {document.body.dataset.locationError && (
        <p className="shrink-0 border-b border-white/10 bg-header px-4 py-2 text-xs text-red-400">
          {document.body.dataset.locationError}
        </p>
      )}
      <div className="workspace-layout flex min-h-0 flex-1 gap-4 p-4">
        <main className="worker-area flex min-h-0 min-w-0 flex-1 gap-4">
          {selected ? (
            <WorkerDetail
              key={selected.id}
              agent={selected}
              snapshot={snapshot}
              onClose={() => setShown(null)}
            />
          ) : snapshot.workers.length ? (
            columns(snapshot.workers).map((column, i) => (
              <div
                key={i}
                className="flex min-h-0 min-w-0 flex-1 flex-col gap-4"
              >
                {column.map((a) => (
                  <Card
                    key={a.id}
                    agent={a}
                    state={states.get(a.id)!}
                    onMaximize={() => setShown(a.id)}
                  />
                ))}
              </div>
            ))
          ) : (
            <p className="m-auto text-center text-zinc-500">
              Your workspace is ready.
              <br />
              Direct the orchestrator in the chat sidebar.
            </p>
          )}
        </main>
        <aside
          className="orchestrator relative flex min-h-0 shrink-0"
          style={{ width }}
        >
          <div
            className="resize-handle"
            onPointerDown={(e) => {
              e.preventDefault();
              e.currentTarget.setPointerCapture(e.pointerId);
            }}
            onPointerMove={(e) => {
              if (e.currentTarget.hasPointerCapture(e.pointerId))
                resize(window.innerWidth - e.clientX - 16);
            }}
          />
          <Orchestrator client={client} snapshot={snapshot} />
        </aside>
      </div>
    </div>
  );
}

function WorkspaceSwitcher({
  client,
  snapshot,
}: {
  client: WorkspaceClient;
  snapshot: Snapshot;
}) {
  const workspaces = client.workspaces.length ? client.workspaces : [snapshot];
  return (
    <label className="shrink-0 text-base font-semibold text-zinc-100">
      <select
        className="max-w-48 cursor-pointer appearance-none border-0 bg-transparent pr-1 outline-none"
        value={snapshot.id}
        onChange={(e) => {
          window.location.href = `/swarms/${encodeURIComponent(e.target.value)}`;
        }}
      >
        {workspaces.map((w) => (
          <option key={w.id} value={w.id}>
            {w.workspace.name}
          </option>
        ))}
      </select>
    </label>
  );
}

function History({
  snapshot,
  agent,
  value,
  onChange,
}: {
  snapshot: Snapshot;
  agent: Agent;
  value: string;
  onChange: (id: string) => void;
}) {
  const conversations = snapshot.conversations.filter(
    (c) => c.agent_id === agent.id,
  );
  if (conversations.length < 2) return null;
  return (
    <label className="flex items-center gap-2 text-xs text-zinc-500">
      Conversation
      <select
        className="min-w-0 max-w-full flex-1 rounded border border-white/10 bg-card px-2 py-1 text-zinc-400"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      >
        {conversations.map((c, i) => (
          <option key={c.id} value={c.id}>
            {i + 1} · {new Date(c.created_at).toLocaleString()}
            {c.id === agent.conversation_id ? " (current)" : ""}
          </option>
        ))}
      </select>
    </label>
  );
}

function Transcript({
  state,
  empty,
}: {
  state: AgentState;
  empty?: React.ReactNode;
}) {
  const body = useRef<HTMLDivElement>(null);
  const atEnd = useRef(true);
  useLayoutEffect(() => {
    if (atEnd.current && body.current)
      body.current.scrollTop = body.current.scrollHeight;
  });
  return (
    <div className="relative flex min-h-0 min-w-0 flex-1">
      <div
        ref={body}
        className="min-h-0 min-w-0 flex-1 overflow-y-auto px-5 py-4"
        onScroll={(event) => {
          const el = event.currentTarget;
          atEnd.current =
            el.scrollTop + el.clientHeight >= el.scrollHeight - 40;
        }}
      >
        {state.items.length ? <Items items={state.items} /> : empty}
      </div>
      <TranscriptFade />
    </div>
  );
}

function WorkerDetail({
  agent,
  snapshot,
  onClose,
}: {
  agent: Agent;
  snapshot: Snapshot;
  onClose: () => void;
}) {
  const [history, setHistory] = useState<string | null>(null);
  const conversation = history ?? agent.conversation_id;
  const state = useMemo(
    () => agentState(snapshot, agent, conversation),
    [snapshot, agent, conversation],
  );
  const queued = snapshot.messages.filter(
    (m) => m.agent_id === agent.id && m.state === "queued",
  );
  return (
    <AgentCard
      agent={agent}
      state={state}
      actions={
        <button className={BUTTON} title="Close" onClick={onClose}>
          ✕
        </button>
      }
    >
      <div className="border-t border-white/5 px-5 py-2 text-xs text-zinc-500">
        <p className="truncate" title={agent.working_dir}>
          {agent.working_dir}
        </p>
        {agent.branch && (
          <p className="mt-1 truncate" title={agent.branch}>
            {agent.branch}
          </p>
        )}
        <History
          snapshot={snapshot}
          agent={agent}
          value={conversation}
          onChange={(id) =>
            setHistory(id === agent.conversation_id ? null : id)
          }
        />
      </div>
      <Transcript
        key={conversation}
        state={state}
        empty={<p className="italic text-zinc-600">no activity yet</p>}
      />
      {queued.length > 0 && (
        <p className="border-t border-white/5 px-5 py-2 text-xs text-zinc-500">
          {queued.length} queued: {queued.map((m) => m.text).join(" · ")}
        </p>
      )}
    </AgentCard>
  );
}

function Orchestrator({
  client,
  snapshot,
}: {
  client: WorkspaceClient;
  snapshot: Snapshot;
}) {
  const agent = snapshot.orchestrator;
  const [history, setHistory] = useState<string | null>(null);
  const [prompt, setPrompt] = useState("");
  const conversation = history ?? agent.conversation_id;
  const state = useMemo(
    () => agentState(snapshot, agent, conversation),
    [snapshot, agent, conversation],
  );
  const queued = snapshot.messages.filter(
    (m) => m.agent_id === agent.id && m.state === "queued",
  );
  const running = snapshot.turns.some(
    (t) => t.agent_id === agent.id && t.state === "running",
  );
  useEffect(() => {
    if (client.acknowledged?.action === "send") {
      const text = client.acknowledged.text;
      setPrompt((current) => (current === text ? "" : current));
    }
  }, [client.acknowledged]);
  const action = (kind: Action) => void client.submit(kind);
  return (
    <AgentCard
      agent={agent}
      state={state}
      errorAction={
        canAct(snapshot, "retry") && (
          <button
            className={`ml-2 ${BUTTON}`}
            onClick={() => action("retry")}
            disabled={!!client.pending}
          >
            Recheck harness
          </button>
        )
      }
      actions={
        <details className="action-menu relative shrink-0">
          <summary
            className={`cursor-pointer list-none ${BUTTON}`}
            title="Orchestrator controls"
          >
            •••
          </summary>
          <div className="absolute right-0 top-7 z-30 w-56 rounded-lg border border-white/10 bg-header p-1 text-xs shadow-xl">
            {(["pause", "stop", "interrupt", "reset"] as Action[])
              .filter((kind) => canAct(snapshot, kind))
              .map((kind) => (
                <button
                  key={kind}
                  disabled={!!client.pending}
                  className="block w-full rounded px-3 py-2 text-left text-zinc-400 hover:bg-white/10 hover:text-zinc-100 disabled:opacity-40"
                  onClick={(e) => {
                    e.currentTarget.closest("details")?.removeAttribute("open");
                    action(kind);
                  }}
                >
                  {kind === "reset"
                    ? running
                      ? "Interrupt before new conversation"
                      : "New conversation"
                    : kind[0]!.toUpperCase() + kind.slice(1)}
                </button>
              ))}
          </div>
        </details>
      }
    >
      {snapshot.conversations.filter((c) => c.agent_id === agent.id).length >
        1 && (
        <div className="shrink-0 border-t border-white/5 px-4 py-2">
          <History
            snapshot={snapshot}
            agent={agent}
            value={conversation}
            onChange={(id) =>
              setHistory(id === agent.conversation_id ? null : id)
            }
          />
        </div>
      )}
      <Transcript key={conversation} state={state} />
      <div className="shrink-0 border-t border-white/5 p-3">
        {queued.length > 0 && (
          <div className="mb-2 max-h-24 overflow-y-auto px-1 text-xs text-zinc-500">
            {queued.length} queued
            {queued.map((m) => (
              <p key={m.id} className="mt-1 truncate" title={m.text}>
                {m.text}
              </p>
            ))}
          </div>
        )}
        <form
          className="rounded-lg border border-white/10 bg-zinc-900/70 focus-within:border-zinc-600"
          onSubmit={(e) => {
            e.preventDefault();
            if (!prompt.trim() || client.pending) return;
            void client.submit("send", prompt);
          }}
        >
          <textarea
            id="prompt"
            rows={3}
            value={prompt}
            className="block max-h-48 min-h-20 w-full resize-y bg-transparent px-3 pt-3 text-sm leading-6 text-zinc-200 outline-none placeholder:text-zinc-600"
            onChange={(e) => setPrompt(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                e.preventDefault();
                e.currentTarget.form?.requestSubmit();
              }
            }}
          />
          <div className="flex items-center justify-end px-2 pb-2 pt-1">
            <button
              type="submit"
              disabled={!prompt.trim() || !!client.pending}
              className="rounded bg-zinc-700/70 px-3 py-1 text-xs text-zinc-200 hover:bg-zinc-600 disabled:cursor-default disabled:opacity-35"
            >
              Send
            </button>
          </div>
        </form>
        {client.error && (
          <p className="mt-2 px-1 text-xs text-red-400">{client.error}</p>
        )}
        {client.pending && !client.busy && (
          <button
            className="mt-2 px-1 text-xs text-zinc-400 underline"
            onClick={() => void client.deliver()}
          >
            Retry unacknowledged request
          </button>
        )}
      </div>
    </AgentCard>
  );
}

const BUTTON =
  "rounded px-1.5 text-zinc-500 hover:bg-white/10 hover:text-zinc-100";

function AgentCard({
  agent,
  state,
  actions,
  errorAction,
  children,
}: {
  agent: Agent;
  state: AgentState;
  actions?: React.ReactNode;
  errorAction?: React.ReactNode;
  children: React.ReactNode;
}) {
  const cost = state.usage.cost;
  return (
    <section className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-xl bg-card shadow-xl shadow-black/60 ring-1 ring-white/10">
      <div className="flex shrink-0 items-baseline gap-3 bg-zinc-800/70 px-4 py-2">
        <h2 className="shrink-0 font-semibold text-zinc-100">{agent.name}</h2>
        <span
          className="min-w-0 truncate text-xs text-zinc-400"
          title={setupLine(agent)}
        >
          {setupLine(agent)}
        </span>
        <span
          className="ml-auto shrink-0 text-xs text-zinc-500"
          title={agent.working_dir}
        >
          {agentStatus(agent)}
        </span>
        <span
          className="shrink-0 text-xs tabular-nums text-zinc-400"
          title={usageSummary(state.usage)}
        >
          {typeof cost === "number" ? dollars(cost) : ""}
        </span>
        {actions}
      </div>
      {agent.error && (
        <p className="shrink-0 border-t border-white/5 px-4 py-2 text-xs text-red-400 [overflow-wrap:anywhere]">
          {agent.error}
          {errorAction}
        </p>
      )}
      {children}
    </section>
  );
}

function Card({
  agent,
  state,
  onMaximize,
}: {
  agent: Agent;
  state: AgentState;
  onMaximize: () => void;
}) {
  return (
    <AgentCard
      agent={agent}
      state={state}
      actions={
        <button
          className={`shrink-0 ${BUTTON}`}
          title="Maximize"
          onClick={onMaximize}
        >
          ⤢
        </button>
      }
    >
      <Transcript state={state} />
    </AgentCard>
  );
}
