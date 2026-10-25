import { test, expect } from "bun:test";
import { WorkspaceClient } from "../src/client";
import { initial, agent, event } from "./fixtures";

const response = (value: unknown) => new Response(JSON.stringify(value));
async function browser(
  fetcher: (url: string, options?: RequestInit) => Promise<Response>,
) {
  const streams: Source[] = [],
    timers = new Set<() => void>();
  class Source {
    url: string;
    closed = false;
    onopen = () => {};
    onerror = () => {};
    onmessage = (_: { data: string }) => {};
    constructor(url: string) {
      this.url = url;
      streams.push(this);
    }
    close() {
      this.closed = true;
    }
    send(value: unknown) {
      this.onmessage({ data: JSON.stringify(value) });
    }
  }
  const client = new WorkspaceClient("one", () => {}, {
    fetch: fetcher,
    EventSource: Source,
    setTimeout: (fn: () => void) => {
      timers.add(fn);
      return fn;
    },
    clearTimeout: (fn: () => void) => timers.delete(fn),
  } as any);
  const settle = async () => {
    for (let i = 0; i < 40; i++) await Promise.resolve();
  };
  void client.connect();
  await settle();
  return {
    client,
    streams,
    timers,
    settle,
    retry: async () => {
      const callbacks = [...timers];
      timers.clear();
      callbacks.forEach((fn) => fn());
      await settle();
    },
  };
}

test("initial failure and dropped stream reload snapshots without duplicate output or usage", async () => {
  let snapshot = initial(),
    unavailable = true;
  const b = await browser(async () => {
    if (unavailable) throw Error("offline");
    return response(snapshot);
  });
  expect(b.streams.length).toBe(0);
  expect(b.timers.size).toBe(1);
  unavailable = false;
  await b.retry();
  expect(b.streams[0]!.url).toBe("/api/swarms/one/events?after=4");
  snapshot.events = [
    event(5, { type: "text_delta", text: "Hello" }),
    event(6, { type: "usage", input_tokens: 9 }),
  ];
  snapshot.cursor = 6;
  b.streams[0]!.send(snapshot.events[0]);
  await b.settle();
  b.streams[0]!.onerror();
  expect(b.streams[0]!.closed).toBe(true);
  await b.retry();
  const stream = b.streams.at(-1)!;
  expect(stream.url).toBe("/api/swarms/one/events?after=6");
  stream.send(snapshot.events[1]);
  stream.send({ ...snapshot.events[0], swarm_id: "two", sequence: 99 });
  await b.settle();
  expect(b.client.snapshot!.events.length).toBe(2);
  expect(b.client.snapshot!.cursor).toBe(6);
  b.client.hide();
  expect(stream.closed).toBe(true);
  expect(b.timers.size).toBe(0);
  await b.client.connect();
  expect(b.streams.length).toBe(3);
});

test("a failed transition refresh recovers queue and membership on reconnect", async () => {
  let snapshot = initial(),
    unavailable = false;
  const b = await browser(async () => {
    if (unavailable) throw Error("offline");
    return response(snapshot);
  });
  unavailable = true;
  b.streams[0]!.send(event(5, { type: "message_accepted" }));
  await b.settle();
  expect(b.timers.size).toBe(1);
  unavailable = false;
  snapshot.cursor = 9;
  snapshot.messages = [
    {
      id: "queued",
      agent_id: "orchestrator",
      sender: "user",
      state: "queued",
      text: "Saved input",
    },
  ];
  snapshot.workers = [agent("worker")];
  await b.retry();
  expect(b.client.snapshot!.messages[0]!.text).toBe("Saved input");
  expect(b.client.snapshot!.workers.length).toBe(1);
  expect(b.client.snapshot!.cursor).toBe(9);
});

test("active reset interrupts the observed turn before a settled reset starts fresh", async () => {
  const snapshot = initial(),
    requests: any[] = [];
  snapshot.turns = [
    { id: "active", agent_id: "orchestrator", state: "running" },
  ];
  const b = await browser(async (_, options) => {
    if (options) {
      const request = JSON.parse(String(options.body));
      requests.push(request);
      if (request.action === "interrupt")
        snapshot.turns[0]!.state = "interrupted";
      if (request.action === "reset")
        snapshot.orchestrator.conversation_id = "fresh";
      snapshot.cursor++;
      return response({ ok: true });
    }
    return response(snapshot);
  });
  await b.client.submit("reset");
  expect(requests[0].action).toBe("interrupt");
  expect(requests[0].turn_id).toBe("active");
  await b.client.submit("reset");
  expect(requests[1].action).toBe("reset");
  expect(b.client.snapshot!.orchestrator.conversation_id).toBe("fresh");
});

test("workspace discovery leaves the selected swarm and subscription alone", async () => {
  const b = await browser(async (url) =>
    response(
      url === "/api/swarms"
        ? [initial(), { id: "two", workspace: { name: "Second" } }]
        : initial(),
    ),
  );
  await b.client.refreshWorkspaces();
  expect(b.client.workspaces.map((w) => w.id)).toEqual(["one", "two"]);
  expect(b.client.swarm).toBe("one");
  expect(b.streams.length).toBe(1);
});

test("restoring a page discards snapshots pending before page hide", async () => {
  let resolveFirst!: (value: Response) => void,
    requests = 0;
  const b = await browser(async () =>
    ++requests === 1
      ? await new Promise((resolve) => {
          resolveFirst = resolve;
        })
      : response(initial()),
  );
  b.client.hide();
  await b.client.connect();
  const stale = initial();
  stale.cursor = 999;
  resolveFirst(response(stale));
  await b.settle();
  expect(b.streams.length).toBe(1);
  expect(b.client.snapshot!.cursor).toBe(4);
});

test("an obsolete connection failure cannot close a restored page stream", async () => {
  let rejectFirst!: (reason: Error) => void,
    requests = 0;
  const b = await browser(async () =>
    ++requests === 1
      ? await new Promise((_, reject) => {
          rejectFirst = reject;
        })
      : response(initial()),
  );
  b.client.hide();
  await b.client.connect();
  rejectFirst(Error("old request failed"));
  await b.settle();
  expect(b.streams.filter((s) => !s.closed).length).toBe(1);
  expect(b.timers.size).toBe(0);
});

test("a lost send acknowledgement retries the same request and acknowledges only success", async () => {
  const requests: any[] = [];
  let failed = true;
  const b = await browser(async (_, options) => {
    if (!options) return response(initial());
    requests.push(JSON.parse(String(options.body)));
    if (failed) throw Error("connection lost");
    return response({ ok: true });
  });
  await b.client.submit("send", "Keep this draft");
  expect(b.client.pending?.text).toBe("Keep this draft");
  expect(b.client.acknowledged).toBeUndefined();
  await b.client.submit("send", "Must not send twice");
  expect(requests.length).toBe(1);
  failed = false;
  await b.client.deliver();
  expect(requests[0]).toEqual(requests[1]);
  expect(b.client.pending).toBeUndefined();
  expect(b.client.acknowledged?.text).toBe("Keep this draft");
});

test("idle interrupt, running resume, and irrelevant retry never dispatch requests", async () => {
  const snapshot = initial();
  snapshot.scheduling = "running";
  let mutations = 0;
  const b = await browser(async (_, options) => {
    if (options) mutations++;
    return response(options ? { ok: true } : snapshot);
  });
  await b.client.submit("interrupt");
  await b.client.submit("resume");
  await b.client.submit("retry");
  expect(mutations).toBe(0);
  expect(b.client.pending).toBeUndefined();
  expect(b.client.error).toBe("");
  snapshot.scheduling = "paused";
  snapshot.cursor++;
  await b.client.refresh();
  await b.client.submit("resume");
  expect(mutations).toBe(1);
});

test("transitions refresh state without history while finished turns reload it", async () => {
  const snapshot = initial(),
    urls: string[] = [];
  snapshot.events = [event(4, { type: "text_delta", text: "Hel" })];
  const b = await browser(async (url) => {
    urls.push(url);
    return response(
      url.endsWith("/state") ? { ...snapshot, events: [] } : snapshot,
    );
  });
  b.streams[0]!.send(event(5, { type: "text_delta", text: "lo" }));
  snapshot.cursor = 7;
  snapshot.workers = [agent("worker")];
  b.streams[0]!.send(event(6, { type: "agent_created" }, "worker"));
  await b.settle();
  expect(urls.at(-1)).toBe("/api/swarms/one/state");
  expect(b.client.snapshot!.workers.length).toBe(1);
  expect(b.client.snapshot!.events.map((e) => e.sequence)).toEqual([4, 5, 6]);
  expect(b.client.snapshot!.cursor).toBe(6);
  b.streams[0]!.send(event(7, { type: "agent_state" }, "worker"));
  await b.settle();
  expect(b.client.snapshot!.cursor).toBe(7);
  snapshot.events = [
    event(4, { type: "message", text: "Hello" }),
    event(8, { type: "turn_ended", state: "completed" }),
  ];
  snapshot.cursor = 8;
  b.streams[0]!.send(snapshot.events[1]);
  await b.settle();
  expect(urls.at(-1)).toBe("/api/swarms/one");
  expect(b.client.snapshot!.events).toEqual(snapshot.events);
});

test("a failed state refresh reconnects with a full snapshot", async () => {
  const snapshot = initial();
  let unavailable = false;
  const b = await browser(async (url) => {
    if (unavailable && url.endsWith("/state")) throw Error("offline");
    return response(snapshot);
  });
  unavailable = true;
  snapshot.events = [event(5, { type: "message_accepted" })];
  snapshot.cursor = 5;
  b.streams[0]!.send(snapshot.events[0]);
  await b.settle();
  expect(b.client.error).toBe("offline");
  expect(b.timers.size).toBe(1);
  await b.retry();
  expect(b.streams.at(-1)!.url).toBe("/api/swarms/one/events?after=5");
  expect(b.client.snapshot!.events).toEqual(snapshot.events);
});
