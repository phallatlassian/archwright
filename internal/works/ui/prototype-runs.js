// PROTOTYPE (throwaway): the Job detail's "Pieces and Wright runs" table, replaced by the Warden and Wright
// runs in operating order; clicking a run opens its transcript in a modal. Two variants on the same route,
// switched with ?variant=B|D (or the bar at the bottom, or the arrow keys). Uses app.js's globals.
'use strict';

const PR_VARIANTS = [['B', 'Waterfall'], ['D', 'By step']];
const prVariant = () => { const v = new URLSearchParams(location.search).get('variant'); return PR_VARIANTS.some((x) => x[0] === v) ? v : 'B'; };

const prDur = (ms) => (ms == null ? '' : ms === 0 ? '0s' : ms < 1000 ? ms + 'ms' : ms < 60000 ? Math.round(ms / 1000) + 's' : ms < 3600000 ? Math.floor(ms / 60000) + 'm' + String(Math.round((ms % 60000) / 1000)).padStart(2, '0') + 's' : Math.floor(ms / 3600000) + 'h' + String(Math.round((ms % 3600000) / 60000)).padStart(2, '0') + 'm');
const prClock = (d) => (d ? d.toLocaleTimeString([], { hour12: false }) : '');
const prCap = (s) => s.charAt(0).toUpperCase() + s.slice(1);

// prRows turns the Job's runs into rows in operating order. A Warden run has no dispatch; it is matched to
// the Plan's started steps in order (same Guild), and a Wright run to its dispatch (piece and round) and to
// the Warden run it started inside.
function prRows(full, ds, runs) {
  const steps = full.plan ? full.plan.steps : [];
  const states = full.step_states || {};
  const started = steps.filter((s) => states[s.id] && states[s.id] !== 'pending');
  const used = new Set();
  const now = Date.now();
  const rows = runs.map((r) => {
    const start = new Date(r.started_at);
    const end = r.ended_at ? new Date(r.ended_at) : null;
    return { run: r, start, end, ms: (end ? end : now) - start, d: ds.find((x) => x.id === r.dispatch_id || x.run_id === r.run_id) || null };
  }).sort((a, b) => a.start - b.start);
  for (const row of rows) {
    row.kind = row.d ? 'wright' : 'warden';
    if (row.kind === 'warden') {
      let s = started.find((x) => x.guild === row.run.guild && !used.has(x.id));
      if (!s) s = [...started].reverse().find((x) => x.guild === row.run.guild);
      if (s) { used.add(s.id); row.step = s; }
    }
  }
  const wardens = rows.filter((r) => r.kind === 'warden');
  for (const row of rows.filter((r) => r.kind === 'wright')) {
    row.parent = [...wardens].reverse().find((w) => w.run.guild === row.run.guild && w.start <= row.start && (!w.end || w.end >= row.start)) || null;
  }
  rows.forEach((r, i) => {
    r.n = i + 1;
    r.title = r.kind === 'warden' ? (r.step ? r.step.id : r.run.guild) : r.d.piece;
    r.sub = r.kind === 'warden' ? r.run.guild + ' Warden' : `${r.run.guild} Wright${r.d.round ? ', round ' + r.d.round : ''}`;
    r.status = r.run.status;
  });
  return rows;
}

// The Guilds' details come from /catalogue, fetched once; the first render shows names only and refreshes when it lands.
let prGuilds = null;
function prLoadGuilds() {
  if (prGuilds || prLoadGuilds.p) return;
  prLoadGuilds.p = get('/catalogue').then((c) => { prGuilds = Object.fromEntries(c.guilds.map((g) => [g.name, g])); refresh(); }).catch(() => { prGuilds = {}; });
}
// prGuildLine is the row's second line: the runner this run used.
const prGuildLine = (r) => (r.run.runner ? h('div', { class: 'pr-wf-guild' }, h('span', { class: 'muted' }, r.run.runner)) : null);

// prAccess is the Guild's access (read-only or read-write), shown in the transcript's header, not on every row.
const prAccess = (row) => { const g = prGuilds && prGuilds[row.run.guild]; return g ? h('span', { class: 'pr-access ' + g.access }, g.access) : null; };
// prPill names the role and the Guild ("Warden \u00b7 masonry"); hover for the Guild's access, runners, intake and handback.
function prPill(row) {
  const g = prGuilds && prGuilds[row.run.guild];
  const pill = h('span', { class: 'pr-pill ' + row.kind }, (row.kind === 'warden' ? 'Warden' : 'Wright') + ' \u00b7 ' + row.run.guild);
  if (g) {
    withTip(pill, () => h('div', { class: 'tipc' },
      h('div', { class: 'tiph' }, h('strong', {}, g.name + ' Guild'), h('span', { class: 'tag' }, g.access)),
      h('div', { class: 'muted' }, 'Runners: ' + ((g.runners || []).join(', ') || 'any') + ' \u00b7 ' + g.source + (row.run.runner ? ' \u00b7 this run: ' + row.run.runner : '')),
      h('div', {}, h('strong', {}, 'Takes: '), g.intake),
      h('div', {}, h('strong', {}, 'Hands back: '), g.handback)));
  }
  return pill;
}
const prStats = (row) => `${row.run.runner || ''} · ${tokens(row.run.input_tokens)} in / ${tokens(row.run.output_tokens)} out · ${money(row.run.cost_usd)}`;

// ---- the entry point app.js calls ------------------------------------------------------------------
function protoRunList({ full, ds, runs }) {
  const v = prVariant();
  prSwitcher();
  prLoadGuilds();
  const rows = prRows(full, ds, runs);
  const out = [h('h2', {}, 'Runs in operating order')];
  if (!rows.length) return [...out, h('p', { class: 'muted' }, 'Nothing has run yet.')];
  const w = rows.filter((r) => r.kind === 'warden').length;
  const span = Math.max(...rows.map((r) => +(r.end || Date.now()))) - Math.min(...rows.map((r) => +r.start));
  out.push(h('p', { class: 'muted' }, `${rows.length} runs (${w} Warden, ${rows.length - w} Wright) over ${prDur(span)} · ${money(rows.reduce((a, r) => a + r.run.cost_usd, 0))} · click a run for its transcript`));
  out.push({ B: prWaterfall, D: prByStep }[v](rows));
  return out;
}

// ---- variant B: waterfall --------------------------------------------------------------------------
function prWaterfall(rows) {
  const t0 = Math.min(...rows.map((r) => +r.start));
  const t1 = Math.max(...rows.map((r) => +(r.end || Date.now())));
  const span = Math.max(1, t1 - t0);
  const axis = h('div', { class: 'pr-wf-axis' }, [0, 25, 50, 75, 100].map((p) => h('span', { class: 'pr-tick' }, prDur(Math.round(span * p / 100)))));
  return h('div', { class: 'pr-wf' }, h('div', { class: 'pr-wf-row head' }, h('div', {}, ''), axis),
    rows.map((r) => {
      const left = ((+r.start - t0) / span) * 100, width = Math.max(0.7, ((+(r.end || Date.now()) - +r.start) / span) * 100);
      const bar = h('div', { class: 'pr-bar ' + r.kind + ' ' + r.status });
      bar.style.cssText = `left:${left.toFixed(2)}%;width:${width.toFixed(2)}%`;
      const dur = h('span', { class: 'pr-wf-dur mono' }, prDur(r.ms) + ' \u00b7 ' + money(r.run.cost_usd));
      if (left > 55) dur.style.cssText = `right:${(100 - left).toFixed(2)}%;margin-right:.5rem`; // late bars: the text goes left of the bar
      else dur.style.cssText = `left:${(left + width).toFixed(2)}%;margin-left:.5rem`; // early bars: right of the bar
      return h('div', { class: 'pr-wf-row click ' + r.kind, tabindex: 0, onclick: () => prOpen(r), onkeydown: (k) => { if (k.key === 'Enter') prOpen(r); } },
        h('div', { class: 'pr-wf-label' }, h('span', { class: 'muted mono' }, r.n + ' '), prPill(r), ' ', h('strong', {}, r.title), ' ', tag(r.status), r.run.runner ? h('span', { class: 'muted' }, r.run.runner) : null),
        h('div', { class: 'pr-wf-track' }, bar, dur));
    }));
}

// ---- variant D: grouped by step --------------------------------------------------------------------
function prByStep(rows) {
  const wardens = rows.filter((r) => r.kind === 'warden');
  const kids = (w) => rows.filter((r) => r.parent === w);
  const orphans = rows.filter((r) => r.kind === 'wright' && !r.parent);
  const line = (r, nested) => h('div', { class: 'pr-c-line click' + (nested ? ' nested' : ''), tabindex: 0, onclick: () => prOpen(r), onkeydown: (k) => { if (k.key === 'Enter') prOpen(r); } },
    h('div', { class: 'pr-c-main' },
      h('div', { class: 'pr-c-top' }, h('span', { class: 'muted mono' }, r.n), prPill(r), h('strong', {}, r.title), tag(r.status),
        h('span', { class: 'pr-c-right muted mono' }, `${prClock(r.start)} \u00b7 ${prDur(r.ms)} \u00b7 ${money(r.run.cost_usd)}`)),
      prGuildLine(r)));
  const out = wardens.map((w) => h('div', { class: 'panel pr-c-group' },
    line(w, false), w.step ? h('div', { class: 'pr-c-task muted' }, w.step.title) : null, kids(w).map((k) => line(k, true))));
  if (orphans.length) out.push(h('div', { class: 'panel pr-c-group' }, h('div', { class: 'muted' }, 'Wright runs outside any Warden run'), orphans.map((k) => line(k, true))));
  return h('div', {}, out);
}

// ---- the switcher bar ------------------------------------------------------------------------------
function prSwitcher() {
  const v = prVariant();
  let bar = document.getElementById('pr-switch');
  if (!bar) {
    bar = h('div', { id: 'pr-switch' });
    document.body.append(bar);
    document.addEventListener('keydown', (e) => {
      if (document.getElementById('pr-modal') || e.target.closest('input, textarea, [contenteditable]')) return;
      if (e.key === 'ArrowLeft') prStep(-1); else if (e.key === 'ArrowRight') prStep(1);
    });
  }
  const cur = PR_VARIANTS.find((x) => x[0] === v);
  bar.replaceChildren(h('button', { onclick: () => prStep(-1), 'aria-label': 'previous variant' }, '←'),
    h('span', {}, 'PROTOTYPE ', h('strong', {}, cur[0]), ' (' + cur[1] + ')'), h('button', { onclick: () => prStep(1), 'aria-label': 'next variant' }, '→'));
}
function prStep(d) {
  const i = PR_VARIANTS.findIndex((x) => x[0] === prVariant());
  const next = PR_VARIANTS[(i + d + PR_VARIANTS.length) % PR_VARIANTS.length][0];
  const q = new URLSearchParams(location.search);
  q.set('variant', next);
  history.replaceState(null, '', '?' + q + location.hash);
  refresh();
}

// ---- the modal -------------------------------------------------------------------------------------
const prRawCache = new Map();
function prRaw(runId, seq) {
  const k = runId + '/' + seq;
  if (!prRawCache.has(k)) prRawCache.set(k, get(`/runs/${runId}/events/${seq}/raw`));
  return prRawCache.get(k);
}
// prText pulls the readable text out of one raw vendor event.
function prText(raw) {
  try {
    const j = typeof raw === 'string' ? JSON.parse(raw) : raw;
    const c = j.message && j.message.content;
    if (Array.isArray(c)) {
      return c.map((b) => (b.type === 'text' ? b.text : b.type === 'tool_use' ? (b.input && b.input.command) || JSON.stringify(b.input, null, 2)
        : b.type === 'tool_result' ? (typeof b.content === 'string' ? b.content : JSON.stringify(b.content, null, 2)) : '')).filter(Boolean).join('\n\n');
    }
    if (typeof j.result === 'string') return j.result;
    return JSON.stringify(j, null, 2);
  } catch { return typeof raw === 'string' ? raw : JSON.stringify(raw, null, 2); }
}
function prClose() { const m = document.getElementById('pr-modal'); if (m) m.remove(); }
document.addEventListener('keydown', (e) => { if (e.key === 'Escape') prClose(); });

function prOpen(row) {
  prClose();
  const body = h('div', { class: 'pr-mbody' }, h('p', { class: 'muted' }, 'Loading transcript…'));
  const m = h('div', { id: 'pr-modal', class: 'pr-overlay', onclick: (e) => { if (e.target === m) prClose(); } },
    h('div', { class: 'pr-modal', role: 'dialog', 'aria-modal': 'true', 'aria-label': 'Run transcript' },
      h('div', { class: 'pr-mhead' }, prPill(row), h('strong', {}, row.title), tag(row.status), prAccess(row),
        h('span', { class: 'muted mono pr-grow' }, `${row.run.run_id} · ${prDur(row.ms)} · ${prStats(row)}`), h('button', { onclick: prClose }, 'Close')),
      body));
  document.body.append(m);
  get(`/runs/${row.run.run_id}/events`).then((evs) => prConversation(evs, row)).then((node) => body.replaceChildren(node))
    .catch((e) => body.replaceChildren(h('p', { class: 'bad' }, e.message)));
}

// ---- the transcript: a conversation ------------------------------------------------------------
// The run read as a conversation: the assistant's messages as bubbles, and each tool call as one card that
// holds its command and, once it has one, its output (paired by tool_use_id from the raw events).
const prToolText = (c) => (typeof c === 'string' ? c : Array.isArray(c) ? c.map((b) => (b && b.text) || '').join('\n') : JSON.stringify(c, null, 2));

async function prConversation(evs, row) {
  const id = row.run.run_id;
  const raws = new Map();
  await Promise.all(evs.filter((e) => e.has_raw && ['message', 'tool_use', 'tool_result', 'result'].includes(e.kind))
    .map((e) => prRaw(id, e.seq).then((r) => raws.set(e.seq, r)).catch(() => {})));
  const blocks = (e) => { const j = raws.get(e.seq); const c = j && j.message && j.message.content; return Array.isArray(c) ? c : []; };
  const results = new Map(); // tool_use_id -> the output that answered the call
  for (const e of evs) if (e.kind === 'tool_result') for (const b of blocks(e)) if (b.type === 'tool_result') results.set(b.tool_use_id, { text: prToolText(b.content), err: !!b.is_error, at: e.at });
  const called = new Set();
  const who = `${row.run.guild} ${row.kind === 'warden' ? 'Warden' : 'Wright'}`;
  const wrap = h('div', { class: 'pr-chat pr-conv' });
  const toolCards = [];
  const when = (at) => prClock(new Date(at));

  for (const e of evs) {
    if (e.kind === 'other' && !e.summary) continue;
    if (e.kind === 'posture' || e.kind === 'started') { wrap.append(h('div', { class: 'pr-note muted' }, e.summary)); continue; }
    if (e.kind === 'message') {
      wrap.append(h('div', { class: 'pr-msg' }, h('div', { class: 'pr-who muted' }, who + ' \u00b7 ' + when(e.at)), h('div', { class: 'pr-bubble assistant' }, prText(raws.get(e.seq)) || e.summary)));
    } else if (e.kind === 'result') {
      wrap.append(h('div', { class: 'pr-msg' }, h('div', { class: 'pr-who muted' }, 'Final result \u00b7 ' + when(e.at)), h('div', { class: 'pr-bubble result' }, prText(raws.get(e.seq)) || e.summary)));
    } else if (e.kind === 'tool_use') {
      const b = blocks(e).find((x) => x.type === 'tool_use');
      const cmd = b ? ((b.input && (b.input.command || b.input.file_path || b.input.pattern)) || JSON.stringify(b.input, null, 2)) : e.summary;
      const r = b && results.get(b.id);
      if (b) called.add(b.id);
      const first = String(cmd).split('\n')[0];
      const card = h('details', { class: 'pr-tool call' + (r && r.err ? ' err' : '') },
        h('summary', {}, h('span', { class: 'pr-tool-name' }, b ? b.name : 'tool'), h('span', { class: 'pr-tool-cmd mono' }, first.length > 140 ? first.slice(0, 140) + '\u2026' : first),
          h('span', { class: 'pr-tool-at muted mono' }, when(e.at)), r ? null : h('span', { class: 'muted' }, ' (no output recorded)')),
        h('pre', { class: 'pr-tool-body' }, cmd),
        r ? h('div', { class: 'pr-out-label muted' }, r.err ? 'error' : 'output') : null,
        r ? h('pre', { class: 'pr-tool-body pr-out' + (r.err ? ' err' : '') }, r.text || '(empty)') : null);
      toolCards.push(card);
      wrap.append(card);
    } else if (e.kind === 'tool_result') {
      const orphan = blocks(e).filter((b) => b.type === 'tool_result' && !called.has(b.tool_use_id));
      for (const b of orphan) wrap.append(h('details', { class: 'pr-tool call' }, h('summary', {}, h('span', { class: 'pr-tool-name' }, 'output'), h('span', { class: 'pr-tool-cmd mono' }, e.summary)), h('pre', { class: 'pr-tool-body pr-out' }, prToolText(b.content))));
    } else {
      wrap.append(h('div', { class: 'pr-note muted' }, `${e.kind}: ${e.summary}`));
    }
  }
  let open = false;
  const toggle = h('button', { onclick: () => { open = !open; toolCards.forEach((c) => { c.open = open; }); toggle.textContent = open ? 'Collapse tool calls' : 'Expand tool calls'; } }, 'Expand tool calls');
  return h('div', {}, h('div', { class: 'row pr-conv-bar' }, toggle, h('span', { class: 'muted' }, `${toolCards.length} tool calls \u00b7 ${evs.filter((e) => e.kind === 'message').length} messages`)), wrap);
}

