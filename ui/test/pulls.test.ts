import { expect, test } from "bun:test";
import { ago, sorted, status, type PullRequest } from "../src/pulls";

const pull = (number: number, state: PullRequest["state"], updated_at: string, draft = false): PullRequest =>
  ({ number, title: "", url: "", state, draft, branch: "", author: "", agent: null, updated_at });

test("open pull requests come first, each most recently changed first", () => {
  const pulls = [
    pull(1, "merged", "2026-10-21T12:00:00Z"),
    pull(2, "open", "2026-10-20T12:00:00Z"),
    pull(3, "closed", "2026-10-21T13:00:00Z"),
    pull(4, "open", "2026-10-21T09:00:00Z", true),
  ];
  expect(sorted(pulls).map(pull => pull.number)).toEqual([4, 2, 3, 1]);
});

test("a draft's badge says so, and only while it's open", () => {
  expect(status(pull(1, "open", "", true))).toBe("draft");
  expect(status(pull(1, "merged", "", true))).toBe("merged");
  expect(status(pull(1, "open", ""))).toBe("open");
});

test("times are how long ago they were", () => {
  const now = Date.parse("2026-10-21T12:00:00Z");
  expect(ago("2026-10-21T11:59:30Z", now)).toBe("just now");
  expect(ago("2026-10-21T11:55:00Z", now)).toBe("5m ago");
  expect(ago("2026-10-21T09:00:00Z", now)).toBe("3h ago");
  expect(ago("2026-10-19T12:00:00Z", now)).toBe("2d ago");
  expect(ago("not a time", now)).toBe("");
});
