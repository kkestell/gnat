'use strict';
const swarm = document.body.dataset.swarm;
const base = `/api/swarms/${swarm}`;
const byId = id => document.getElementById(id);
let snapshot, source, selectedConversation, pendingRequest;
let updates = Promise.resolve();
let cursor = 0;
const events = new Map();
function entry(text, kind = '', parent = '') {
  const el = document.createElement('div');
  el.className = `entry ${kind}${parent ? ' nested' : ''}`;
  el.textContent = text;
  byId('transcript').append(el);
  return el;
}
function render() {
  const log = byId('transcript');
  const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 80;
  log.replaceChildren();
  const messages = new Map(snapshot.messages.map(m => [m.id, m]));
  let lastDelta, lastTurn, lastParent;
  for (const e of events.values()) {
    const p = e.payload;
    if (e.conversation_id !== selectedConversation) continue;
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
      if (m) entry(m.text, 'user');
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
  // Queue is independent of the selected historic conversation.
  const queued = snapshot.messages.filter(m => m.state === 'queued');
  byId('queued').textContent = queued.length ? `${queued.length} queued: ${queued.map(m => m.text).join(' · ')}` : '';
  const a = snapshot.orchestrator;
  byId('state').textContent = `${a.lifecycle} · scheduling ${snapshot.scheduling}`;
  byId('agent-error').textContent = a.error || '';
  const select = byId('conversation');
  select.replaceChildren();
  snapshot.conversations.forEach((c, i) => {
    const option = document.createElement('option');
    option.value = c.id;
    option.textContent = `${i + 1} · ${c.created_at.replace('T', ' ').slice(0, 19)}${c.id === a.conversation_id ? ' (current)' : ''}`;
    option.selected = c.id === selectedConversation;
    select.append(option);
  });
  if (atBottom) log.scrollTop = log.scrollHeight;
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
async function connect() {
  try {
    await refresh();
    source = new EventSource(`${base}/events?after=${cursor}`);
    source.onopen = () => { byId('connection').textContent = 'Connected'; };
    source.onerror = () => { byId('connection').textContent = 'Disconnected · reconnecting…'; };
    source.onmessage = message => {
      updates = updates.then(async () => {
      const e = JSON.parse(message.data);
      if (e.swarm_id !== swarm || e.sequence <= cursor) return;
      // Refresh transitions atomically, so queue and execution state agree with history.
      if (['message_accepted', 'turn_started', 'turn_ended', 'agent_state', 'scheduling_changed', 'conversation_created'].includes(e.payload.type)) {
        try { await refresh(); } catch (error) { byId('request-error').textContent = error.message; }
      } else {
        if (e.sequence <= cursor) return;
        events.set(e.sequence, e); cursor = e.sequence; render();
      }
      }).catch(error => { byId('request-error').textContent = error.message; });
    };
  } catch (error) {
    byId('connection').textContent = 'Unable to connect';
    byId('request-error').textContent = error.message;
  }
}
async function submit(action, text = '') {
  if (pendingRequest) return;
  const turn = snapshot?.turns.find(t => t.state === 'running');
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
byId('chat').addEventListener('submit', e => { e.preventDefault(); if (byId('prompt').value.trim()) submit('send', byId('prompt').value); });
byId('prompt').addEventListener('keydown', e => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); byId('chat').requestSubmit(); } });
byId('conversation').addEventListener('change', e => { selectedConversation = e.target.value; render(); });
byId('retry-request').addEventListener('click', deliver);
for (const button of document.querySelectorAll('[data-action]')) button.addEventListener('click', () => submit(button.dataset.action));
window.addEventListener('pagehide', () => source?.close());
connect();
