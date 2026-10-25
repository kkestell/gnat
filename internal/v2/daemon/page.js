'use strict';
const swarm = document.body.dataset.swarm;
const base = `/api/swarms/${swarm}`;
const byId = id => document.getElementById(id);
let snapshot, source, selectedConversation, pendingRequest, selectedWorker, workerConversation;
let updates = Promise.resolve();
let cursor = 0;
let reconnectTimer, hidden = false, connectionAttempt = 0;
const events = new Map();
function appendEntry(log, text, kind = '', parent = '') {
  const el = document.createElement('div');
  el.className = `entry ${kind}${parent ? ' nested' : ''}`;
  el.textContent = text;
  log.append(el);
  return el;
}
function renderHistory(log, conversation) {
  const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 80;
  log.replaceChildren();
  const entry = (...args) => appendEntry(log, ...args);
  const messages = new Map(snapshot.messages.map(m => [m.id, m]));
  let lastDelta, lastTurn, lastParent;
  for (const e of events.values()) {
    const p = e.payload;
    if (e.conversation_id !== conversation) continue;
    const parent = p.parent_tool_use_id || '';
    if (p.type === 'text_delta') {
      if (!lastDelta || lastTurn !== e.turn_id || lastParent !== parent) lastDelta = entry('', '', parent);
      lastDelta.textContent += p.text || '';
      lastTurn = e.turn_id; lastParent = parent;
      continue;
    }
    lastDelta = null;
    if (p.type === 'turn_started') {
      const m = messages.get(p.message_id);
      if (m) entry(`${m.sender === 'user' ? 'You' : m.sender}:\n${m.text}`, 'user');
    } else if (p.type === 'message') entry(p.text || '', '', parent);
    else if (p.type === 'tool_call') entry(`${p.name || 'Tool'}\n${JSON.stringify(p.input || {}, null, 2)}`, 'activity', parent);
    else if (p.type === 'tool_result') entry(p.output || '(no output)', `activity${p.error ? ' error' : ''}`, parent);
    else if (p.type === 'turn_ended') entry(`${p.state}${p.error ? ` · ${p.error}` : ''}`, `outcome${p.error ? ' error' : ''}`);
    else if (p.type === 'usage') {
      const fields = ['input_tokens', 'output_tokens', 'cache_read_input_tokens', 'cache_creation_input_tokens'];
      const values = fields.filter(k => p[k] != null).map(k => `${k.replaceAll('_', ' ')}: ${p[k]}`);
      if (p.cost != null) values.push(`cost: $${Number(p.cost).toFixed(4)}`);
      if (values.length) entry(values.join(' · '), 'activity');
    }
  }
  if (atBottom) log.scrollTop = log.scrollHeight;
}
function render() {
  renderHistory(byId('transcript'), selectedConversation);
  // Queue is independent of the selected historic conversation.
  const queued = snapshot.messages.filter(m => m.agent_id === snapshot.orchestrator.id && m.state === 'queued');
  byId('queued').textContent = queued.length ? `${queued.length} queued: ${queued.map(m => m.text).join(' · ')}` : '';
  const a = snapshot.orchestrator;
  byId('state').textContent = `${a.lifecycle} · scheduling ${snapshot.scheduling}`;
  byId('agent-error').textContent = a.error || '';
  const running = snapshot.turns.some(t => t.agent_id === a.id && t.state === 'running');
  document.querySelector('[data-action=reset]').textContent = running ? 'Interrupt before new conversation' : 'New conversation';
  const select = byId('conversation');
  select.replaceChildren();
  snapshot.conversations.filter(c => c.agent_id === a.id).forEach((c, i) => {
    const option = document.createElement('option');
    option.value = c.id;
    option.textContent = `${i + 1} · ${c.created_at.replace('T', ' ').slice(0, 19)}${c.id === a.conversation_id ? ' (current)' : ''}`;
    option.selected = c.id === selectedConversation;
    select.append(option);
  });
  renderWorkers();
}
function renderWorkers() {
  const team = snapshot.workers || [];
  const all = [...team, ...(snapshot.removed_workers || [])];
  byId('empty-team').hidden = team.length > 0;
  const cards = byId('workers'); cards.replaceChildren();
  for (const a of team) {
    const card = document.createElement('article'); card.className = 'worker-card';
    const title = document.createElement('button'); title.textContent = a.name;
    title.addEventListener('click', () => { selectedWorker = a.id; workerConversation = a.conversation_id; renderWorkers(); });
    const state = document.createElement('p'); state.textContent = `${a.membership} · ${a.lifecycle} · ${a.settings.harness} / ${a.settings.model}`;
    const metadata = document.createElement('p'); metadata.className = 'muted'; metadata.textContent = `${a.working_dir}\n${a.branch || ''}${a.membership === 'active' ? '' : `\n${(a.stage || '').replaceAll('_', ' ')}`}`;
    const error = document.createElement('p'); error.className = 'error'; error.textContent = a.error || '';
    card.append(title, state, metadata, error); cards.append(card);
  }
  const history = byId('worker-history'); history.replaceChildren(new Option('Select a worker', ''));
  for (const a of all) history.append(new Option(`${a.name}${a.membership === 'removed' ? ' (removed)' : ''}`, a.id, false, a.id === selectedWorker));
  const a = all.find(a => a.id === selectedWorker);
  byId('worker-detail').hidden = !a; if (!a) return;
  byId('worker-name').textContent = a.name;
  byId('worker-metadata').textContent = `${a.membership} · ${a.lifecycle} · ${a.working_dir} · ${a.branch || ''}`;
  const conversations = snapshot.conversations.filter(c => c.agent_id === a.id);
  if (!conversations.some(c => c.id === workerConversation)) workerConversation = a.conversation_id;
  const select = byId('worker-conversation'); select.replaceChildren();
  conversations.forEach((c, i) => select.append(new Option(`${i + 1} · ${c.created_at.replace('T', ' ').slice(0, 19)}`, c.id, false, c.id === workerConversation)));
  renderHistory(byId('worker-transcript'), workerConversation);
  const queued = snapshot.messages.filter(m => m.agent_id === a.id && m.state === 'queued');
  if (queued.length) appendEntry(byId('worker-transcript'), `Queued: ${queued.map(m => m.text).join(' · ')}`, 'activity');
}
async function refresh() {
  const response = await fetch(base);
  if (!response.ok) throw new Error(await response.text());
  const next = await response.json();
  if (next.cursor < cursor) return;
  if (!snapshot || selectedConversation === snapshot.orchestrator.conversation_id) selectedConversation = next.orchestrator.conversation_id;
  snapshot = next;
  events.clear();
  for (const e of snapshot.events) events.set(e.sequence, e);
  cursor = snapshot.cursor;
  render();
}
async function refreshWorkspaces() {
  const response = await fetch('/api/swarms');
  if (!response.ok) throw new Error(await response.text());
  const nav = document.querySelector('nav');
  nav.replaceChildren();
  for (const workspace of await response.json()) {
    const link = document.createElement('a');
    link.href = `/swarms/${workspace.id}`;
    link.textContent = workspace.workspace.name;
    if (workspace.id === swarm) link.setAttribute('aria-current', 'page');
    nav.append(link);
  }
}
function reconnect() {
  connectionAttempt++;
  source?.close();
  clearTimeout(reconnectTimer);
  byId('connection').textContent = 'Disconnected · reconnecting…';
  if (!hidden) reconnectTimer = setTimeout(connect, 1000);
}
async function connect() {
  const attempt = ++connectionAttempt;
  source?.close();
  clearTimeout(reconnectTimer);
  updates = Promise.resolve();
  try {
    await refresh();
    if (hidden || attempt !== connectionAttempt) return;
    source = new EventSource(`${base}/events?after=${cursor}`);
    source.onopen = () => { if (attempt === connectionAttempt) byId('connection').textContent = 'Connected'; };
    source.onerror = () => { if (attempt === connectionAttempt) reconnect(); };
    source.onmessage = message => {
      updates = updates.then(async () => {
      if (hidden || attempt !== connectionAttempt) return;
      const e = JSON.parse(message.data);
      if (e.swarm_id !== swarm || e.sequence <= cursor) return;
      // Refresh transitions atomically, so queue and execution state agree with history.
      if (['message_accepted', 'turn_started', 'turn_ended', 'agent_state', 'scheduling_changed', 'conversation_created', 'agent_created', 'agent_removing', 'message_cancelled'].includes(e.payload.type)) {
        await refresh();
      } else {
        if (e.sequence <= cursor) return;
        events.set(e.sequence, e); cursor = e.sequence; render();
      }
      }).catch(error => {
        if (hidden || attempt !== connectionAttempt) return;
        byId('request-error').textContent = error.message; reconnect();
      });
    };
  } catch (error) {
    if (hidden || attempt !== connectionAttempt) return;
    byId('request-error').textContent = error.message;
    reconnect();
  }
}
async function submit(action, text = '') {
  if (pendingRequest) return;
  const turn = snapshot?.turns.find(t => t.agent_id === snapshot.orchestrator.id && t.state === 'running');
  if (action === 'reset' && turn) action = 'interrupt';
  pendingRequest = { request_id: crypto.randomUUID(), action, text, turn_id: action === 'interrupt' ? turn?.id || '' : '' };
  await deliver();
}
async function deliver() {
  byId('send').disabled = true;
  byId('request-error').textContent = '';
  byId('retry-request').hidden = true;
  try {
    const response = await fetch(`${base}/actions`, {
      method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Gnat-Browser': '1' }, body: JSON.stringify(pendingRequest)
    });
    if (!response.ok) {
      const reason = await response.text();
      if (response.status < 500) pendingRequest = null;
      throw new Error(reason);
    }
    const result = await response.json();
    if (!result.ok) throw new Error('Request was not acknowledged');
    if (pendingRequest.action === 'send' && byId('prompt').value === pendingRequest.text) byId('prompt').value = '';
    pendingRequest = null;
    await refresh();
  } catch (error) {
    byId('request-error').textContent = error.message;
    byId('retry-request').hidden = !pendingRequest;
  } finally { byId('send').disabled = !!pendingRequest; }
}
byId('worker-history').addEventListener('change', e => { selectedWorker = e.target.value; workerConversation = null; renderWorkers(); });
byId('worker-conversation').addEventListener('change', e => { workerConversation = e.target.value; renderWorkers(); });
byId('chat').addEventListener('submit', e => { e.preventDefault(); if (byId('prompt').value.trim()) submit('send', byId('prompt').value); });
byId('prompt').addEventListener('keydown', e => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); byId('chat').requestSubmit(); } });
byId('conversation').addEventListener('change', e => { selectedConversation = e.target.value; render(); });
byId('retry-request').addEventListener('click', deliver);
for (const button of document.querySelectorAll('[data-action]')) button.addEventListener('click', () => submit(button.dataset.action));
window.addEventListener('focus', () => refreshWorkspaces().catch(error => { byId('request-error').textContent = error.message; }));
window.addEventListener('pagehide', () => { hidden = true; connectionAttempt++; clearTimeout(reconnectTimer); source?.close(); });
window.addEventListener('pageshow', event => { if (event.persisted) { hidden = false; connect(); } });
connect();
