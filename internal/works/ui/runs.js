// The Job detail's run list and a run's transcript (ADR 0016: plain JavaScript, no build step). Loaded before app.js and uses its globals
// (h, get, tag, money, tokens, withTip) only when called, never while loading.
'use strict';

// ---- the model: plain data in, plain data out, no DOM -----------------------------------------------

// fmtDur reads a length of time in s, m and h: 0s, 45s, 5m30s, 2h45m.
function fmtDur(ms) {
  if (ms == null) return '';
  if (ms > 0 && ms < 1000) return Math.round(ms) + 'ms';
  const s = Math.round(ms / 1000);
  if (s < 60) return s + 's';
  if (s < 3600) return Math.floor(s / 60) + 'm' + String(s % 60).padStart(2, '0') + 's';
  const m = Math.round(s / 60);
  return Math.floor(m / 60) + 'h' + String(m % 60).padStart(2, '0') + 'm';
}

// runRows turns a Job's runs into one row each, in the order they started. A run with a dispatch is a Wright's,
// named for its piece; any other is a Warden's, named for the Plan step it recorded. A Wright whose recorded
// parent_run is a run in the list sits under it. A run that recorded neither is a plain row.
function runRows(runs, dispatches, now) {
  const byDispatch = new Map(dispatches.map((d) => [d.id, d]));
  const rows = runs.map((run, i) => {
    const wright = !!run.dispatch_id;
    const start = Date.parse(run.started_at);
    const end = run.ended_at ? Date.parse(run.ended_at) : run.status === 'running' ? Math.max(now, start) : start;
    const d = wright ? byDispatch.get(run.dispatch_id) : null;
    return { run, kind: wright ? 'wright' : 'warden', name: wright ? (d ? d.piece : '') : run.step || '', status: run.status, start, end, ms: end - start, i };
  }).sort((a, b) => a.start - b.start || a.i - b.i);
  const ids = new Set(rows.map((r) => r.run.run_id));
  rows.forEach((r, n) => { r.n = n + 1; r.indent = r.kind === 'wright' && !!r.run.parent_run && ids.has(r.run.parent_run); });
  return rows;
}

// runSpan is the time axis every row shares: from the first run's start to the latest end (now, for a run that is running).
function runSpan(rows) {
  if (!rows.length) return { t0: 0, span: 1 };
  const t0 = Math.min(...rows.map((r) => r.start));
  return { t0, span: Math.max(1, Math.max(...rows.map((r) => r.end)) - t0) };
}

// The timeline entries whose full text is in their raw vendor event.
const TEXT_KINDS = ['message', 'result', 'tool_use', 'tool_result'];

const BAR_MIN = 0.7; // percent of the axis, so a short run stays visible
const LABEL_LEFT_FROM = 55; // a bar starting past this percent has its label on its left

// barLayout places a row's bar on the axis, in percent, and says on which side its duration label goes.
function barLayout(row, t0, span) {
  const left = ((row.start - t0) / span) * 100;
  const width = Math.max(BAR_MIN, ((row.end - row.start) / span) * 100);
  return { left, width, labelLeft: left > LABEL_LEFT_FROM };
}

// axisTicks labels the axis at its start, quarters and end.
function axisTicks(span) { return [0, 25, 50, 75, 100].map((p) => fmtDur(Math.round((span * p) / 100))); }

// text of a tool's output or a message: a string, or a list of text blocks.
const blockText = (c) => (typeof c === 'string' ? c : Array.isArray(c) ? c.map((b) => (b && b.text) || '').join('\n') : c == null ? '' : JSON.stringify(c, null, 2));
const firstLine = (s) => String(s).split('\n')[0];

// pieces reads what one timeline entry says out of its raw vendor event. One raw line can carry several
// entries (a Claude message with text and a tool call), so each kind takes the next block of its type.
// Claude's events carry content blocks; Codex's carry an item.
function rawReader() {
  const taken = new Map(); // raw text -> block type -> how many were taken
  const next = (key, type, blocks) => {
    const t = taken.get(key) || taken.set(key, {}).get(key);
    const n = t[type] || 0;
    t[type] = n + 1;
    return blocks.filter((b) => b && b.type === type)[n];
  };
  return (e, raw, key) => {
    if (raw == null) return null;
    const j = typeof raw === 'string' ? safeParse(raw) : raw;
    if (!j || typeof j !== 'object') return null;
    const content = j.message && j.message.content;
    const item = j.item;
    switch (e.kind) {
      case 'message': {
        if (typeof content === 'string') return { text: content };
        const b = Array.isArray(content) && next(key, 'text', content);
        if (b) return { text: b.text };
        return item && typeof item.text === 'string' ? { text: item.text } : null;
      }
      case 'result':
        return typeof j.result === 'string' ? { text: j.result, failed: !!j.is_error } : null;
      case 'tool_use': {
        const b = Array.isArray(content) && next(key, 'tool_use', content);
        if (b) {
          const inp = b.input || {};
          return { id: b.id, name: b.name, command: String(inp.command || inp.file_path || inp.path || inp.pattern || inp.url || JSON.stringify(inp, null, 2)) };
        }
        return item && item.command != null ? { id: item.id, command: String(item.command) } : null;
      }
      case 'tool_result': {
        const b = Array.isArray(content) && next(key, 'tool_result', content);
        if (b) return { id: b.tool_use_id, text: blockText(b.content), failed: !!b.is_error };
        return item ? { id: item.id, text: blockText(item.aggregated_output), failed: item.status === 'failed' || (item.exit_code != null && item.exit_code !== 0) } : null;
      }
    }
    return null;
  };
}
function safeParse(s) { try { return JSON.parse(s); } catch { return null; } }

// transcriptItems reads a run's timeline as a conversation. evs are the timeline entries; raws maps a seq to
// its raw vendor event (text or parsed) when it was loaded. An entry without one is read from its summary,
// and the result says how many were. Each tool call is one item that holds its output, paired by the id the
// call and its result share.
function transcriptItems(evs, raws) {
  const read = rawReader();
  const items = [];
  const calls = new Map(); // tool_use_id -> the call item
  let fromSummary = 0;
  for (const e of evs) {
    const rawKey = raws.has(e.seq) ? (typeof raws.get(e.seq) === 'string' ? raws.get(e.seq) : JSON.stringify(raws.get(e.seq))) : null;
    const wantsText = TEXT_KINDS.includes(e.kind);
    const got = wantsText ? read(e, raws.get(e.seq), rawKey) : null;
    if (wantsText && !got) fromSummary++;
    if (!e.summary && !wantsText) continue; // an empty vendor event says nothing
    switch (e.kind) {
      case 'other': case 'posture':
        items.push({ type: 'note', text: e.summary });
        break;
      case 'message':
        items.push({ type: 'message', at: e.at, text: got ? got.text : e.summary });
        break;
      case 'result':
        items.push({ type: 'result', at: e.at, text: got ? got.text : e.summary, failed: !!(got && got.failed) });
        break;
      case 'tool_use': {
        const [summaryName, ...rest] = e.summary.split(': ');
        const call = { type: 'tool', at: e.at, name: (got && got.name) || (rest.length ? summaryName : 'tool'), command: got ? got.command : rest.length ? rest.join(': ') : e.summary, output: null, failed: false };
        items.push(call);
        if (got && got.id) calls.set(got.id, call);
        break;
      }
      case 'tool_result': {
        const text = got ? got.text : e.summary.replace(/^error: /, '');
        const failed = got ? got.failed : e.summary.startsWith('error: ');
        const call = got && got.id && calls.get(got.id);
        if (call && call.output === null) Object.assign(call, { output: text, failed, outputAt: e.at });
        else items.push({ type: 'tool', at: e.at, name: 'output', command: firstLine(text), output: text, failed, outputAt: e.at });
        break;
      }
      default:
        items.push({ type: 'note', text: e.kind + ': ' + e.summary });
    }
  }
  return { items, fromSummary };
}

// ---- the run list ------------------------------------------------------------------------------------

// runList draws every run of the Job, Warden and Wright, as rows on a shared time axis. guilds maps a Guild
// name to its catalogue entry. A row opens the run's transcript.
function runList({ runs, dispatches, guilds, now }) {
  const rows = runRows(runs, dispatches, now);
  if (!rows.length) return [h('h2', {}, 'Runs'), h('p', { class: 'muted' }, 'Nothing has run yet.')];
  const { t0, span } = runSpan(rows);
  const wardens = rows.filter((r) => r.kind === 'warden').length;
  const summary = `${rows.length} runs (${wardens} Warden, ${rows.length - wardens} Wright) over ${fmtDur(span)} · ${money(rows.reduce((a, r) => a + r.run.cost_usd, 0))} · select a run for its transcript`;
  const axis = h('div', { class: 'wf-row wf-head' }, h('div', {}),
    h('div', { class: 'wf-axis', 'aria-hidden': 'true' }, axisTicks(span).map((t) => h('span', {}, t))));
  const list = h('div', { class: 'wf', role: 'group', 'aria-label': 'Runs of this Job' }, axis, rows.map((r) => runRow(r, t0, span, guilds)));
  return [h('h2', {}, 'Runs'), h('p', { class: 'muted' }, summary), list];
}

// runPill names the role and the Guild. With card, hovering it shows what the Guild is: its access, runners,
// source, what it takes and what it hands back.
function runPill(row, guilds, card) {
  const g = guilds[row.run.guild];
  const pill = h('span', { class: 'wf-pill ' + row.kind }, (row.kind === 'warden' ? 'Warden' : 'Wright') + ' · ' + (row.run.guild || '?'));
  if (g && card) {
    withTip(pill, () => h('div', { class: 'tipc' },
      h('div', { class: 'tiph' }, h('strong', {}, g.name + ' Guild'), h('span', { class: 'tag' }, g.access)),
      h('div', { class: 'muted' }, 'Runners: ' + ((g.runners || []).join(', ') || 'any') + ' · ' + g.source),
      h('div', {}, h('strong', {}, 'Takes: '), g.intake),
      h('div', {}, h('strong', {}, 'Hands back: '), g.handback)), false);
  }
  return pill;
}

function runRow(row, t0, span, guilds) {
  const { left, width, labelLeft } = barLayout(row, t0, span);
  const bar = h('div', { class: 'wf-bar ' + row.kind + ' ' + row.status, style: `left:${left.toFixed(2)}%;width:${width.toFixed(2)}%` });
  const dur = h('span', { class: 'wf-dur mono', style: labelLeft ? `right:${(100 - left).toFixed(2)}%;margin-right:.5rem` : `left:${(left + width).toFixed(2)}%;margin-left:.5rem` },
    fmtDur(row.ms) + ' · ' + money(row.run.cost_usd));
  const pill = runPill(row, guilds, true);
  const open = () => openTranscript(row, guilds);
  const el = h('div', { class: 'wf-row wf-run' + (row.indent ? ' child' : ''), role: 'button', tabindex: 0, 'aria-haspopup': 'dialog', 'data-fk': 'run:' + row.run.run_id,
    onclick: open, onkeydown: (k) => { if (k.target === el && (k.key === 'Enter' || k.key === ' ')) { k.preventDefault(); open(); } } },
  h('div', { class: 'wf-label' }, h('span', { class: 'muted mono' }, row.n), pill, row.name ? h('strong', {}, row.name) : null, tag(row.status), row.run.runner ? h('span', { class: 'muted' }, row.run.runner) : null),
  h('div', { class: 'wf-track' }, bar, dur));
  // The pill's card shows on hover, and on the row's focus for a keyboard user (the pill is not a stop of its own).
  el.addEventListener('focus', () => { if (pill._tip) showTip(pill, pill._tip()); });
  el.addEventListener('blur', hideTip);
  return el;
}

// ---- the transcript dialog ---------------------------------------------------------------------------

const clock = (at) => new Date(at).toLocaleTimeString([], { hour12: false });

// loadRaws fetches the raw vendor event of every entry that has one and says something, a few at a time.
async function loadRaws(runId, evs, fetchRaw = (seq) => get(`/runs/${runId}/events/${seq}/raw`)) {
  const raws = new Map();
  let failed = 0;
  const want = evs.filter((e) => e.has_raw && TEXT_KINDS.includes(e.kind));
  let i = 0;
  await Promise.all(Array.from({ length: Math.min(6, want.length) }, async () => {
    while (i < want.length) {
      const e = want[i++];
      try { raws.set(e.seq, await fetchRaw(e.seq)); } catch { failed++; }
    }
  }));
  return { raws, failed };
}

// briefView says what the transcript's Brief section shows of GET /runs/{id}/brief: the text, how many lines it
// has, and whether it is the exact prompt (a run from before it was recorded has its dispatch's or step's brief).
function briefView(b) {
  const text = b && typeof b.brief === 'string' ? b.brief : '';
  if (!text) return { available: false, text: 'The brief this agent was started with is not available.' };
  const lines = text.replace(/\n+$/, '').split('\n').length;
  const exact = b.source === 'recorded';
  const from = b.source === 'dispatch' ? 'dispatch' : 'step';
  return { available: true, text, lines, exact, note: exact ? '' : `The exact prompt was not recorded for this run: this is the brief its ${from} kept, which can lack what was added when the run started.` };
}

// briefSection is the collapsed Brief block above a transcript's first entry.
function briefSection(b) {
  const v = briefView(b);
  if (!v.available) return h('div', { class: 'tx-note muted tx-brief-none' }, v.text);
  return h('details', { class: 'tx-brief' },
    h('summary', {}, h('span', { class: 'tx-brief-name' }, 'Brief'), h('span', { class: 'muted' }, `sent to the agent · ${v.lines} line${v.lines === 1 ? '' : 's'}`)),
    v.note ? h('div', { class: 'muted tx-label' }, v.note) : null,
    h('pre', { class: 'tx-brief-text' }, v.text));
}

// openTranscript shows a run as a conversation in a dialog. Esc, the Close button or a click outside closes
// it, and focus goes back to the row it was opened from.
function openTranscript(row, guilds) {
  const run = row.run;
  const g = guilds[run.guild];
  const body = h('div', { class: 'tx-body' }, h('p', { class: 'muted' }, 'Loading the transcript…'));
  const toggle = h('button', { hidden: true }, 'Expand tool calls');
  const dlg = h('dialog', { class: 'tx', role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': 'tx-title' },
    h('div', { class: 'tx-box' },
      h('div', { class: 'tx-head' },
        runPill(row, guilds, false), h('strong', { id: 'tx-title' }, row.name || run.guild || 'Run'), tag(row.status), g ? h('span', { class: 'wf-access ' + g.access }, g.access) : null,
        h('span', { class: 'muted mono tx-stats' }, [run.run_id, fmtDur(row.ms), run.runner, `${tokens(run.input_tokens)} in / ${tokens(run.output_tokens)} out`, money(run.cost_usd)].filter(Boolean).join(' · ')),
        toggle, h('button', { onclick: () => dlg.close() }, 'Close')),
      body));
  dlg.addEventListener('click', (e) => { if (e.target === dlg) dlg.close(); }); // the backdrop is the dialog itself
  dlg.addEventListener('close', () => {
    dlg.remove();
    const back = document.querySelector(`[data-fk="run:${run.run_id}"]`); // the list may have been redrawn since
    if (back) back.focus();
  });
  document.body.append(dlg);
  dlg.showModal();

  // A brief that cannot be loaded is not the transcript's failure: it is said once and the entries still show.
  const briefReq = get(`/runs/${run.run_id}/brief`).catch(() => null);
  get(`/runs/${run.run_id}/events`).then(async (evs) => {
    const brief = await briefReq;
    const { raws, failed } = await loadRaws(run.run_id, evs);
    const { items, fromSummary } = transcriptItems(evs, raws);
    const notes = [];
    if (evs.some((e) => !e.has_raw && TEXT_KINDS.includes(e.kind))) notes.push('The raw content of this run was pruned: showing the summaries that were kept.');
    if (failed) notes.push(`${failed} full message${failed === 1 ? '' : 's'} could not be loaded: showing summaries for them.`);
    const cards = [];
    body.replaceChildren(...notes.map((n) => h('p', { class: 'tx-pruned muted' }, n)), h('div', { class: 'tx-chat' }, briefSection(brief), items.length ? items.map((it) => transcriptItem(it, run, cards)) : h('p', { class: 'muted' }, 'This run recorded nothing.')));
    if (cards.length) {
      let open = false;
      toggle.hidden = false;
      toggle.onclick = () => { open = !open; cards.forEach((c) => { c.open = open; }); toggle.textContent = open ? 'Collapse tool calls' : 'Expand tool calls'; };
    }
  }).catch((e) => body.replaceChildren(h('p', { class: 'bad', role: 'alert' }, e.message)));
}

function transcriptItem(it, run, cards) {
  switch (it.type) {
    case 'note': return h('div', { class: 'tx-note muted' + (it.failed ? ' bad' : '') }, it.text);
    case 'message': return h('div', { class: 'tx-msg' }, h('div', { class: 'tx-who muted' }, `${run.guild} · ${clock(it.at)}`), h('div', { class: 'tx-bubble' }, it.text));
    case 'result': return h('div', { class: 'tx-msg' }, h('div', { class: 'tx-who muted' }, 'Final result · ' + clock(it.at)), h('div', { class: 'tx-bubble result' + (it.failed ? ' failed' : '') }, it.text));
    default: {
      const first = firstLine(it.command);
      const card = h('details', { class: 'tx-tool' + (it.failed ? ' failed' : '') },
        h('summary', {}, h('span', { class: 'tx-tool-name' }, it.name), h('span', { class: 'tx-tool-cmd mono' }, first.length > 140 ? first.slice(0, 140) + '…' : first),
          it.failed ? h('span', { class: 'tx-tool-failed' }, 'failed') : null, h('span', { class: 'tx-tool-at muted mono' }, clock(it.at))),
        h('pre', {}, it.command),
        it.output === null ? h('div', { class: 'muted tx-label' }, 'no output recorded') : [h('div', { class: 'muted tx-label' }, it.failed ? 'error output' : 'output'), h('pre', { class: 'tx-out' }, it.output || '(empty)')]);
      cards.push(card);
      return card;
    }
  }
}
