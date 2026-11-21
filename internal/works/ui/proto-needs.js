// PROTOTYPE (throwaway): variants of the "Needs you" page, switched by ?variant=A|B|C|D.
// "How should each item say which Job, Guild and Wright it came from?" Delete this file,
// proto-needs.css, and the hooks in app.js's needsYou() and index.html.
'use strict';

const PN_VARIANTS = [['A', 'Current'], ['B', 'Origin strip'], ['C', 'Grouped by Job'], ['D', 'Inbox + detail'], ['E', 'Grouped inbox + detail']];
const pnVariant = () => new URLSearchParams(location.search).get('variant') || 'A';
const PN = { sel: null, listTop: 0, detTop: 0 };

// pnResolve fills in where each item came from: its Job, the Guild, and the Wright (the piece's
// dispatch and run). Plan approvals come from the Archwright; parked Jobs show their latest Wright.
async function pnResolve(entries, board) {
  const byJob = {};
  await Promise.all([...new Set(entries.map((e) => e.jobId).filter(Boolean))].map(async (id) => {
    const [ds, runs] = await Promise.all([get(`/jobs/${id}/dispatches`).catch(() => []), get(`/jobs/${id}/runs`).catch(() => [])]);
    byJob[id] = { ds, runs };
  }));
  return entries.map((e) => {
    const { ds = [], runs = [] } = byJob[e.jobId] || {};
    const mine = ds.filter((d) => (e.dispatchId ? d.id === e.dispatchId : e.piece ? d.piece === e.piece && (!e.guild || d.guild === e.guild) : false));
    let d = mine.sort((a, b) => b.round - a.round)[0];
    let last = false;
    if (!d && (e.kind === 'parked' || (!e.piece && e.kind !== 'plan' && e.kind !== 'ask'))) { d = [...ds].sort((a, b) => new Date(b.created_at) - new Date(a.created_at))[0]; last = !!d; }
    const run = d ? runs.find((r) => r.dispatch_id === d.id) || runs.find((r) => r.run_id === d.run_id) : null;
    const job = board.find((j) => j.id === e.jobId);
    return { ...e, job, jobTitle: job ? job.title : e.jobId, site: job && job.site,
      guild: e.guild || (d && d.guild) || '', piece: e.piece || (d && d.piece) || '', round: d && d.round, runner: run && run.runner, runId: run ? run.run_id : d && d.run_id, last,
      byArchwright: e.kind === 'plan' };
  });
}

// The three facts as one line of links: Job › Guild › Wright.
function pnTrail(r) {
  const parts = [r.jobId ? h('a', { href: '#/job/' + r.jobId }, r.jobTitle) : h('span', { class: 'muted' }, 'no Job')];
  if (r.byArchwright) parts.push(h('span', { class: 'muted' }, 'Archwright (planning)'));
  else {
    parts.push(r.guild ? h('strong', {}, r.guild) : h('span', { class: 'muted' }, 'no Guild'));
    const w = r.piece ? [r.piece, r.round ? ' r' + r.round : '', r.runner ? ' · ' + r.runner : '', r.last ? ' (latest)' : ''].join('') : '';
    parts.push(w ? (r.runId ? h('a', { href: `#/run/${r.runId}/${r.jobId}` }, w) : h('span', {}, w)) : h('span', { class: 'muted' }, 'no Wright'));
  }
  return h('div', { class: 'pn-trail' }, parts.flatMap((p, i) => (i ? [h('span', { class: 'muted' }, ' › '), p] : [p])));
}

// A: the page as it is. B: the trail sits over every panel.
function pnStrip(rs) { return rs.map((r) => h('div', { class: 'pn-wrap' }, pnTrail(r), r.node)); }

// C: one block per Job, its items beneath, each with a Guild / Wright chip.
function pnGrouped(rs) {
  const jobs = [...new Set(rs.map((r) => r.jobId))];
  return jobs.map((id) => {
    const mine = rs.filter((r) => r.jobId === id);
    const f = mine[0];
    return h('section', { class: 'pn-job' },
      h('div', { class: 'pn-jh' }, id ? h('a', { href: '#/job/' + id }, h('strong', {}, f.jobTitle)) : h('strong', {}, 'No Job'), f.job ? tag(f.job.state) : null,
        h('span', { class: 'muted' }, [f.site, id, f.job ? money(f.job.cost_usd) : ''].filter(Boolean).join(' · ')), h('span', { class: 'muted pn-n' }, mine.length + ' waiting')),
      mine.map((r) => h('div', { class: 'pn-item' },
        h('div', { class: 'pn-chips' }, r.byArchwright ? h('span', { class: 'tag' }, 'Archwright') : [r.guild ? h('span', { class: 'tag' }, 'Guild ' + r.guild) : null,
          r.piece ? h('span', { class: 'tag' }, 'Wright ' + r.piece + (r.round ? ' r' + r.round : '')) : null, r.runId ? h('a', { href: `#/run/${r.runId}/${r.jobId}` }, 'run timeline') : null]),
        r.node)));
  });
}

// D: an inbox. Each row says kind, Job, Guild and Wright; the selected item's panel opens beside it.
// The selection is kept here, so a live refresh rebuilds the same view.
function pnInbox(rs, grouped) {
  if (PN.sel && !rs.some((r) => r.key === PN.sel)) PN.sel = null;
  const pick = (k) => { PN.sel = k; PN.detTop = 0; refresh(); };
  const keep = (el, key) => { el.addEventListener('scroll', () => { PN[key] = el.scrollTop; }); requestAnimationFrame(() => { el.scrollTop = PN[key]; }); return el; };
  const cur = rs.find((r) => r.key === PN.sel);
  const row = (r) => h('div', { class: 'pn-row' + (r.key === PN.sel ? ' on' : ''), tabindex: 0, onclick: () => pick(r.key),
    onkeydown: (k) => { if (k.key === 'Enter' || k.key === ' ') { k.preventDefault(); pick(r.key); } } },
    h('div', {}, tag(r.kind), h('span', { class: 'muted' }, ago(r.at))),
    grouped ? null : h('div', { class: 'pn-rt' }, r.jobTitle),
    h('div', { class: 'muted pn-rs' }, r.byArchwright ? 'Archwright (planning)' : [r.guild || 'no Guild', r.piece ? ' \u203A ' + r.piece + (r.round ? ' r' + r.round : '') : ''].join('')));
  const jobs = [...new Set(rs.map((r) => r.jobId))];
  const body = grouped ? jobs.map((id) => {
    const mine = rs.filter((r) => r.jobId === id);
    return h('div', { class: 'pn-g' }, h('div', { class: 'pn-gh' }, h('strong', { class: 'pn-rt' }, mine[0].jobTitle), mine[0].job ? tag(mine[0].job.state) : null,
      h('span', { class: 'muted pn-n' }, mine.length)), mine.map(row));
  }) : rs.map(row);
  const list = keep(h('div', { class: 'pn-list' }, body), 'listTop');
  const det = keep(h('div', { class: 'pn-det' }, cur ? [pnTrail(cur), cur.node] : h('p', { class: 'muted' }, 'Select an item.')), 'detTop');
  return h('div', { class: 'pn-split' }, list, det);
}

async function protoNeeds(entries, board) {
  const v = pnVariant();
  if (v === 'A' || !entries.length) return null;
  const rs = await pnResolve(entries, board);
  if (v === 'B') return h('div', {}, pnStrip(rs));
  if (v === 'C') return h('div', {}, pnGrouped(rs));
  return pnInbox(rs, v === 'E');
}

function pnBar() {
  const bar = document.body.appendChild(h('div', { id: 'proto-bar' }));
  const idx = () => Math.max(0, PN_VARIANTS.findIndex(([k]) => k === pnVariant()));
  const set = (i) => {
    const u = new URL(location.href);
    u.searchParams.set('variant', PN_VARIANTS[(i + PN_VARIANTS.length) % PN_VARIANTS.length][0]);
    history.replaceState(null, '', u);
    draw();
    refresh();
  };
  function draw() {
    bar.className = location.hash.startsWith('#/needs-you') ? 'on' : '';
    const [k, name] = PN_VARIANTS[idx()];
    bar.replaceChildren(h('button', { onclick: () => set(idx() - 1) }, '←'), h('span', {}, `${k} (${name})`), h('button', { onclick: () => set(idx() + 1) }, '→'));
  }
  window.addEventListener('hashchange', draw);
  window.addEventListener('keydown', (e) => {
    if (!bar.className || e.target.closest('input,textarea,select,[contenteditable]')) return;
    if (e.key === 'ArrowLeft') set(idx() - 1); else if (e.key === 'ArrowRight') set(idx() + 1);
  });
  draw();
}
pnBar();
