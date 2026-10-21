import { memo, useEffect, useLayoutEffect, useReducer, useRef, useState } from "react";
import { renderMarkdown } from "./markdown.js";
import { PullRequests } from "./PullRequests";
import { AgentState, columns, dollars, setupLine, uptime, usageSummary, type Agent, type Item, type Recent, type Swarm } from "./swarm";
import "./index.css";

// The swarm's agents, a card each, kept up to date from the control plane's event stream.
export function App() {
  const [agents, setAgents] = useState<Agent[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const states = useRef(new Map<string, AgentState>());
  const [, redraw] = useReducer((n: number) => n + 1, 0);
  const [shown, setShown] = useState<string | null>(null);

  useEffect(() => {
    fetch("/agents")
      .then(response => (response.ok ? response.json() : Promise.reject(new Error(`/agents: ${response.status}`))))
      .then(setAgents, err => setError(String(err)));
  }, []);

  useEffect(() => {
    if (!agents) return;
    // Events arrive in bursts (each connection replays every one so far), so draw at
    // most once a frame.
    let frame = 0;
    const schedule = () => {
      frame ||= requestAnimationFrame(() => {
        frame = 0;
        redraw();
      });
    };
    const source = new EventSource("/events");
    // Each connection replays every event from the start, so drop what an earlier one showed.
    source.onopen = () => {
      states.current = new Map(agents.map(agent => [agent.name, new AgentState()]));
      schedule();
    };
    source.onmessage = message => {
      const { agent, event } = JSON.parse(message.data);
      states.current.get(agent)?.apply(event);
      schedule();
    };
    return () => {
      source.close();
      cancelAnimationFrame(frame);
    };
  }, [agents]);

  if (error) return <p className="p-6 text-red-400">{error}</p>;
  if (!agents) return null;
  const state = (name: string) => states.current.get(name) ?? EMPTY;
  const costs = agents.map(agent => state(agent.name).usage.cost).filter(cost => typeof cost === "number");
  const shownAgent = agents.find(agent => agent.name === shown);

  return (
    <div className="flex h-screen flex-col">
      <header className="flex items-baseline gap-3 border-b border-white/5 bg-zinc-900/60 px-6 py-3">
        <SwarmInfo />
        {costs.length > 0 && (
          <span className="ml-auto tabular-nums text-zinc-400">
            {dollars(costs.reduce((sum, cost) => sum + cost, 0))} total
          </span>
        )}
      </header>
      <div className="flex min-h-0 flex-1">
        <main className="flex min-w-0 flex-1 gap-4 p-6">
          {columns(agents).map((column, i) => (
            <div key={i} className="flex min-w-0 flex-1 flex-col gap-4">
              {column.map(agent => (
                <Card key={agent.name} agent={agent} state={state(agent.name)} onMaximize={() => setShown(agent.name)} />
              ))}
            </div>
          ))}
        </main>
        <PullRequests />
      </div>
      {shownAgent && <TranscriptDialog agent={shownAgent} state={state(shownAgent.name)} onClose={() => setShown(null)} />}
    </div>
  );
}

const EMPTY = new AgentState();

// The swarm's repository, linked to GitHub, and how long it's been up.
function SwarmInfo() {
  const [swarm, setSwarm] = useState<Swarm | null>(null);
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    fetch("/swarm")
      .then(response => (response.ok ? response.json() : null))
      .then(setSwarm, () => {});
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);
  if (!swarm) return null;
  return (
    <>
      {swarm.repo && <a
        href={`https://github.com/${swarm.repo}`}
        target="_blank"
        rel="noopener noreferrer"
        className="text-base font-semibold text-zinc-100 hover:underline"
      >
        {swarm.repo}
      </a>}
      <span className="tabular-nums text-zinc-500" title={`up since ${new Date(swarm.started_at).toLocaleString()}`}>
        up {uptime(swarm.started_at, now)}
      </span>
    </>
  );
}

const BUTTON = "rounded px-1.5 text-zinc-500 hover:bg-white/10 hover:text-zinc-100";

function Card({ agent, state, onMaximize }: { agent: Agent; state: AgentState; onMaximize: () => void }) {
  const cost = state.usage.cost;
  return (
    <section className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-xl bg-zinc-900 shadow-xl shadow-black/60 ring-1 ring-white/10">
      <div className="flex items-baseline gap-3 bg-zinc-800/70 px-4 py-2">
        <h2 className="shrink-0 font-semibold text-zinc-100">{agent.name}</h2>
        <span className="min-w-0 truncate text-xs text-zinc-400">{setupLine(agent)}</span>
        <span className="ml-auto shrink-0 text-xs tabular-nums text-zinc-400" title={usageSummary(state.usage)}>
          {typeof cost === "number" ? dollars(cost) : ""}
        </span>
        <button className={`shrink-0 ${BUTTON}`} title="Maximize" onClick={onMaximize}>
          ⤢
        </button>
      </div>
      <Preview recent={state.recent} />
    </section>
  );
}

// The end of the transcript's latest entries, as much as fits.
function Preview({ recent }: { recent: Recent[] }) {
  return (
    <div className="relative flex min-h-0 flex-1 flex-col justify-end gap-1 overflow-hidden border-t border-white/5 px-4 py-2 text-xs leading-5">
      {/* Fades out the entries cut off at the top. */}
      <div className="pointer-events-none absolute inset-x-0 top-0 z-10 h-10 bg-linear-to-b from-zinc-900 to-transparent" />
      {recent.length === 0 && <div className="mb-auto italic text-zinc-600">no activity yet</div>}
      {recent.map(({ item, nested }, i) => (
        <div key={i} className={`shrink-0 ${nested ? "pl-4" : ""}`}>
          {item.kind === "tool" ? (
            <div className={`truncate font-mono ${item.error ? "text-red-400" : "text-zinc-500"}`}>{item.summary}</div>
          ) : (
            <ItemView item={item} />
          )}
        </div>
      ))}
    </div>
  );
}

// The agent's full transcript, in a modal dialog that follows new events while it's
// scrolled to the end.
function TranscriptDialog({ agent, state, onClose }: { agent: Agent; state: AgentState; onClose: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const body = useRef<HTMLDivElement>(null);
  const atEnd = useRef(true);

  useEffect(() => {
    if (!dialog.current?.open) dialog.current?.showModal();
  }, []);
  useLayoutEffect(() => {
    if (atEnd.current && body.current) body.current.scrollTop = body.current.scrollHeight;
  });

  return (
    <dialog
      ref={dialog}
      onClose={onClose}
      onClick={event => event.target === dialog.current && dialog.current.close()}
      className="m-auto flex h-[90vh] max-h-none w-[min(64rem,95vw)] max-w-none flex-col overflow-hidden rounded-xl border border-zinc-700 bg-zinc-900 p-0 text-sm text-zinc-300 shadow-2xl backdrop:bg-black/60"
    >
      <div className="flex items-center gap-3 border-b border-zinc-800 px-5 py-3">
        <h2 className="font-semibold text-zinc-100">{agent.name}</h2>
        <span className="truncate text-xs text-zinc-500">{setupLine(agent)}</span>
        <span className="ml-auto whitespace-nowrap text-xs tabular-nums text-zinc-400">{usageSummary(state.usage)}</span>
        <button className={BUTTON} title="Close" onClick={() => dialog.current?.close()}>
          ✕
        </button>
      </div>
      <div
        ref={body}
        className="flex-1 overflow-y-auto px-5 py-4"
        onScroll={event => {
          const el = event.currentTarget;
          atEnd.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 4;
        }}
      >
        <Items items={state.items} />
      </div>
    </dialog>
  );
}

function Items({ items }: { items: Item[] }) {
  return (
    <div className="flex flex-col gap-2">
      {items.map((item, i) => (
        <ItemView key={i} item={item} />
      ))}
    </div>
  );
}

function ItemView({ item }: { item: Item }) {
  switch (item.kind) {
    case "message":
      return (
        <div className="font-bold text-zinc-100 [overflow-wrap:anywhere]">
          {item.midTurn && <div className="text-xs">mid-turn</div>}
          {item.sender && <div>from {item.sender}:</div>}
          <Markdown text={item.text} />
        </div>
      );
    case "text":
      return (
        <div className="[overflow-wrap:anywhere]">
          <Markdown text={item.text} />
        </div>
      );
    case "tool":
      return (
        <details className="font-mono text-xs text-zinc-500">
          <summary className={`cursor-pointer truncate ${item.error ? "text-red-400" : ""}`}>
            {item.summary}
            {item.output === undefined && " …"}
          </summary>
          {item.items.length > 0 && (
            <div className="mt-1 ml-4">
              <Items items={item.items} />
            </div>
          )}
          {item.output !== undefined && (
            <pre className="mt-1 ml-4 max-h-[300px] overflow-y-auto font-[inherit] whitespace-pre-wrap text-zinc-400 [overflow-wrap:anywhere]">
              {item.output}
            </pre>
          )}
        </details>
      );
    case "notice":
      return <div className="whitespace-pre-wrap text-red-400 italic [overflow-wrap:anywhere]">{item.text}</div>;
  }
}

// Text rendered as Markdown by the page's own renderer, which builds DOM nodes and never
// parses HTML. Rendering is the expensive part of a long transcript, so it's done once
// per text.
const Markdown = memo(function Markdown({ text }: { text: string }) {
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.replaceChildren();
    try {
      renderMarkdown(el, text);
    } catch {
      el.textContent = text; // show it as plain text rather than lose it
    }
  }, [text]);
  return <div ref={ref} className="md" />;
});
