// PROTOTYPE (throwaway): variants of the run timeline view, switched by ?variant=A|B|C.
// "What should the Job's Wright-run timeline look like, with a list/detail option whose
// selection survives live re-renders?" Delete this file, proto-run.css, and the hook in app.js's run().
'use strict';

const PROTO = { run: null, sel: null, open: new Set(), kind: null, raw: new Map(), listTop: 0, detTop: 0 };
const PROTO_VARIANTS = [['A', 'Current: stacked panels'], ['B', 'List + detail'], ['C', 'Inline log + filters'], ['D', 'List + detail + filters']];
const protoVariant = () => new URLSearchParams(location.search).get('variant') || 'A';

// Everything the user has chosen lives here, keyed by run, so a re-render rebuilds the same view.
function protoState(runId) {
  if (PROTO.run !== runId) Object.assign(PROTO, { run: runId, sel: null, open: new Set(), kind: null, raw: new Map(), listTop: 0, detTop: 0 });
  return PROTO;
}

function protoRaw(runId, e) {
  if (!e.has_raw) return h('div', { class: 'muted' }, 'raw content pruned');
  const key = e.seq;
  let r = PROTO.raw.get(key);
  if (!r) {
    r = { s: 'loading' };
    PROTO.raw.set(key, r);
    get(`/runs/${runId}/events/${e.seq}/raw`)
      .then((v) => { r.s = 'ok'; r.text = JSON.stringify(v, null, 2); })
      .catch((er) => { r.s = 'err'; r.text = er.message; })
      .finally(schedule);
  }
  if (r.s === 'loading') return h('div', { class: 'muted' }, 'Loading raw event…');
  return h('pre', { class: r.s === 'err' ? 'bad' : '' }, r.text);
}

function protoScroll(el, key) {
  el.addEventListener('scroll', () => { PROTO[key] = el.scrollTop; });
  requestAnimationFrame(() => { el.scrollTop = PROTO[key]; });
  return el;
}

function protoChips(st, kinds) {
  if (st.kind && !kinds.includes(st.kind)) st.kind = null;
  const chip = (label, k) => h('button', { class: st.kind === k ? 'primary' : '', onclick: () => { st.kind = k; refresh(); } }, label);
  return h('div', { class: 'row' }, chip('All', null), kinds.map((k) => chip(k, k)));
}

// protoCard says who is running this: the Guild, the Wright's runner and piece, and what the run has used.
function protoCard(meta) {
  const r = meta && meta.run;
  const d = meta && meta.dispatch;
  if (!r && !d) return null;
  const guild = (r && r.guild) || (d && d.guild);
  const secs = r ? Math.round(((r.ended_at ? new Date(r.ended_at) : new Date()) - new Date(r.started_at)) / 1000) : 0;
  const dur = secs >= 3600 ? Math.floor(secs / 3600) + 'h ' + Math.floor(secs % 3600 / 60) + 'm' : secs >= 60 ? Math.floor(secs / 60) + 'm ' + (secs % 60) + 's' : secs + 's';
  const f = (label, ...v) => h('div', { class: 'pc-f' }, h('div', { class: 'muted' }, label), h('div', {}, ...v));
  return h('div', { class: 'panel pc' },
    f('Guild', h('strong', {}, guild || 'unknown')),
    f('Wright', h('strong', {}, d ? d.piece : 'unknown'), d ? h('span', { class: 'muted' }, ' round ' + d.round) : null),
    r ? f('Runner', r.runner || 'unknown') : null,
    r ? f('Status', tag(r.status)) : null,
    r ? f('Started', ago(r.started_at), h('span', { class: 'muted' }, ' \u00b7 ' + dur)) : null,
    r ? f('Usage', `${tokens(r.input_tokens)} in / ${tokens(r.output_tokens)} out \u00b7 ${money(r.cost_usd)}`) : null);
}

// B: list on the left, detail on the right. Selection is by seq and only a click changes it.
function protoListDetail(runId, all, filtered, meta) {
  const st = protoState(runId);
  const evs = filtered ? all.filter((e) => !st.kind || e.kind === st.kind) : all;
  const cur = all.find((e) => e.seq === st.sel);
  const list = protoScroll(h('div', { class: 'pl-list' }, evs.map((e) =>
    h('div', { class: 'pl-row' + (e.seq === st.sel ? ' on' : ''), tabindex: 0, onclick: () => { st.sel = e.seq; st.detTop = 0; refresh(); },
      onkeydown: (k) => { if (k.key === 'Enter' || k.key === ' ') { k.preventDefault(); st.sel = e.seq; st.detTop = 0; refresh(); } } },
    h('span', { class: 'muted mono' }, '#' + e.seq), tag(e.kind), h('span', { class: 'pl-sum' }, e.summary)))), 'listTop');
  const det = protoScroll(h('div', { class: 'pl-detail panel' }, cur
    ? [h('div', { class: 'pl-dh' }, h('strong', {}, 'Event #' + cur.seq), tag(cur.kind)),
      h('p', { class: 'pl-full' }, cur.summary), h('h3', {}, 'Raw event'), protoRaw(runId, cur)]
    : h('p', { class: 'muted' }, 'Select an event.')), 'detTop');
  const split = h('div', { class: 'pl-split' + (filtered ? ' filt' : '') }, list, det);
  return filtered ? h('div', {}, protoCard(meta), protoChips(st, [...new Set(all.map((e) => e.kind))]), split) : split;
}

// C: one-line rows that expand in place; any number open; kind filter chips.
function protoInline(runId, evs) {
  const st = protoState(runId);
  const kinds = [...new Set(evs.map((e) => e.kind))];
  const chips = protoChips(st, kinds);
  const rows = evs.filter((e) => !st.kind || e.kind === st.kind).map((e) => {
    const open = st.open.has(e.seq);
    return h('div', { class: 'pi-item' },
      h('div', { class: 'pi-row', tabindex: 0, onclick: () => { open ? st.open.delete(e.seq) : st.open.add(e.seq); refresh(); },
        onkeydown: (k) => { if (k.key === 'Enter' || k.key === ' ') { k.preventDefault(); open ? st.open.delete(e.seq) : st.open.add(e.seq); refresh(); } } },
      h('span', { class: 'pi-car' }, open ? '▾' : '▸'), h('span', { class: 'muted mono' }, '#' + e.seq), tag(e.kind), h('span', { class: 'pi-sum' }, e.summary)),
      open ? h('div', { class: 'pi-body' }, h('p', { class: 'pl-full' }, e.summary), protoRaw(runId, e)) : null);
  });
  return h('div', {}, chips, h('div', { class: 'pi-log' }, rows));
}

// protoRun returns the variant's body, or null for A (the current rendering).
function protoRun(runId, evs, meta) {
  protoState(runId);
  const v = protoVariant();
  if (v === 'B') return protoListDetail(runId, evs, false);
  if (v === 'D') return protoListDetail(runId, evs, true, meta);
  if (v === 'C') return protoInline(runId, evs);
  return null;
}

// The floating bar: arrows and ←/→ cycle; the choice lives in the URL so reloads keep it.
function protoBar() {
  const bar = document.body.appendChild(h('div', { id: 'proto-bar' }));
  const set = (i) => {
    const n = PROTO_VARIANTS.length;
    const u = new URL(location.href);
    u.searchParams.set('variant', PROTO_VARIANTS[(i + n) % n][0]);
    history.replaceState(null, '', u);
    draw();
    refresh();
  };
  const idx = () => Math.max(0, PROTO_VARIANTS.findIndex(([k]) => k === protoVariant()));
  function draw() {
    bar.className = location.hash.startsWith('#/run/') ? 'on' : '';
    const [k, name] = PROTO_VARIANTS[idx()];
    bar.replaceChildren(h('button', { onclick: () => set(idx() - 1) }, '←'), h('span', {}, `${k} (${name})`), h('button', { onclick: () => set(idx() + 1) }, '→'));
  }
  window.addEventListener('hashchange', draw);
  window.addEventListener('keydown', (e) => {
    if (!bar.className || e.target.closest('input,textarea,select,[contenteditable]')) return;
    if (e.key === 'ArrowLeft') set(idx() - 1); else if (e.key === 'ArrowRight') set(idx() + 1);
  });
  draw();
}
protoBar();
