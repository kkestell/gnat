import { useEffect, useState } from "react";
import { ago, sorted, status, type PullRequest } from "./pulls";

// How often the sidebar asks for the pull requests again, in milliseconds.
const REFRESH = 30_000;

const BADGES: Record<ReturnType<typeof status>, string> = {
  open: "bg-emerald-950 text-emerald-300",
  draft: "bg-zinc-800 text-zinc-400",
  merged: "bg-violet-950 text-violet-300",
  closed: "bg-red-950 text-red-300",
};

// The repository's pull requests, newest open ones first, refreshed every so often.
export function PullRequests() {
  const [pulls, setPulls] = useState<PullRequest[] | null>(null);
  const [error, setError] = useState(false);
  const [now, setNow] = useState(Date.now());

  useEffect(() => {
    let live = true;
    const load = () =>
      fetch("/pulls")
        .then(response => (response.ok ? response.json() : Promise.reject(new Error(String(response.status)))))
        .then(
          pulls => live && (setPulls(pulls), setError(false), setNow(Date.now())),
          () => live && setError(true),
        );
    load();
    const timer = setInterval(load, REFRESH);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, []);

  const open = pulls?.filter(pull => pull.state === "open").length ?? 0;
  return (
    <aside className="flex w-80 shrink-0 flex-col border-l border-white/5 bg-zinc-900/60">
      <div className="flex items-baseline gap-2 px-4 py-3">
        <h2 className="font-semibold text-zinc-100">Pull requests</h2>
        {pulls && <span className="text-xs text-zinc-500">{open} open</span>}
      </div>
      <ul className="min-h-0 flex-1 overflow-y-auto">
        {error && !pulls && <li className="px-4 text-xs italic text-zinc-600">couldn't load pull requests</li>}
        {pulls?.length === 0 && <li className="px-4 text-xs italic text-zinc-600">none yet</li>}
        {pulls &&
          sorted(pulls).map(pull => (
            <li key={pull.number} className="border-t border-white/5 first:border-t-0">
              <Pull pull={pull} now={now} />
            </li>
          ))}
      </ul>
    </aside>
  );
}

function Pull({ pull, now }: { pull: PullRequest; now: number }) {
  const badge = status(pull);
  return (
    <a
      href={pull.url.startsWith("https://") ? pull.url : undefined}
      target="_blank"
      rel="noopener noreferrer"
      className={`block px-4 py-2.5 hover:bg-white/5 ${pull.state === "open" ? "" : "opacity-60"}`}
    >
      <div className="flex gap-1.5 text-sm leading-snug">
        <span className="shrink-0 tabular-nums text-zinc-500">#{pull.number}</span>
        <span className="line-clamp-2 text-zinc-200">{pull.title}</span>
      </div>
      <div className="mt-1.5 flex items-center gap-2 text-xs">
        <span className={`shrink-0 rounded px-1.5 py-px font-medium ${BADGES[badge]}`}>{badge}</span>
        {pull.agent ? (
          <span className="shrink-0 font-medium text-zinc-300">{pull.agent}</span>
        ) : (
          <span className="shrink-0 italic text-zinc-600" title="Which agent opened it isn't known yet">
            no agent
          </span>
        )}
        <span className="ml-auto shrink-0 text-zinc-500">{ago(pull.updated_at, now)}</span>
      </div>
      <div className="mt-1 truncate font-mono text-xs text-zinc-500">{pull.branch}</div>
    </a>
  );
}
