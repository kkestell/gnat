// The repository's pull requests, as /pulls serves them.

export type PullRequest = {
  number: number;
  title: string;
  url: string;
  state: "open" | "merged" | "closed";
  draft: boolean;
  branch: string;
  // The GitHub account that opened it.
  author: string;
  // The swarm's agent that opened it, when known.
  agent: string | null;
  // When it last changed, as an ISO 8601 time.
  updated_at: string;
};

// The status a pull request's badge shows.
export function status(pull: PullRequest): "open" | "draft" | "merged" | "closed" {
  return pull.state === "open" && pull.draft ? "draft" : pull.state;
}

// How long before now a time was, such as "just now", "5m ago", "3h ago", or "2d ago".
export function ago(time: string, now: number): string {
  const seconds = Math.max(0, (now - Date.parse(time)) / 1000);
  if (!Number.isFinite(seconds)) return "";
  if (seconds < 60) return "just now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

// Open pull requests first, then the rest, each most recently changed first.
export function sorted(pulls: PullRequest[]): PullRequest[] {
  const open = (pull: PullRequest) => (pull.state === "open" ? 0 : 1);
  return [...pulls].sort((a, b) => open(a) - open(b) || Date.parse(b.updated_at) - Date.parse(a.updated_at));
}
