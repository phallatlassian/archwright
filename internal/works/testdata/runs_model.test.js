// Tests of the dashboard's run list and transcript model (ui/runs.js), run by ui_runs_test.go with node.
// Usage: node runs_model.test.js path/to/runs.js
'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const code = fs.readFileSync(process.argv[2], 'utf8');
const m = vm.runInNewContext(code + '\n;({ fmtDur, runRows, runSpan, barLayout, axisTicks, transcriptItems, loadRaws })', {});

// The code under test runs in its own realm, so its arrays are not this realm's: compare them as plain JSON.
const eq = (actual, want) => assert.deepEqual(JSON.parse(JSON.stringify(actual)), want);

const at = (min, sec = 0) => new Date(Date.UTC(2026, 0, 1, 0, min, sec)).toISOString();
const run = (id, o) => ({ run_id: id, job_id: 'j', guild: 'masonry', runner: 'claude', input_tokens: 0, output_tokens: 0, cost_usd: 0, status: 'done', ...o });

const tests = {
  'durations read in s, m and h'() {
    eq([0, 45000, 330000, 9900000, 39660000, 3599600].map(m.fmtDur), ['0s', '45s', '5m30s', '2h45m', '11h01m', '1h00m']);
    assert.equal(m.fmtDur(null), '');
  },

  'the axis ticks read like 0s 2h45m 5h31m 8h16m 11h01m'() {
    eq(m.axisTicks(39660000), ['0s', '2h45m', '5h31m', '8h16m', '11h01m']);
  },

  'rows come in start order, numbered, named by step and piece'() {
    const runs = [
      run('r-late', { dispatch_id: 'd2', started_at: at(30), ended_at: at(40), parent_run: 'w-1' }),
      run('w-1', { step: 'implement', started_at: at(0), ended_at: at(50) }),
      run('r-early', { dispatch_id: 'd1', started_at: at(10), ended_at: at(20), parent_run: 'w-1' }),
    ];
    const rows = m.runRows(runs, [{ id: 'd1', piece: 'schema' }, { id: 'd2', piece: 'api' }], Date.parse(at(60)));
    eq(rows.map((r) => [r.n, r.run.run_id, r.kind, r.name, r.indent]), [
      [1, 'w-1', 'warden', 'implement', false], [2, 'r-early', 'wright', 'schema', true], [3, 'r-late', 'wright', 'api', true]]);
  },

  'only a recorded parent_run indents a Wright, and only a recorded step names a Warden'() {
    const runs = [
      run('w-1', { started_at: at(0), ended_at: at(50) }), // before the migration: no step
      run('w-2', { step: 'review', started_at: at(1), ended_at: at(50) }),
      run('r-1', { dispatch_id: 'd1', started_at: at(10), ended_at: at(20) }), // same Guild, inside a Warden's run, but no parent_run
      run('r-2', { dispatch_id: 'd2', started_at: at(12), ended_at: at(20), parent_run: 'w-gone' }), // its Warden is not among the runs
      run('r-3', { dispatch_id: 'd3', started_at: at(14), ended_at: at(20), parent_run: 'w-2' }),
    ];
    const rows = m.runRows(runs, [{ id: 'd1', piece: 'a' }, { id: 'd2', piece: 'b' }, { id: 'd3', piece: 'c' }], Date.parse(at(60)));
    eq(rows.map((r) => [r.run.run_id, r.name, r.indent]), [
      ['w-1', '', false], ['w-2', 'review', false], ['r-1', 'a', false], ['r-2', 'b', false], ['r-3', 'c', true]]);
  },

  'a running run ends now, and the axis runs to the latest end'() {
    const now = Date.parse(at(90));
    const rows = m.runRows([run('a', { started_at: at(0), ended_at: at(10) }), run('b', { started_at: at(30), status: 'running' })], [], now);
    assert.equal(rows[1].end, now);
    assert.equal(rows[1].ms, 60 * 60000);
    eq(m.runSpan(rows), { t0: Date.parse(at(0)), span: 90 * 60000 });
    eq(m.runSpan([]), { t0: 0, span: 1 });
  },

  'a bar has a minimum width, and a late bar puts its label on the left'() {
    const rows = m.runRows([run('a', { started_at: at(0), ended_at: at(0, 1) }), run('b', { started_at: at(80), ended_at: at(100) }), run('c', { started_at: at(50), ended_at: at(52) })], [], Date.parse(at(120)));
    const { t0, span } = m.runSpan(rows);
    const [a, c, b] = rows.map((r) => m.barLayout(r, t0, span));
    assert.ok(a.width >= 0.7 && !a.labelLeft);
    assert.ok(b.labelLeft && Math.abs(b.left - 80) < 1e-9);
    assert.ok(!c.labelLeft);
  },

  'a transcript pairs each tool call with its output and reads the run as a conversation'() {
    const evs = [
      { seq: 1, kind: 'posture', summary: 'claude 2.1: default mode', has_raw: true, at: at(0) },
      { seq: 2, kind: 'started', summary: 'session started', has_raw: true, at: at(0) },
      { seq: 3, kind: 'other', summary: '', has_raw: true, at: at(0) },
      { seq: 4, kind: 'message', summary: 'Listing the directory.', has_raw: true, at: at(1) },
      { seq: 5, kind: 'tool_use', summary: 'Bash: ls', has_raw: true, at: at(1) },
      { seq: 6, kind: 'tool_use', summary: 'Bash: false', has_raw: true, at: at(1) },
      { seq: 7, kind: 'tool_result', summary: 'a.go', has_raw: true, at: at(2) },
      { seq: 8, kind: 'tool_result', summary: 'error: boom', has_raw: true, at: at(2) },
      { seq: 9, kind: 'result', summary: 'Done.', has_raw: true, at: at(3) },
    ];
    const assistant = { type: 'assistant', message: { content: [{ type: 'text', text: 'Listing the directory.\nThen more.' },
      { type: 'tool_use', id: 'u1', name: 'Bash', input: { command: 'ls\nsecond line' } }, { type: 'tool_use', id: 'u2', name: 'Bash', input: { command: 'false' } }] } };
    const user = { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'u2', content: 'boom\nmore', is_error: true }, { type: 'tool_result', tool_use_id: 'u1', content: [{ type: 'text', text: 'a.go\nb.go' }] }] } };
    const raws = new Map([[4, assistant], [5, assistant], [6, assistant], [7, user], [8, user], [9, { type: 'result', result: 'Done, fully.' }]]);
    const { items, fromSummary } = m.transcriptItems(evs, raws);
    assert.equal(fromSummary, 0);
    eq(items.map((i) => i.type), ['note', 'note', 'message', 'tool', 'tool', 'result']); // the empty vendor event is hidden
    assert.equal(items[2].text, 'Listing the directory.\nThen more.');
    eq([items[3].name, items[3].command, items[3].output, items[3].failed], ['Bash', 'ls\nsecond line', 'a.go\nb.go', false]);
    eq([items[4].command, items[4].output, items[4].failed], ['false', 'boom\nmore', true]); // paired by tool_use_id, not by order
    assert.equal(items[5].text, 'Done, fully.');
  },

  'a transcript whose raw content was pruned reads from the summaries'() {
    const evs = [
      { seq: 1, kind: 'message', summary: 'Reading the task', has_raw: false, at: at(0) },
      { seq: 2, kind: 'tool_use', summary: 'Bash: go test ./...', has_raw: false, at: at(1) },
      { seq: 3, kind: 'tool_result', summary: 'error: FAIL', has_raw: false, at: at(2) },
      { seq: 4, kind: 'result', summary: 'Done', has_raw: false, at: at(3) },
    ];
    const { items, fromSummary } = m.transcriptItems(evs, new Map());
    assert.equal(fromSummary, 4);
    eq(items.map((i) => i.type), ['message', 'tool', 'tool', 'result']); // no pairing without ids: the result stands alone
    eq([items[1].name, items[1].command], ['Bash', 'go test ./...']);
    eq([items[2].output, items[2].failed], ['FAIL', true]);
  },

  'a codex transcript pairs a command with its output by item id'() {
    const evs = [
      { seq: 1, kind: 'tool_use', summary: 'shell: /bin/zsh -lc ls', has_raw: true, at: at(0) },
      { seq: 2, kind: 'tool_result', summary: 'a.go', has_raw: true, at: at(1) },
      { seq: 3, kind: 'message', summary: 'Two files.', has_raw: true, at: at(2) },
    ];
    const raws = new Map([
      [1, { type: 'item.started', item: { id: 'i2', type: 'command_execution', command: '/bin/zsh -lc ls', status: 'in_progress' } }],
      [2, { type: 'item.completed', item: { id: 'i2', type: 'command_execution', command: '/bin/zsh -lc ls', aggregated_output: 'a.go\nb.go\n', exit_code: 0, status: 'completed' } }],
      [3, { type: 'item.completed', item: { id: 'i4', type: 'agent_message', text: 'Two files, both Go.' } }]]);
    const { items } = m.transcriptItems(evs, raws);
    eq(items.map((i) => i.type), ['tool', 'message']);
    eq([items[0].name, items[0].output, items[0].failed], ['shell', 'a.go\nb.go\n', false]);
    assert.equal(items[1].text, 'Two files, both Go.');
  },

  async 'raw events load a few at a time, and a failed one is counted, not fatal'() {
    const evs = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10].map((seq) => ({ seq, kind: seq === 5 ? 'posture' : 'message', has_raw: seq !== 7, summary: 's' + seq }));
    let live = 0, peak = 0;
    const { raws, failed } = await m.loadRaws('r', evs, async (seq) => {
      peak = Math.max(peak, ++live);
      await new Promise((r) => setTimeout(r, 2));
      live--;
      if (seq === 3) throw new Error('gone');
      return { seq };
    });
    assert.equal(failed, 1);
    eq([...raws.keys()].sort((a, b) => a - b), [1, 2, 4, 6, 8, 9, 10]); // not the posture, not the pruned, not the failure
    assert.ok(peak <= 6 && peak > 1, 'peak ' + peak);
  },
};

(async () => {
  let bad = 0;
  for (const [name, fn] of Object.entries(tests)) {
    try { await fn(); console.log('ok   ' + name); } catch (e) { bad++; console.log('FAIL ' + name + '\n' + e.stack); }
  }
  process.exit(bad ? 1 : 0);
})();
