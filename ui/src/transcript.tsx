import { memo, useLayoutEffect, useRef } from "react";
import { renderMarkdown } from "./markdown.js";
import { groupItems, type Item, type Tool } from "./swarm";

export function TranscriptFade() {
  return <div className="pointer-events-none absolute inset-x-0 top-0 z-10 h-10 bg-linear-to-b from-card to-transparent" />;
}

function Spinner() {
  return (
    <svg
      viewBox="0 0 16 16"
      className="mt-px mr-1.5 inline-block size-3.5 animate-spin align-top"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
    >
      <circle cx="8" cy="8" r="6" className="opacity-25" />
      <path d="M14 8a6 6 0 0 0-6-6" strokeLinecap="round" />
    </svg>
  );
}

// Tool calls made one after another: the latest call while they're the latest thing the
// agent did, then how many there were.
export function Tools({ tools, latest }: { tools: Tool[]; latest: boolean }) {
  if (latest) {
    const tool = tools.at(-1)!;
    return (
      <span className="text-zinc-500">
        {tool.output === undefined && <Spinner />}
        <span
          className={`font-mono ${tool.error ? "text-red-400" : "text-zinc-600"}`}
        >
          {tool.summary}
        </span>
      </span>
    );
  }
  const failed = tools.filter((tool) => tool.error).length;
  return (
    <span className="truncate font-mono text-zinc-500">
      Called {tools.length} {tools.length === 1 ? "tool" : "tools"}
      {failed > 0 && <span className="text-red-400"> ({failed} failed)</span>}
    </span>
  );
}

// A compact transcript shows each run of tool calls as one line; otherwise the line
// expands into the calls and their output.
export function Items({
  items,
  compact = false,
}: {
  items: Item[];
  compact?: boolean;
}) {
  const groups = groupItems(items);
  return (
    <div className="flex flex-col gap-3">
      {groups.map((group, i) =>
        Array.isArray(group) && compact ? (
          <div key={i} className="truncate font-mono text-xs text-zinc-500">
            <Tools tools={group} latest={i === groups.length - 1} />
          </div>
        ) : Array.isArray(group) ? (
          <details
            key={i}
            className="tool-group font-mono text-xs text-zinc-500"
          >
            <summary className="cursor-pointer truncate">
              <Tools tools={group} latest={i === groups.length - 1} />
            </summary>
            <div className="mt-2 ml-4 flex flex-col gap-2">
              {group.map((tool, j) => (
                <ItemView key={j} item={tool} />
              ))}
            </div>
          </details>
        ) : (
          <ItemView key={i} item={group} />
        ),
      )}
    </div>
  );
}

export function ItemView({ item }: { item: Item }) {
  switch (item.kind) {
    case "message":
      return (
        <div className="my-1 w-fit max-w-[85%] rounded-lg rounded-tl-sm bg-zinc-800 px-3 py-1.5 text-zinc-200 [overflow-wrap:anywhere]">
          <div className="text-xs text-zinc-400">
            From {item.sender ?? "operator"}
            {item.cancelled && " (cancelled before delivery)"}:
          </div>
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
          <summary
            className={`cursor-pointer truncate ${item.error ? "text-red-400" : ""}`}
          >
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
      return (
        <div className="whitespace-pre-wrap text-red-400 italic [overflow-wrap:anywhere]">
          {item.text}
        </div>
      );
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
