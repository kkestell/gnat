import { test, expect } from 'bun:test';
import { readFileSync } from 'node:fs';
import { createContext, runInContext } from 'node:vm';

const script = readFileSync(new URL('./page.js', import.meta.url), 'utf8');
class Element {
  constructor() { this.children = []; this.listeners = {}; this.textContent = ''; this.value = ''; }
  append(...items) { this.children.push(...items); }
  replaceChildren(...items) { this.children = items; }
  addEventListener(name, fn) { this.listeners[name] = fn; }
  setAttribute(name, value) { this[name] = value; }
}
const initial = () => ({
  id: 'one', cursor: 4, scheduling: 'paused',
  workspace: { name: 'First' },
  orchestrator: { id: 'orchestrator', conversation_id: 'conversation', lifecycle: 'ready' },
  messages: [], turns: [], events: [], workers: [],
  conversations: [{ id: 'conversation', agent_id: 'orchestrator', created_at: '2026-10-24' }],
});
async function browser(fetcher) {
  const elements = new Map();
  const element = id => { if (!elements.has(id)) elements.set(id, new Element()); return elements.get(id); };
  const timers = new Set(), streams = [], listeners = {};
  const context = createContext({
    document: {
      body: { dataset: { swarm: 'one' } }, getElementById: element,
      createElement: () => new Element(), querySelector: element, querySelectorAll: () => [],
    },
    window: { addEventListener: (name, fn) => { listeners[name] = fn; } },
    Option: class extends Element {}, crypto: { randomUUID: () => 'request' },
    fetch: fetcher,
    EventSource: class {
      constructor(url) { this.url = url; streams.push(this); }
      close() { this.closed = true; }
    },
    setTimeout: fn => { timers.add(fn); return fn; }, clearTimeout: fn => timers.delete(fn),
  });
  runInContext(script, context);
  const settle = async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); };
  await settle();
  return {
    context, elements, streams, timers, listeners, settle,
    value: expression => runInContext(expression, context),
    retry: async () => { const callbacks = [...timers]; timers.clear(); for (const fn of callbacks) await fn(); await settle(); },
  };
}
const response = value => ({ ok: true, json: async () => structuredClone(value) });

test('initial failure and dropped stream reload snapshots without duplicate output or usage', async () => {
  let snapshot = initial(), unavailable = true;
  const b = await browser(async () => { if (unavailable) throw Error('offline'); return response(snapshot); });
  expect(b.streams.length).toBe(0);
  expect(b.timers.size).toBe(1);
  unavailable = false;
  await b.retry();
  expect(b.streams[0].url).toBe('/api/swarms/one/events?after=4');
  snapshot.events = [
    { sequence: 5, swarm_id: 'one', conversation_id: 'conversation', turn_id: 'turn', payload: { type: 'text_delta', text: 'Hello' } },
    { sequence: 6, swarm_id: 'one', conversation_id: 'conversation', turn_id: 'turn', payload: { type: 'usage', input_tokens: 9 } },
  ];
  snapshot.cursor = 6;
  b.streams[0].onmessage({ data: JSON.stringify(snapshot.events[0]) });
  await b.settle();
  b.streams[0].onerror();
  expect(b.streams[0].closed).toBe(true);
  await b.retry();
  const stream = b.streams.at(-1);
  expect(stream.url).toBe('/api/swarms/one/events?after=6');
  stream.onmessage({ data: JSON.stringify(snapshot.events[1]) });
  stream.onmessage({ data: JSON.stringify({ ...snapshot.events[0], swarm_id: 'two', sequence: 99 }) });
  await b.settle();
  expect(b.value('events.size')).toBe(2);
  expect(b.elements.get('transcript').children.map(e => e.textContent)).toEqual(['Hello', 'input tokens: 9']);
  b.listeners.pagehide();
  expect(stream.closed).toBe(true);
  expect(b.timers.size).toBe(0);
  b.listeners.pageshow({ persisted: true });
  await b.settle();
  expect(b.streams.length).toBe(3);
});

test('a failed transition refresh recovers queue and membership on reconnect', async () => {
  let snapshot = initial(), unavailable = false;
  const b = await browser(async () => { if (unavailable) throw Error('offline'); return response(snapshot); });
  unavailable = true;
  b.streams[0].onmessage({ data: JSON.stringify({ sequence: 5, swarm_id: 'one', payload: { type: 'message_accepted' } }) });
  await b.settle();
  expect(b.timers.size).toBe(1);
  unavailable = false;
  snapshot.cursor = 9;
  snapshot.messages = [{ id: 'queued', agent_id: 'orchestrator', state: 'queued', text: 'Saved input' }];
  snapshot.workers = [{ id: 'worker', name: 'Worker', membership: 'active', lifecycle: 'ready', settings: { harness: 'claude', model: 'example' } }];
  await b.retry();
  expect(b.elements.get('queued').textContent).toBe('1 queued: Saved input');
  expect(b.elements.get('workers').children.length).toBe(1);
  expect(b.value('cursor')).toBe(9);
});

test('active reset offers interruption, settled reset starts a fresh conversation', async () => {
  const snapshot = initial(), requests = [];
  snapshot.turns = [{ id: 'active', agent_id: 'orchestrator', state: 'running' }];
  const b = await browser(async (url, options) => {
    if (options) {
      const request = JSON.parse(options.body); requests.push(request);
      if (request.action === 'interrupt') snapshot.turns[0].state = 'interrupted';
      if (request.action === 'reset') {
        snapshot.orchestrator.conversation_id = 'fresh';
        snapshot.conversations.push({ id: 'fresh', agent_id: 'orchestrator', created_at: '2026-10-24' });
      }
      snapshot.cursor++;
      return response({ ok: true });
    }
    return response(snapshot);
  });
  expect(b.elements.get('[data-action=reset]').textContent).toBe('Interrupt before new conversation');
  await b.value("submit('reset')");
  expect(requests[0].action).toBe('interrupt');
  expect(requests[0].turn_id).toBe('active');
  expect(b.elements.get('[data-action=reset]').textContent).toBe('New conversation');
  await b.value("submit('reset')");
  expect(requests[1].action).toBe('reset');
  expect(b.value('selectedConversation')).toBe('fresh');
  expect(b.value('snapshot.conversations.length')).toBe(2);
});

test('focusing an existing tab discovers a second workspace without changing the selected swarm', async () => {
  const b = await browser(async url => response(url === '/api/swarms' ? [initial(), { id: 'two', workspace: { name: 'Second' } }] : initial()));
  b.listeners.focus();
  await b.settle();
  const links = b.elements.get('nav').children;
  expect(links.map(a => a.href)).toEqual(['/swarms/one', '/swarms/two']);
  expect(links[0]['aria-current']).toBe('page');
  expect(b.value('swarm')).toBe('one');
  expect(b.streams.length).toBe(1);
});

test('restoring a page invalidates a snapshot fetch pending before page hide', async () => {
  let resolveFirst;
  let requests = 0;
  const b = await browser(async () => {
    if (++requests === 1) return await new Promise(resolve => { resolveFirst = resolve; });
    return response(initial());
  });
  expect(b.streams.length).toBe(0);
  b.listeners.pagehide();
  b.listeners.pageshow({ persisted: true });
  await b.settle();
  expect(b.streams.length).toBe(1);
  resolveFirst(response(initial()));
  await b.settle();
  expect(b.streams.length).toBe(1);
  b.listeners.pagehide();
  expect(b.streams.filter(s => !s.closed).length).toBe(0);
});

test('an obsolete connection failure cannot close a restored page stream', async () => {
  let rejectFirst;
  let requests = 0;
  const b = await browser(async () => {
    if (++requests === 1) return await new Promise((_, reject) => { rejectFirst = reject; });
    return response(initial());
  });
  b.listeners.pagehide();
  b.listeners.pageshow({ persisted: true });
  await b.settle();
  rejectFirst(Error('old request failed'));
  await b.settle();
  expect(b.streams.filter(s => !s.closed).length).toBe(1);
  expect(b.timers.size).toBe(0);
});
