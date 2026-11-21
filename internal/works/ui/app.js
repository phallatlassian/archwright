// The Archwright dashboard: plain JavaScript over the Works' API and event stream (ADR 0007).
// The token arrives in the URL fragment, which a browser never sends to a server.
'use strict';

const $ = (id) => document.getElementById(id);

// h builds an element; children are text or nodes, so nothing from the API is ever parsed as HTML.
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'class') el.className = v;
    else if (k === 'style') el.style.cssText = v; // not setAttribute: the page's CSP forbids inline style attributes
    else if (v === true) el.setAttribute(k, '');
    else if (v !== false && v != null) el.setAttribute(k, v);
  }
  for (const kid of kids.flat(Infinity)) if (kid != null && kid !== false) el.append(kid.nodeType ? kid : String(kid));
  return el;
}

// ---- token and API ----------------------------------------------------------------
if (location.hash.startsWith('#token=')) {
  sessionStorage.setItem('aw-token', decodeURIComponent(location.hash.slice(7)));
  history.replaceState(null, '', location.pathname);
}
const token = sessionStorage.getItem('aw-token') || '';

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: { Authorization: 'Bearer ' + token, ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}) },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (!res.ok) throw new Error((data && data.error) || text || res.statusText);
  return data;
}
const get = (p) => api('GET', p);

function toast(msg, err) {
  const t = $('toast');
  t.textContent = msg;
  t.className = err ? 'err' : '';
  t.style.display = 'block';
  clearTimeout(toast.t);
  toast.t = setTimeout(() => { t.style.display = 'none'; }, err ? 6000 : 2500);
}

// act runs a command through the API, then reports and refreshes.
async function act(label, method, path, body, then) {
  try {
    await api(method, path, body);
    toast(label + ': done');
    if (document.activeElement) document.activeElement.blur(); // let the refresh replace the form that was just used
    if (then) then();
    else refresh();
  } catch (e) {
    toast(label + ': ' + e.message, true);
  }
}

// ---- step popover -----------------------------------------------------------------
// One floating card, shown while the pointer (or keyboard focus) is on an element that has a card to show.
// It ignores the pointer, so it never gets in the way. A redraw replaces the element it was shown for, so
// afterwards it moves to whatever step is under the pointer, with its up-to-date details.
let tipEl;
let pointer = { x: -1, y: -1 };
document.addEventListener('mousemove', (e) => { pointer = { x: e.clientX, y: e.clientY }; }, { passive: true });
function hideTip() { if (tipEl) tipEl.className = ''; }
// retip runs after a redraw: the element the card was for is gone, so if one that has a card is now under
// the pointer, show its (fresh) card there; otherwise hide it.
function retip() {
  const el = tipEl && tipEl.className === 'show' ? document.elementFromPoint(pointer.x, pointer.y) : null;
  const target = el && el.closest('[data-tip]');
  if (target) showTip(target, target._tip()); else hideTip();
}
function showTip(el, content) {
  if (!tipEl) { tipEl = h('div', { id: 'tip', role: 'tooltip' }); document.body.append(tipEl); }
  tipEl.replaceChildren(content);
  tipEl.className = 'show';
  // Anchor to the step's visible content (its header down to its last child), not the whole fixed-height
  // cell, so the card sits right against what the pointer is on.
  const box = el.getBoundingClientRect(), last = el.lastElementChild ? el.lastElementChild.getBoundingClientRect() : box;
  const r = { left: box.left, width: box.width, top: box.top, bottom: Math.min(box.bottom, last.bottom) };
  const t = tipEl.getBoundingClientRect(), gap = 4;
  const left = Math.min(Math.max(8, r.left + r.width / 2 - t.width / 2), window.innerWidth - t.width - 8);
  const top = r.bottom + gap + t.height > window.innerHeight && r.top - gap - t.height > 0 ? r.top - gap - t.height : r.bottom + gap;
  tipEl.style.left = left + 'px';
  tipEl.style.top = top + 'px';
}
// withTip makes el show build() on hover and focus.
function withTip(el, build) {
  el.setAttribute('tabindex', '0');
  el.dataset.tip = '1';
  el._tip = build;
  el.addEventListener('mouseenter', () => showTip(el, build()));
  el.addEventListener('focus', () => showTip(el, build()));
  el.addEventListener('mouseleave', hideTip);
  el.addEventListener('blur', hideTip);
  return el;
}
window.addEventListener('scroll', hideTip, true);

// stepCard is what the popover says about a Plan step: what it is, where it stands, and the Wright runs
// behind it. ctx is the Job's dispatches and runs; without it the card describes the step alone.
function stepCard(s, status, ctx) {
  const ds = ctx ? ctx.ds.filter((d) => d.piece === s.id).sort((a, b) => a.round - b.round) : [];
  return h('div', { class: 'tipc' },
    h('div', { class: 'tiph' }, h('strong', {}, s.id), s.checkpoint ? h('span', { class: 'cpm' }, ' \u25C7 checkpoint') : null,
      status ? h('span', { class: 'tag ' + status }, STATUS_LABEL[status]) : null),
    h('div', { class: 'muted' }, s.guild),
    h('div', {}, s.title),
    s.brief ? h('div', { class: 'tipb' }, s.brief) : null,
    s.blocked_by && s.blocked_by.length ? h('div', { class: 'muted' }, 'Waits on ' + s.blocked_by.join(', ')) : null,
    ctx ? (ds.length ? h('div', { class: 'tipr' }, ds.map((d) => {
      const r = ctx.runs.find((x) => x.dispatch_id === d.id);
      return h('div', {}, h('span', { class: 'muted' }, 'Round ' + d.round + ' '), tag(d.status),
        r ? h('span', { class: 'muted' }, ` ${r.runner || ''} \u00b7 ${tokens(r.input_tokens)} in / ${tokens(r.output_tokens)} out \u00b7 ${money(r.cost_usd)}`) : null);
    })) : h('div', { class: 'muted' }, 'No Wright has run for this step.')) : null);
}

// ---- formatting -------------------------------------------------------------------
const money = (n) => '$' + (n || 0).toFixed(4);
const tokens = (n) => (n || 0).toLocaleString();
const ago = (iso) => {
  if (!iso) return '';
  const s = Math.max(0, (Date.now() - new Date(iso)) / 1000);
  if (s < 90) return Math.round(s) + 's ago';
  if (s < 5400) return Math.round(s / 60) + 'm ago';
  if (s < 129600) return Math.round(s / 3600) + 'h ago';
  return Math.round(s / 86400) + 'd ago';
};
const tag = (s) => h('span', { class: 'tag ' + s }, s);
const jobLink = (id, text) => h('a', { href: '#/job/' + id }, text || id);


// ---- views ------------------------------------------------------------------------
// Each view fetches what it shows and returns a node; refresh() re-renders the current one.
const views = {
  async board() {
    const jobs = await get('/board');
    const sites = [...new Set(jobs.map((j) => j.site || '(no site)'))].sort();
    const want = sessionStorage.getItem('aw-site') || '';
    const shown = jobs.filter((j) => !want || (j.site || '(no site)') === want)
      .sort((a, b) => a.created_at.localeCompare(b.created_at) || a.id.localeCompare(b.id)); // oldest first, and never by state, so a lane stays put as its Job moves
    const [full, dispatches, runs, catalogue] = await Promise.all([Promise.all(shown.map((j) => get('/jobs/' + j.id))),
      Promise.all(shown.map((j) => get(`/jobs/${j.id}/dispatches`))), Promise.all(shown.map((j) => get(`/jobs/${j.id}/runs`))), get('/catalogue').catch(() => ({ jigs: [] }))]);
    const sel = h('select', { onchange: (e) => { sessionStorage.setItem('aw-site', e.target.value); e.target.blur(); refresh(); } }, // blur so the redraw is not held back by focus
      h('option', { value: '' }, 'All Sites'),
      sites.map((s) => h('option', { value: s, selected: s === want }, s)));
    const lane = (j, i, columns) => {
      const plan = full[i].plan;
      return h('div', { class: 'lane' },
        h('a', { class: 'lhead', href: '#/job/' + j.id },
          h('div', { class: 't' }, j.title),
          h('div', {}, tag(j.state), plan && plan.approval === 'pending' ? tag('plan pending') : null),
          h('div', { class: 'm' }, [j.site || 'no site', j.id].join(' · ')),
          h('div', { class: 'm' },
            j.live_runs ? j.live_runs + ' running · ' : '',
            j.open_asks ? j.open_asks + ' ask(s) · ' : '',
            money(j.cost_usd))),
        plan && plan.steps.length ? stairTrack(plan.steps, stepStatuses(plan.steps, dispatches[i], full[i]), !!columns, columns, { ds: dispatches[i], runs: runs[i] }) : h('div', { class: 'muted lempty' }, 'No Plan yet.'));
    };
    // Group by the Jig each Plan came from: one section per Jig, then ad hoc Plans, then Jobs with no Plan.
    const AD_HOC = '\uffffad hoc', NO_PLAN = '\uffffzno plan'; // sort after every Jig name
    const groups = new Map();
    shown.forEach((j, i) => {
      const key = !j.steps ? NO_PLAN : j.jig || AD_HOC;
      if (!groups.has(key)) groups.set(key, []);
      groups.get(key).push({ j, i });
    });
    const desc = Object.fromEntries(catalogue.jigs.map((j) => [j.name, j.description]));
    const label = (k) => (k === AD_HOC ? 'Ad hoc Plans' : k === NO_PLAN ? 'No Plan yet' : k);
    const sections = [...groups].sort(([a], [b]) => a.localeCompare(b)).map(([k, members]) => {
      // A Jig's columns come from its definition (or, if it is gone, its first Plan), so every lane lines up.
      const named = k !== AD_HOC && k !== NO_PLAN;
      const layout = named ? ((catalogue.jigs.find((j) => j.name === k) || {}).steps || []).map((s) => ({ id: s.id, guild: s.guild, title: s.task, blocked_by: s.blocked_by, checkpoint: s.checkpoint })) : [];
      const steps = layout.length ? layout : named ? full[members[0].i].plan.steps
        : k === AD_HOC ? sharedSteps(members.map(({ i }) => full[i].plan.steps)) : [];
      const ordered = k === AD_HOC ? steps : layers(steps).flat(); // the merged order is already one column per step
      const columns = ordered.length ? ordered.map((s) => s.id) : null;
      // Each group expands and collapses; the choice survives the live refresh and reloads of the tab.
      const closed = () => { try { return JSON.parse(sessionStorage.getItem('aw-collapsed') || '[]'); } catch { return []; } };
      return h('details', { class: 'group', open: !closed().includes(k), ontoggle: (e) => {
        const rest = closed().filter((x) => x !== k);
        sessionStorage.setItem('aw-collapsed', JSON.stringify(e.target.open ? rest : [...rest, k]));
      } },
        h('summary', {}, h('h2', {}, label(k), ' ', h('span', { class: 'muted' }, members.length), desc[k] ? h('span', { class: 'muted gdesc' }, desc[k]) : null)),
        h('div', { class: 'lanes' }, ordered.length ? jigHeader(ordered) : null, members.map(({ j, i }) => lane(j, i, columns))));
    });
    return h('div', {}, crumbs(), h('h1', {}, 'Jobs'), h('div', { class: 'row' }, sel, legend()), sections.length ? sections : h('p', { class: 'muted' }, 'No Jobs.'));
  },

  async needsYou() {
    const [board, items, asks] = await Promise.all([get('/board'), get('/needs-you'), get('/asks?status=open')]);
    const byId = Object.fromEntries(board.map((j) => [j.id, j]));
    const title = (id) => (byId[id] ? byId[id].title : id);
    if (!ny) ny = newInbox();
    const entries = [];

    for (const j of board.filter((j) => j.plan_approval === 'pending' && !j.outcome)) {
      entries.push({ key: 'plan:' + j.id, kind: 'plan', jobId: j.id, at: j.updated_at, byArchwright: true, build: async () => {
        const plan = await get('/jobs/' + j.id + '/plan');
        return h('div', { class: 'panel attn' },
          h('h3', {}, 'Plan approval · ', jobLink(j.id, title(j.id))),
          jigTrack(plan.steps),
          h('div', { class: 'row' },
            h('button', { class: 'primary', onclick: () => act('Approve Plan', 'POST', `/jobs/${j.id}/plan/approve`, { updated_at: plan.updated_at }) }, 'Approve'),
            h('button', { class: 'danger', onclick: () => act('Reject Plan', 'POST', `/jobs/${j.id}/plan/reject`, {}) }, 'Reject')));
      } });
    }

    for (const a of asks.filter((a) => a.holder === 'archwright')) {
      entries.push({ key: 'ask:' + a.id, kind: 'ask', jobId: a.job_id, at: a.created_at, guild: a.guild, piece: a.piece, dispatchId: a.dispatch_id, build: () => askPanel(a, title(a.job_id)) });
    }

    for (const j of board.filter((j) => j.state === 'parked')) {
      entries.push({ key: 'parked:' + j.id, kind: 'parked', jobId: j.id, at: j.updated_at, latest: true, build: () => h('div', { class: 'panel attn' },
        h('h3', {}, 'Parked · ', jobLink(j.id, title(j.id))),
        h('p', {}, j.parked_for),
        instructBox(j.id, 'Tell the Archwright what to do'),
        h('div', { class: 'row' }, h('button', { class: 'danger', onclick: () => confirm('Cancel and discard this Job?') && act('Cancel Job', 'POST', `/jobs/${j.id}/cancel`, {}) }, 'Cancel Job'))) });
    }

    for (const it of items.filter((it) => it.kind !== 'ask')) { // asks have their own panels above
      const detail = it.detail || it.payload || {};
      entries.push({ key: 'item:' + it.id, kind: it.kind, jobId: it.job_id || '', at: it.created_at, guild: detail.guild, piece: detail.piece, latest: true, build: () => h('div', { class: 'panel attn' },
        h('h3', {}, tag(it.kind), ' ', it.job_id ? jobLink(it.job_id, title(it.job_id)) : '', ' ', h('span', { class: 'muted' }, ago(it.created_at))),
        h('p', {}, it.summary),
        it.detail && Object.keys(it.detail).length ? h('pre', {}, JSON.stringify(it.detail, null, 2)) : null,
        it.job_id ? instructBox(it.job_id, 'Tell the Archwright what to do about it') : null) });
    }

    await updateInbox(ny, await resolveOrigins(entries, byId));
    return ny.root;
  },

  async job(id) {
    const [full, ds, reviews, runs, wts] = await Promise.all([
      get('/jobs/' + id), get(`/jobs/${id}/dispatches`), get(`/jobs/${id}/reviews`), get(`/jobs/${id}/runs`), get(`/jobs/${id}/worktrees`).catch(() => []),
    ]);
    const board = (await get('/board')).find((j) => j.id === id);
    const state = board ? board.state : '';
    const closed = !!full.outcome;
    const live = ds.filter((d) => d.status === 'queued' || d.status === 'running');
    const guilds = [...new Set([...(full.plan ? full.plan.steps.map((s) => s.guild) : []), ...ds.map((d) => d.guild)])];
    const cost = runs.reduce((a, r) => a + r.cost_usd, 0);
    const controls = closed ? [] : [
      full.paused_at
        ? h('button', { onclick: () => act('Resume', 'POST', `/jobs/${id}/resume`, {}) }, 'Resume')
        : h('button', { onclick: () => act('Pause', 'POST', `/jobs/${id}/pause`, {}) }, 'Pause'),
      h('button', { class: 'danger', onclick: () => confirm('Cancel and discard this Job?') && act('Cancel Job', 'POST', `/jobs/${id}/cancel`, {}) }, 'Cancel'),
    ];

    const parts = [
      crumbs(full.title),
      h('h1', {}, full.title, ' ', tag(state)),
      h('p', { class: 'muted' }, [full.site || 'no site', id, 'created ' + ago(full.created_at), full.writ ? 'writ ' + full.writ : '', money(cost)].filter(Boolean).join(' · ')),
      full.source_issue ? h('p', { class: 'muted' }, 'Source issue: ', h('a', { href: full.source_issue, target: '_blank', rel: 'noopener' }, full.source_issue)) : null,
      full.description ? h('p', {}, full.description) : null,
      h('div', { class: 'row' }, controls),
    ];
    if (full.parked_for) parts.push(h('div', { class: 'panel attn' }, h('strong', {}, 'Parked: '), full.parked_for));

    if (full.plan) {
      const p = full.plan;
      parts.push(h('h2', {}, 'Plan ', tag(p.approval), p.jig ? h('span', { class: 'muted' }, ' from Jig ' + p.jig) : ' (ad hoc)'));
      parts.push(stairTrack(p.steps, stepStatuses(p.steps, ds, full), false, null, { ds, runs }));
      if (p.approval === 'pending') {
        parts.push(h('div', { class: 'row' },
          h('button', { class: 'primary', onclick: () => act('Approve Plan', 'POST', `/jobs/${id}/plan/approve`, { updated_at: p.updated_at }) }, 'Approve Plan'),
          h('button', { class: 'danger', onclick: () => act('Reject Plan', 'POST', `/jobs/${id}/plan/reject`, {}) }, 'Reject Plan')));
      }
    } else parts.push(h('h2', {}, 'Plan'), h('p', { class: 'muted' }, 'No Plan yet.'));

    if (full.asks.length) parts.push(h('h2', {}, 'Open asks'), full.asks.map((a) => askPanel(a, full.title)));

    parts.push(h('h2', {}, 'Wardens'));
    parts.push(guilds.length ? table(['Guild (Warden)', 'Pieces', 'Live', 'Cost'], guilds.map((g) => {
      const mine = ds.filter((d) => d.guild === g);
      const rs = runs.filter((r) => r.guild === g);
      return [g, [...new Set(mine.map((d) => d.piece))].length, mine.filter((d) => live.includes(d)).length, money(rs.reduce((a, r) => a + r.cost_usd, 0))];
    })) : h('p', { class: 'muted' }, 'No Warden has dispatched work.'));

    parts.push(h('h2', {}, 'Pieces and Wright runs'));
    parts.push(ds.length ? table(['Piece', 'Guild', 'Round', 'Status', 'Run', 'Runner', 'Tokens in/out', 'Cost'], ds.map((d) => {
      const r = runs.find((x) => x.dispatch_id === d.id);
      return [d.piece, d.guild, d.round, tag(d.status),
        r ? r.status : (d.run_id ? 'run' : ''),
        r ? r.runner : '', r ? tokens(r.input_tokens) + ' / ' + tokens(r.output_tokens) : '', r ? money(r.cost_usd) : ''];
    }), ds.map((d) => { // a row opens its run's timeline
      const run = (runs.find((x) => x.dispatch_id === d.id) || {}).run_id || d.run_id;
      return run ? '#/run/' + run + '/' + id : '';
    })) : h('p', { class: 'muted' }, 'No Wrights dispatched.'));

    if (reviews.reviews && reviews.reviews.length) {
      parts.push(h('h2', {}, 'Reviews'));
      for (const r of reviews.reviews) {
        parts.push(h('div', { class: 'panel' }, h('strong', {}, '#' + r.seq + ' '), tag(r.status), r.overall ? tag(r.overall) : null,
          ['standards', 'spec'].filter((k) => r[k]).map((k) => h('div', {}, h('strong', {}, k + ': '), r[k].verdict, r[k].reason ? ' — ' + r[k].reason : '',
            (r[k].findings || []).map((f) => h('div', { class: 'mono muted' }, `${f.file}:${f.line} [${f.severity}] ${f.reason}`))))));
      }
    }
    if (wts && wts.length) {
      parts.push(h('h2', {}, 'Worktrees'), table(['Kind', 'Branch', 'Path'], wts.map((w) => [w.kind, w.branch, h('span', { class: 'mono' }, w.path)])));
    }
    if (!closed) parts.push(h('h2', {}, 'Instruct the Archwright'), instructBox(id, 'A free-text instruction'));
    return h('div', {}, parts);
  },

  async run(id) {
    const jobId = location.hash.split('/')[3] || '';
    const [evs, job, runs, dispatches] = await Promise.all([get(`/runs/${id}/events`),
      ...(jobId ? [get('/jobs/' + jobId), get(`/jobs/${jobId}/runs`), get(`/jobs/${jobId}/dispatches`)].map((p) => p.catch(() => null)) : [null, null, null])]);
    return runTimeline(id, jobId, evs, job, runs, dispatches);
  },

  async usage() {
    const [use, board] = await Promise.all([get('/usage'), get('/board')]);
    const title = Object.fromEntries(board.map((j) => [j.id, j.title]));
    const sum = (rows, k) => rows.reduce((a, r) => a + r[k], 0);
    const group = (key) => {
      const m = new Map();
      for (const r of use) {
        const g = m.get(r[key]) || { runs: 0, input_tokens: 0, output_tokens: 0, cost_usd: 0 };
        for (const k of Object.keys(g)) g[k] += r[k];
        m.set(r[key], g);
      }
      return [...m].map(([k, g]) => [k || '(unknown)', g.runs, tokens(g.input_tokens), tokens(g.output_tokens), money(g.cost_usd)]);
    };
    const head = (first) => [first, 'Runs', 'Tokens in', 'Tokens out', 'Cost'];
    const perJob = group('job_id').map((r) => { r[0] = h('a', { href: '#/job/' + r[0] }, title[r[0]] || r[0]); return r; });
    return h('div', {}, crumbs('Usage'), h('h1', {}, 'Usage and cost'),
      h('p', { class: 'muted' }, `Total ${money(sum(use, 'cost_usd'))} over ${sum(use, 'runs')} runs. A runner that reports no cost (codex) counts $0.`),
      h('h2', {}, 'Per Job'), table(head('Job'), perJob),
      h('h2', {}, 'Per Guild'), table(head('Guild'), group('guild')),
      h('h2', {}, 'Per runner'), table(head('Runner'), group('runner')));
  },

  async catalogue() {
    const [sites, cat] = await Promise.all([get('/sites'), get('/catalogue')]);
    const out = [crumbs('Catalogue'), h('h1', {}, 'Catalogue'), h('h2', {}, 'Sites'),
      sites.length ? table(['Name', 'URL', 'Base', 'Cap', 'Typecheck', 'Lint', 'Test'], sites.map((s) => [s.name, s.url, s.base_branch, s.concurrency_cap,
        ...['typecheck', 'lint', 'test'].map((k) => h('code', {}, s.gates[k] || '—'))])) : h('p', { class: 'muted' }, 'No Sites registered.'),
      h('h2', {}, 'Guilds'),
      table(['Name', 'Access', 'Runners', 'Intake', 'Handback', 'Source'], cat.guilds.map((g) => [g.name, g.access, (g.runners || []).join(', ') || 'any', g.intake, g.handback, h('span', { class: 'mono' }, g.source)])),
      h('h2', {}, 'Jigs'),
      ...cat.jigs.map((j) => h('div', { class: 'panel' }, h('strong', {}, j.name), ' ', h('span', { class: 'muted' }, j.description),
        j.parameters && j.parameters.length ? h('div', { class: 'muted' }, 'Parameters: ' + j.parameters.map((p) => p.name + (p.required ? '*' : '')).join(', ')) : null,
        jigTrack(j.steps.map((s) => ({ id: s.id, guild: s.guild, title: s.task, blocked_by: s.blocked_by, checkpoint: s.checkpoint }))))),
    ];
    if (cat.errors && cat.errors.length) out.push(h('h2', {}, 'Definitions that failed to load'), ...cat.errors.map((e) => h('div', { class: 'panel attn' }, `${e.kind} ${e.name}: ${e.error}`)));
    return h('div', {}, out);
  },

  async runner() {
    const [queue, board, sites] = await Promise.all([get('/queue'), get('/board'), get('/sites')]);
    const title = Object.fromEntries(board.map((j) => [j.id, j.title]));
    const job = (id) => jobLink(id, title[id] || id);
    const count = (key) => queue.running.reduce((m, q) => m.set(q[key], (m.get(q[key]) || 0) + 1), new Map());
    const perSite = count('site'), perGuild = count('guild');
    const cap = Object.fromEntries(sites.map((x) => [x.name, x.concurrency_cap]));
    const out = [crumbs('Runner'), h('h1', {}, 'Runner'),
      h('p', { class: 'muted' }, 'The scheduler that starts Wrights. Work waits in a queue, oldest first, when a cap is reached.')];
    if (queue.paused) out.push(h('div', { class: 'panel attn' }, h('strong', {}, 'Paused after rate limits '), `until ${queue.paused.until} (${queue.paused.failures} in a row). Nothing new starts until then.`));
    out.push(h('h2', {}, 'Capacity'), table(['Cap', 'In use', 'Limit'], [
      ['Total', queue.running.length, queue.limits.total],
      ...[...perSite].map(([k, n]) => ['Site ' + (k || '(none)'), n, cap[k] || queue.limits.per_site]),
      ...[...perGuild].map(([k, n]) => ['Warden ' + k, n, queue.limits.per_warden]),
    ]));
    out.push(h('h2', {}, 'Running ', h('span', { class: 'muted' }, queue.running.length)),
      queue.running.length ? table(['Dispatch', 'Job', 'Piece', 'Guild', 'Site'], queue.running.map((q) => [q.dispatch_id, job(q.job), q.piece, q.guild, q.site || '']))
        : h('p', { class: 'muted' }, 'Nothing is running.'));
    out.push(h('h2', {}, 'Queued ', h('span', { class: 'muted' }, queue.queued.length)),
      queue.queued.length ? table(['#', 'Dispatch', 'Job', 'Piece', 'Guild', 'Site', 'Waiting on'], queue.queued.map((q) => [q.position || '', q.dispatch_id, job(q.job), q.piece, q.guild, q.site || '', q.waiting || '']))
        : h('p', { class: 'muted' }, 'Nothing is waiting.'));
    return h('div', {}, out);
  },
};

// ---- shared pieces ----------------------------------------------------------------
// crumbs draws the breadcrumb trail every screen starts with. It begins at Jobs, the home screen; an item is
// a string (plain text) or [text, href] (a link); the last item is the current page.
function crumbs(...rest) {
  const trail = [rest.length ? ['Jobs', '#/board'] : 'Jobs', ...rest];
  const kids = [];
  trail.forEach((c, i) => {
    if (i) kids.push(h('span', {}, '\u203A'));
    const [text, href] = Array.isArray(c) ? c : [c];
    kids.push(i < trail.length - 1 && href ? h('a', { href }, text) : h('span', i === trail.length - 1 ? { class: 'cur', 'aria-current': 'page' } : {}, text));
  });
  return h('nav', { class: 'crumbs', 'aria-label': 'Breadcrumb' }, kids);
}

// table draws rows of cells. When hrefs is given, a row with an href opens it on a click or Enter, so the
// whole row is the target and not only a link inside it; a click on a link or button in the row is left alone.
function table(head, rows, hrefs) {
  return h('table', {}, h('thead', {}, h('tr', {}, head.map((c) => h('th', {}, c)))),
    h('tbody', {}, rows.map((r, i) => {
      const href = hrefs && hrefs[i];
      const tr = h('tr', href ? { class: 'click', tabindex: '0' } : {}, r.map((c) => h('td', {}, c)));
      if (href) {
        const open = () => { location.hash = href; };
        tr.addEventListener('click', (e) => { if (!e.target.closest('a, button')) open(); });
        tr.addEventListener('keydown', (e) => { if (e.key === 'Enter' && e.target === tr) open(); });
      }
      return tr;
    })));
}

// layers groups steps into columns by depth: a step sits one column right of its deepest blocker.
function layers(steps) {
  const by = Object.fromEntries(steps.map((s) => [s.id, s]));
  const depth = {};
  const of = (s, seen = new Set()) => {
    if (depth[s.id] != null) return depth[s.id];
    if (seen.has(s.id)) return 0;
    seen.add(s.id);
    return (depth[s.id] = Math.max(-1, ...(s.blocked_by || []).filter((b) => by[b]).map((b) => of(by[b], seen))) + 1);
  };
  steps.forEach((s) => of(s));
  const out = [];
  steps.forEach((s) => (out[depth[s.id]] = out[depth[s.id]] || []).push(s));
  return out.filter(Boolean);
}

// stepStatuses says where each Plan step stands: done, running, blocked or pending. The API only reports
// a running step (step_states), so the rest is read from the Job's dispatches, matching a dispatch to a step
// by piece name; a step whose latest dispatch is live is running, one that ended on an ask is blocked, one
// that reported is done, and anything else is pending. An accepted Job has every step done.
function stepStatuses(steps, ds, job) {
  const out = {};
  for (const s of steps) {
    const mine = ds.filter((d) => d.piece === s.id).sort((a, b) => b.round - a.round || b.created_at.localeCompare(a.created_at));
    const d = mine[0];
    out[s.id] = job.outcome === 'accepted' ? 'done'
      : !d ? 'pending'
      : d.status === 'queued' || d.status === 'running' ? 'running'
      : d.status === 'blocked' ? 'blocked'
      : d.status === 'reported' || d.status === 'answered' ? 'done'
      : 'pending';
  }
  return out;
}
const STATUS_LABEL = { done: 'completed', running: 'executing', blocked: 'blocked', pending: 'pending' };

// jigTrack draws a Jig for the catalogue: dots joined by a line, one column per depth with the step name as
// its header. Steps that can run in parallel stack in one column, and the line curves from each step into
// the steps it blocks, so a fan-in bends together. A hollow dot marks a checkpoint. Every step has a fixed
// height so the curves can be placed without measuring the page.
const COLW = 120; // the narrowest a column of a Job lane and its header gets; wider lanes spread the columns evenly
const STEP = 96; // px per step: header, rail, guild and up to two lines of task
const DOT_Y = 31; // px from the top of a step to its dot's centre
const LINK = 44; // px between columns
function jigTrack(steps) {
  const cols = layers(steps);
  const tall = Math.max(...cols.map((c) => c.length));
  const dotY = (n, k) => ((tall - n) / 2 + k) * STEP + DOT_Y;
  const ns = 'http://www.w3.org/2000/svg';
  const parts = [];
  cols.forEach((l, i) => {
    if (i > 0) {
      const prev = cols[i - 1];
      const svg = document.createElementNS(ns, 'svg');
      svg.setAttribute('class', 'link');
      svg.setAttribute('width', LINK);
      svg.setAttribute('height', tall * STEP);
      l.forEach((s, k) => prev.forEach((p, pk) => {
        if (!(s.blocked_by || []).includes(p.id)) return;
        const y1 = dotY(prev.length, pk), y2 = dotY(l.length, k);
        const path = document.createElementNS(ns, 'path');
        path.setAttribute('d', `M0,${y1} C${LINK / 2},${y1} ${LINK / 2},${y2} ${LINK},${y2}`);
        svg.append(path);
      }));
      parts.push(svg);
    }
    parts.push(h('div', { class: 'tcol' + (i === 0 ? ' first' : '') + (i === cols.length - 1 ? ' last' : ''), style: `height:${tall * STEP}px` },
      l.map((s) => withTip(h('div', { class: 'tstep', style: `height:${STEP}px` },
        h('div', { class: 'th' }, s.id),
        h('div', { class: 'rail' }, h('span', { class: 'dot' + (s.checkpoint ? ' cp' : '') + ' pending' })),
        h('div', { class: 'tg' }, s.guild)), () => stepCard(s)))));
  });
  return h('div', { class: 'track' }, parts);
}

// stairs places a Plan's steps for a Job lane: one column per step, in dependency order, and a row for each,
// so parallel steps sit at different heights and a step that waits on several is centred between them
// (a fan-in) while siblings spread evenly around the step they share (a fan-out). Rows may be fractional.
function stairs(steps) {
  const row = {};
  for (const l of layers(steps)) {
    const want = l.map((s) => {
      const bs = (s.blocked_by || []).filter((b) => row[b] != null).map((b) => row[b]);
      return bs.length ? bs.reduce((a, b) => a + b, 0) / bs.length : 0;
    });
    const idx = l.map((_, i) => i).sort((a, b) => want[a] - want[b]);
    const got = [];
    idx.forEach((i, n) => { got[n] = n && want[i] < got[n - 1] + 1 ? got[n - 1] + 1 : want[i]; });
    const shift = idx.reduce((a, i) => a + want[i], 0) / idx.length - got.reduce((a, b) => a + b, 0) / got.length;
    idx.forEach((i, n) => { row[l[i].id] = got[n] + shift; });
  }
  const top = Math.min(...Object.values(row));
  for (const k of Object.keys(row)) row[k] -= top;
  return row;
}

// stairTrack draws a Job's Plan as a staircase (see stairs): a dot per step coloured by status, joined by
// S-curves that run from each blocker into the step it blocks. A compact track is only the dots, for lanes
// under a jigHeader. Every step has a fixed size so the curves can be placed without measuring the page.
const LANE_STEP = 26; // px per step in a compact lane: just the dot
function stairTrack(steps, state, compact, columns, ctx) {
  const ord = layers(steps).flat();
  const ids = columns || ord.map((s) => s.id); // a shared header's columns; a lane without a step leaves its column empty
  const byId = Object.fromEntries(ord.map((s) => [s.id, s]));
  const row = stairs(steps);
  const unit = 64; // px per row
  const cell = compact ? LANE_STEP : STEP;
  const dotAt = compact ? LANE_STEP / 2 : DOT_Y;
  const H = Math.max(...Object.values(row)) * unit + cell;
  const col = Object.fromEntries(ids.map((id, i) => [id, i]));
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  // x runs 100 units per column, stretched to the lane's width, so the columns spread evenly
  svg.setAttribute('viewBox', `0 0 ${ids.length * 100} ${H}`);
  svg.setAttribute('preserveAspectRatio', 'none');
  svg.setAttribute('width', '100%');
  svg.setAttribute('height', H);
  for (const s of ord) for (const b of s.blocked_by || []) {
    if (col[b] == null || col[s.id] == null) continue;
    const x1 = col[b] * 100 + 50, x2 = col[s.id] * 100 + 50, mid = (x1 + x2) / 2;
    const y1 = row[b] * unit + dotAt, y2 = row[s.id] * unit + dotAt;
    const path = document.createElementNS(ns, 'path');
    path.setAttribute('d', `M${x1},${y1} C${mid},${y1} ${mid},${y2} ${x2},${y2}`);
    svg.append(path);
  }
  return h('div', { class: 'track' + (compact ? ' compact' : '') }, h('div', { class: 'sinner', style: `min-width:${ids.length * COLW}px;height:${H}px` }, svg,
    ids.map((id) => h('div', { class: 'scol', style: `min-width:${COLW}px;height:${H}px` }, byId[id] ? stairStep(byId[id], row[id] * unit, cell, state, compact, ctx) : null))));
}

function stairStep(s, top, cell, state, compact, ctx) {
  const cellEl = h('div', { class: 'sstep', style: `top:${top}px;height:${cell}px` },
    compact ? null : h('div', { class: 'th' }, s.id),
    h('div', { class: 'rail' }, h('span', { class: 'dot' + (s.checkpoint ? ' cp' : '') + ' ' + ((state && state[s.id]) || 'pending') })),
    compact ? null : h('div', { class: 'tg' }, s.guild));
  return withTip(cellEl, () => stepCard(s, state && state[s.id], ctx));
}

// sharedSteps merges several Plans' steps into the columns of one header: a step name keeps one column
// across Plans, and a step new to the header goes after its blockers and after the Plan's earlier steps,
// or at the end when it has neither.
function sharedSteps(plans) {
  const out = [];
  for (const steps of plans) {
    let cursor = -1; // the column of the Plan's latest step so far
    for (const s of layers(steps).flat()) {
      let at = out.findIndex((o) => o.id === s.id);
      if (at < 0) {
        const after = Math.max(cursor, ...(s.blocked_by || []).map((b) => out.findIndex((o) => o.id === b)));
        at = after < 0 ? out.length : after + 1;
        out.splice(at, 0, s);
      }
      cursor = at;
    }
  }
  return out;
}

// legend explains the dot colours.
function legend() {
  return h('span', { class: 'legend' }, ['done', 'running', 'blocked', 'pending'].map((k) => h('span', {}, h('span', { class: 'dot sw ' + k }), ' ' + STATUS_LABEL[k])));
}

// jigHeader is the row of column headers over a group's compact lanes: one column per step in ord, at the
// same widths as the lanes, so each name sits over its dots.
function jigHeader(ord) {
  return h('div', { class: 'lane lhrow' }, h('div', { class: 'lhead' }), h('div', { class: 'track' },
    ord.map((s) => h('div', { class: 'hcol', style: `min-width:${COLW}px` }, withTip(h('div', { class: 'hname' }, s.id, s.checkpoint ? h('span', { class: 'cpm' }, ' \u25C7') : null), () => stepCard(s))))));
}

function instructBox(jobId, placeholder) {
  const box = h('input', { type: 'text', placeholder });
  return h('div', { class: 'row' }, box,
    h('button', { onclick: () => {
      if (!box.value.trim()) return;
      act('Instruction sent to the Archwright', 'POST', `/jobs/${jobId}/instruct`, { text: box.value }, () => { box.value = ''; });
    } }, 'Send'));
}

// askPanel answers an ask through the same API path the Archwright's tools use; the first answer wins.
function askPanel(a, jobTitle) {
  const inputs = {};
  const qs = a.questions.map((q) => {
    let field;
    if (q.options && q.options.length) {
      const multi = q.multiple;
      const name = a.id + q.id;
      field = h('div', {}, q.options.map((o) => h('label', {},
        h('input', { type: multi ? 'checkbox' : 'radio', name, value: o, checked: o === q.default }), ' ', o, o === q.default ? h('span', { class: 'muted' }, ' (recommended)') : '')));
      inputs[q.id] = () => [...field.querySelectorAll('input:checked')].map((i) => i.value);
    } else {
      field = h('input', { type: 'text', value: q.default || '' });
      inputs[q.id] = () => (field.value.trim() ? [field.value.trim()] : []);
    }
    return h('div', { class: 'q' }, h('div', {}, h('strong', {}, q.text)), field);
  });
  const answers = () => a.questions.map((q) => {
    const values = inputs[q.id]();
    return values.length ? { question_id: q.id, values } : { question_id: q.id, skipped: true };
  });
  return h('div', { class: 'panel attn' },
    h('h3', {}, 'Ask · ', tag(a.kind), a.blocking ? tag('blocking') : null, ' ', jobLink(a.job_id, jobTitle), ' ', h('span', { class: 'muted' }, `${a.asker}${a.guild ? ' (' + a.guild + ')' : ''} ${ago(a.created_at)}`)),
    a.why ? h('p', { class: 'muted' }, a.why) : null, qs,
    h('div', { class: 'row' },
      h('button', { class: 'primary', onclick: () => act('Answer', 'POST', `/asks/${a.id}/answer`, { answers: answers() }) }, 'Answer'),
      h('button', { onclick: () => act('Accept defaults', 'POST', `/asks/${a.id}/accept-default`, {}) }, 'Accept recommended'),
      h('button', { onclick: () => act('Skip', 'POST', `/asks/${a.id}/skip`, {}) }, 'Skip'),
      h('button', { class: 'danger', onclick: () => confirm('Cancel this ask and its Job?') && act('Cancel', 'POST', `/asks/${a.id}/cancel`, {}) }, 'Cancel Job')));
}

// ---- run timeline -----------------------------------------------------------------
// A list of events beside the selected event's detail. The view is built once per run and then updated in place:
// a live refresh appends new events and refreshes the card, but never touches the selection, the detail, the
// filter or either pane's scroll position, because none of those nodes is replaced.
const ALL = 'All';
let tl = null; // the timeline on screen; a new one starts when the run changes

function newTimeline(runId) {
  const t = { runId, seen: 0, rows: new Map(), kinds: '', kind: ALL, sel: null, raw: new Map() };
  t.head = h('div', {});
  t.card = h('div', {});
  t.chips = h('div', { class: 'row', role: 'group', 'aria-label': 'Filter by kind' });
  t.empty = h('p', { class: 'muted', hidden: true }, 'No events recorded.');
  t.list = h('div', { class: 'tl-list', role: 'listbox', 'aria-label': 'Events' });
  t.detail = h('div', { class: 'tl-detail panel' }, h('p', { class: 'muted' }, 'Select an event.'));
  t.root = h('div', {}, t.head, t.card, t.chips, t.empty, h('div', { class: 'tl-split' }, t.list, t.detail));
  return t;
}

function runCard(runId, run, dispatch) {
  if (!run && !dispatch) return null;
  const secs = run ? Math.max(0, Math.round(((run.ended_at ? new Date(run.ended_at) : new Date()) - new Date(run.started_at)) / 1000)) : 0;
  const dur = secs >= 3600 ? Math.floor(secs / 3600) + 'h ' + Math.floor(secs % 3600 / 60) + 'm' : secs >= 60 ? Math.floor(secs / 60) + 'm ' + (secs % 60) + 's' : secs + 's';
  const f = (label, ...v) => h('div', { class: 'tl-f' }, h('div', { class: 'muted' }, label), h('div', {}, ...v));
  const guild = (run && run.guild) || (dispatch && dispatch.guild);
  return h('div', { class: 'panel tl-card' },
    f('Guild', h('strong', {}, guild || 'unknown')),
    f('Wright', h('strong', {}, dispatch ? dispatch.piece : 'unknown'), dispatch ? h('span', { class: 'muted' }, ' round ' + dispatch.round) : null),
    run ? f('Runner', run.runner || 'unknown') : null,
    run ? f('Status', tag(run.status)) : null,
    run ? f('Started', ago(run.started_at), h('span', { class: 'muted' }, ' \u00b7 ' + dur)) : null,
    run ? f('Usage', `${tokens(run.input_tokens)} in / ${tokens(run.output_tokens)} out \u00b7 ${money(run.cost_usd)}`) : null);
}

// rawEvent returns the node for an event's raw content, fetching it once and caching the outcome.
function rawEvent(t, e) {
  if (!e.has_raw) return h('div', { class: 'muted' }, 'raw content pruned');
  let r = t.raw.get(e.seq);
  if (!r) {
    r = { node: h('div', { class: 'muted' }, 'Loading raw event\u2026') };
    t.raw.set(e.seq, r);
    get(`/runs/${t.runId}/events/${e.seq}/raw`)
      .then((v) => r.node.replaceChildren(h('pre', {}, JSON.stringify(v, null, 2))))
      .catch((er) => { t.raw.delete(e.seq); r.node.replaceChildren(h('span', { class: 'bad' }, er.message)); })
      .finally(() => { r.node.className = ''; });
  }
  return r.node;
}

function selectEvent(t, e) {
  t.sel = e.seq;
  for (const [seq, row] of t.rows) row.setAttribute('aria-selected', seq === e.seq);
  t.detail.replaceChildren(h('div', { class: 'tl-dh' }, h('strong', {}, 'Event #' + e.seq), tag(e.kind)),
    h('p', { class: 'tl-full' }, e.summary), h('h3', {}, 'Raw event'), rawEvent(t, e));
  t.detail.scrollTop = 0;
}

function applyFilter(t) {
  for (const row of t.rows.values()) row.hidden = t.kind !== ALL && row.dataset.kind !== t.kind;
  for (const chip of t.chips.children) chip.setAttribute('aria-pressed', chip.dataset.kind === t.kind);
  for (const chip of t.chips.children) chip.className = chip.dataset.kind === t.kind ? 'primary' : '';
}

function drawChips(t, kinds) {
  const chip = (k) => h('button', { 'data-kind': k, onclick: () => { t.kind = k; applyFilter(t); } }, k);
  t.chips.replaceChildren(chip(ALL), ...kinds.map(chip));
}

function runTimeline(runId, jobId, evs, job, runs, dispatches) {
  if (!tl || tl.runId !== runId) tl = newTimeline(runId);
  const t = tl;
  const trail = job ? [[job.title, '#/job/' + jobId], 'Run ' + runId] : ['Run ' + runId];
  t.head.replaceChildren(crumbs(...trail), h('h1', {}, 'Run ', h('span', { class: 'mono' }, runId)),
    h('p', { class: 'muted' }, 'Read-only timeline. Raw vendor events load on selection.'));
  const run = (runs || []).find((r) => r.run_id === runId);
  const dispatch = run && (dispatches || []).find((d) => d.id === run.dispatch_id || d.run_id === runId);
  const card = jobId ? runCard(runId, run, dispatch) : null;
  t.card.replaceChildren(...(card ? [card] : []));

  for (const e of evs) {
    if (t.rows.has(e.seq)) continue; // appended once; an existing row is never rebuilt
    const pick = () => selectEvent(t, e);
    const row = h('div', { class: 'tl-row', role: 'option', tabindex: 0, 'aria-selected': false, 'data-kind': e.kind, onclick: pick,
      onkeydown: (k) => { if (k.key === 'Enter' || k.key === ' ') { k.preventDefault(); pick(); } } },
    h('span', { class: 'muted mono' }, '#' + e.seq), tag(e.kind), h('span', { class: 'tl-sum' }, e.summary));
    t.rows.set(e.seq, row);
    t.list.append(row);
  }
  t.empty.hidden = t.rows.size > 0;

  const kinds = [...new Set(evs.map((e) => e.kind))];
  if (t.kind !== ALL && !kinds.includes(t.kind)) t.kind = ALL; // its kind is gone: fall back to All
  if (kinds.join('\n') !== t.kinds) { t.kinds = kinds.join('\n'); drawChips(t, kinds); }
  applyFilter(t);
  return t.root;
}

// ---- needs-you inbox ----------------------------------------------------------------
// Items waiting on the user, grouped by Job, beside the selected item's action panel. Like the run timeline,
// the view is built once and updated in place: a live refresh adds, updates and removes rows, but never
// replaces the selection, the detail panel (and the form in it) or either pane, so scroll positions hold.
let ny = null;

function newInbox() {
  const n = { sel: null, rows: new Map(), groups: new Map(), panels: new Map() };
  n.empty = h('p', { class: 'muted', hidden: true }, 'Nothing is waiting on you.');
  n.list = h('div', { class: 'tl-list', role: 'listbox', 'aria-label': 'Waiting on you' });
  n.trail = h('div', { class: 'ny-trail' });
  n.panel = h('div', {});
  n.detail = h('div', { class: 'tl-detail panel' }, h('p', { class: 'muted' }, 'Select an item.'));
  n.split = h('div', { class: 'tl-split' }, n.list, n.detail);
  n.root = h('div', {}, crumbs('Needs you'), h('h1', {}, 'Needs you'), n.empty, n.split);
  return n;
}

// resolveOrigins says where each entry came from: its Job, the Guild and the Wright (a piece's dispatch and
// run). A Plan approval comes from the Archwright's planning. An item with no dispatch of its own gets the
// Job's latest one, marked as a guess.
async function resolveOrigins(entries, byId) {
  const ctx = {};
  await Promise.all([...new Set(entries.filter((e) => e.jobId && !e.byArchwright).map((e) => e.jobId))].map(async (id) => {
    const [ds, runs] = await Promise.all([get(`/jobs/${id}/dispatches`).catch(() => []), get(`/jobs/${id}/runs`).catch(() => [])]);
    ctx[id] = { ds, runs };
  }));
  return entries.map((e) => {
    const { ds, runs } = ctx[e.jobId] || { ds: [], runs: [] };
    const byRound = (a, b) => b.round - a.round;
    let d = e.dispatchId ? ds.find((x) => x.id === e.dispatchId) : null;
    if (!d && e.piece) d = ds.filter((x) => x.piece === e.piece && (!e.guild || x.guild === e.guild)).sort(byRound)[0];
    const guess = !d && !e.piece && !e.guild && e.latest && !e.byArchwright;
    if (guess) d = [...ds].sort((a, b) => new Date(b.created_at) - new Date(a.created_at))[0];
    const run = d && (runs.find((r) => r.dispatch_id === d.id) || runs.find((r) => r.run_id === d.run_id));
    const job = byId[e.jobId];
    return { ...e, jobTitle: job ? job.title : e.jobId || 'No Job', jobState: job && job.state,
      guild: e.guild || (d && d.guild) || '', piece: e.piece || (d && d.piece) || '', round: d && d.round,
      runner: run && run.runner, runId: (run && run.run_id) || (d && d.run_id) || '', guess };
  });
}

const wrightLabel = (r) => r.piece + (r.round ? ' r' + r.round : '');

// originTrail is Job › Guild › Wright, each a link where it has a page.
function originTrail(r) {
  const sep = () => h('span', { class: 'muted' }, ' › ');
  const parts = [r.jobId ? jobLink(r.jobId, r.jobTitle) : h('span', { class: 'muted' }, 'No Job')];
  if (r.byArchwright) parts.push(h('span', { class: 'muted' }, 'Archwright (planning)'));
  else {
    parts.push(r.guild ? h('strong', {}, r.guild) : h('span', { class: 'muted' }, 'no Guild'));
    const text = r.piece ? wrightLabel(r) + (r.runner ? ' · ' + r.runner : '') + (r.guess ? ' (latest)' : '') : '';
    parts.push(!text ? h('span', { class: 'muted' }, 'no Wright') : r.runId ? h('a', { href: `#/run/${r.runId}/${r.jobId}` }, text) : h('span', {}, text));
  }
  return parts.flatMap((p, i) => (i ? [sep(), p] : [p]));
}

function selectItem(n, key) {
  n.sel = key;
  for (const [k, row] of n.rows) row.setAttribute('aria-selected', k === key);
  const r = n.items.get(key);
  n.detail.replaceChildren(n.trail, n.panel);
  n.trail.replaceChildren(...originTrail(r));
  n.panel.replaceChildren(h('p', { class: 'muted' }, 'Loading…'));
  n.detail.scrollTop = 0;
  Promise.resolve(n.panels.get(key) || r.build()).then((node) => {
    n.panels.set(key, node);
    if (n.sel === key) n.panel.replaceChildren(node);
  }).catch((e) => { if (n.sel === key) n.panel.replaceChildren(h('span', { class: 'bad' }, e.message)); });
}

function clearSelection(n) {
  n.sel = null;
  for (const row of n.rows.values()) row.setAttribute('aria-selected', false);
  n.detail.replaceChildren(h('p', { class: 'muted' }, 'Select an item.'));
}

function inboxRow(n, r) {
  const pick = () => selectItem(n, r.key);
  return h('div', { class: 'tl-row ny-row', role: 'option', tabindex: 0, 'aria-selected': false, onclick: pick,
    onkeydown: (k) => { if (k.key === 'Enter' || k.key === ' ') { k.preventDefault(); pick(); } } });
}

function inboxGroup(r) {
  const g = { rows: h('div', {}), state: h('span', {}), count: h('span', { class: 'muted' }) };
  g.title = h('strong', { class: 'ny-title' });
  g.el = h('div', { class: 'ny-group' }, h('div', { class: 'ny-head' }, g.title, g.state, g.count), g.rows);
  return g;
}

// updateInbox reconciles the list with the items now waiting; nodes that persist are updated, not replaced.
async function updateInbox(n, entries) {
  n.items = new Map(entries.map((r) => [r.key, r]));
  for (const [k, row] of n.rows) if (!n.items.has(k)) { row.remove(); n.rows.delete(k); n.panels.delete(k); }
  if (n.sel && !n.items.has(n.sel)) clearSelection(n);

  const jobs = new Map();
  for (const r of entries) jobs.set(r.jobId, [...(jobs.get(r.jobId) || []), r]);
  for (const [id, g] of n.groups) if (!jobs.has(id)) { g.el.remove(); n.groups.delete(id); }
  for (const [id, mine] of jobs) {
    let g = n.groups.get(id);
    if (!g) { g = inboxGroup(mine[0]); n.groups.set(id, g); n.list.append(g.el); }
    g.title.textContent = mine[0].jobTitle;
    g.state.className = mine[0].jobState ? 'tag ' + mine[0].jobState : '';
    g.state.textContent = mine[0].jobState || '';
    g.count.textContent = mine.length + ' waiting';
    for (const r of mine) {
      let row = n.rows.get(r.key);
      if (!row) { row = inboxRow(n, r); n.rows.set(r.key, row); g.rows.append(row); }
      row.replaceChildren(tag(r.kind), h('span', { class: 'muted' }, ago(r.at)),
        h('span', { class: 'ny-where' }, r.byArchwright ? 'Archwright (planning)' : (r.guild || 'no Guild') + (r.piece ? ' › ' + wrightLabel(r) : '')));
    }
  }
  if (n.sel) n.trail.replaceChildren(...originTrail(n.items.get(n.sel)));
  n.empty.hidden = entries.length > 0;
  n.split.hidden = entries.length === 0;
}

// ---- routing, rendering and live updates -------------------------------------------
const NAV = [['#/board', 'Jobs'], ['#/needs-you', 'Needs you'], ['#/runner', 'Runner'], ['#/usage', 'Usage'], ['#/catalogue', 'Catalogue']];

function route() {
  const [, name, arg] = location.hash.split('/');
  switch (name) {
    case 'needs-you': return ['needsYou'];
    case 'job': return ['job', arg];
    case 'run': return ['run', arg];
    case 'runner': return ['runner'];
    case 'usage': return ['usage'];
    case 'catalogue': return ['catalogue'];
    default: return ['board'];
  }
}

let rendering = false;
let again = false;
async function refresh() {
  if (rendering) { again = true; return; }
  rendering = true;
  const [name, arg] = route();
  try {
    if (!token) throw new Error('No token. Open the dashboard with the URL printed by `archwright dashboard`.');
    // A form being filled must not be lost to a live refresh: skip while a field in the view has focus.
    const el = document.activeElement;
    const editing = el && ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName) && $('main').contains(el) && el.type !== 'button';
    if (!editing) {
      const node = await views[name](arg);
      if (node.innerHTML !== $('main').firstElementChild?.innerHTML || $('main').childElementCount !== 1) { $('main').replaceChildren(node); retip(); };
    }
  } catch (e) {
    $('main').replaceChildren(h('p', { class: 'bad' }, e.message));
  }
  rendering = false;
  if (again) { again = false; refresh(); }
  renderNav();
}

let badge = 0;
async function renderNav() {
  try { if (token) { const [items, board] = await Promise.all([get('/needs-you'), get('/board')]); badge = items.filter((it) => it.kind !== 'ask').length + board.filter((j) => j.state === 'needs-you' || j.state === 'parked').length; } } catch { /* shown by the view */ }
  const cur = '#/' + (location.hash.split('/')[1] || 'board');
  const nav = h('nav', {}, ...NAV.map(([href, label]) => h('a', { href, class: href === cur ? 'on' : '' }, label,
    href === '#/needs-you' && badge ? h('span', { class: 'badge' }, badge) : null)));
  if (nav.innerHTML !== $('nav').innerHTML) $('nav').replaceChildren(...nav.childNodes);
}

let timer;
function schedule() { clearTimeout(timer); timer = setTimeout(refresh, 250); }

// connect follows the event stream with fetch, so the token travels in a header and every
// event type, named or not, triggers a refresh. It resumes from the last id it saw.
async function connect() {
  if (!token) return;
  let last = 'latest';
  for (;;) {
    try {
      const res = await fetch('/events/stream?after=' + last, { headers: { Authorization: 'Bearer ' + token } });
      if (!res.ok || !res.body) throw new Error('stream ' + res.status);
      $('live').className = 'live on';
      $('live').textContent = '● live';
      schedule();
      const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
      let buf = '';
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += value;
        let i;
        while ((i = buf.indexOf('\n\n')) >= 0) {
          const frame = buf.slice(0, i);
          buf = buf.slice(i + 2);
          const id = /^id: (\d+)$/m.exec(frame);
          if (id) { last = id[1]; schedule(); }
        }
      }
    } catch { /* reconnect below */ }
    $('live').className = 'live off';
    $('live').textContent = '● reconnecting';
    await new Promise((r) => setTimeout(r, 2000));
  }
}

window.addEventListener('hashchange', refresh);
connect();
renderNav();
refresh();
// A safety net for event types not listed above, and for relative times.
setInterval(refresh, 5000);
