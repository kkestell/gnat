// A development server: the UI with hot reloading, either against a pretend swarm
// (mock/swarm.ts) or passing its data requests to a running swarm's web UI.
//
//   bun dev                                         # the pretend swarm; open /?agents=N for N agents
//   GNAT_URL=$(gnat web SWARM --no-open) bun dev    # a real one
import { serve } from "bun";
import index from "./index.html";
import * as mock from "../mock/swarm";
import { pulls } from "../mock/pulls";

const upstream = process.env.GNAT_URL;

const proxy = (request: Request) => fetch(new URL(new URL(request.url).pathname, upstream), { signal: request.signal });

// How many agents the pretend swarm has: the page's ?agents=N, else 4.
function mockCount(request: Request): number {
  const page = request.headers.get("referer");
  return Number((page && new URL(page).searchParams.get("agents")) || 4);
}

// The pretend swarm's events, as the control plane streams them.
function mockEvents(request: Request): Response {
  const encoder = new TextEncoder();
  let unwatch = () => {};
  const body = new ReadableStream({
    start(controller) {
      unwatch = mock.watch(mockCount(request), record => controller.enqueue(encoder.encode(`data: ${JSON.stringify(record)}\n\n`)));
      request.signal.addEventListener("abort", () => {
        unwatch();
        controller.close();
      });
    },
    cancel: () => unwatch(),
  });
  return new Response(body, { headers: { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" } });
}

const server = serve({
  port: Number(process.env.PORT ?? 5180),
  routes: {
    "/agents": upstream ? proxy : request => Response.json(mock.agents(mockCount(request))),
    "/events": upstream ? proxy : mockEvents,
    "/pulls": upstream ? proxy : () => Response.json(pulls()),
    "/swarm": upstream ? proxy : () => Response.json(mock.swarm),
    "/*": index,
  },
  // The event stream stays open.
  idleTimeout: 0,
  development: { hmr: true, console: true },
});

console.log(`gnat UI at ${server.url}, for ${upstream ?? "a pretend swarm"}`);
