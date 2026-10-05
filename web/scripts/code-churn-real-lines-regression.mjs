// Regression guard: CodeChurnTimeline must sum the real added_lines /
// deleted_lines and never fabricate a magnitude from a genuine 0.
//
// Producer contract (internal/ast/diff.go, formatAddedDiff/formatDeletedDiff):
//   if len(lines) == 0 { return "", 0, 0 }
// A node with an empty body therefore yields a GENUINE added_lines = 0 (and a
// DELETED event with an empty body a genuine deleted_lines = 0). Separately,
// a MODIFIED event can legitimately be deletion-only, so its added_lines is 0.
// 0 here is a real measurement, NOT "unknown".
//
// The TS mirror is now `added_lines: number` (non-optional), matching the Go
// EventRecord (no omitempty) and the db query's COALESCE(added_lines, 0).
//
// Defect (pre-fix): BOTH the bucketing (CodeChurnTimeline.tsx timelineData) and
// the KPI totals useMemo read
//     typeof e.added_lines === 'number' && e.added_lines > 0
//       ? e.added_lines
//       : e.action === 'ADDED' ? e.lines_of_code || 10
//       : e.action === 'MODIFIED' ? Math.max(1, Math.round((e.lines_of_code||6)*0.3))
//       : 0
// The `> 0` test folds a genuine 0 (empty body / deletion-only edit) into the
// "unknown" arm and invents a positive magnitude from lines_of_code — so the
// "AST inserted code" KPI reports insertions that never happened. This guard
// pins that the KPIs sum the real fields.
//
// Run: node web/scripts/code-churn-real-lines-regression.mjs
// Exit 0 = pass, 1 = fail, 2 = blocked. Renders the REAL component via the
// repo's own vite and reads the plain-text KPI totals.
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';

const scriptDir = path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
const root = path.resolve(scriptDir, '..', '..');
const webDir = path.join(root, 'web');

const webRequire = createRequire(pathToFileURL(path.join(webDir, 'package.json')));
const vite = webRequire('vite');

fs.mkdirSync(path.join(root, '.temp_files'), { recursive: true });
const work = fs.mkdtempSync(path.join(root, '.temp_files', 'wt-churn-'));
const cleanup = () => { try { fs.rmSync(work, { recursive: true, force: true }); } catch {} };
process.on('exit', cleanup);
const blocked = (why) => { console.log('BLOCKED: ' + why); cleanup(); process.exit(2); };

const failures = [];
const check = (cond, msg) => { if (!cond) failures.push(msg); };

try {
  fs.symlinkSync(path.join(webDir, 'node_modules'), path.join(work, 'node_modules'), process.platform === 'win32' ? 'junction' : 'dir');
  if (!fs.existsSync(path.join(work, 'node_modules'))) blocked('could not link web/node_modules');

  const compSrc = fs.readFileSync(path.join(webDir, 'src', 'components', 'CodeChurnTimeline.tsx'), 'utf8');
  const out = await vite.transformWithOxc(compSrc, 'CodeChurnTimeline.tsx');
  if (out.errors && out.errors.length > 0) blocked('transform errors: ' + JSON.stringify(out.errors));
  const compOut = path.join(work, 'CodeChurnTimeline.mjs');
  fs.writeFileSync(compOut, out.code, 'utf8');

  const React = (await import('react')).default;
  const { renderToStaticMarkup } = await import('react-dom/server');
  const { CodeChurnTimeline } = await import(pathToFileURL(compOut).href);

  // Read a plain-text KPI number rendered in a `... text-lg font-bold ...` div
  // (e.g. `+57` / `-0`). Returns null when absent so a broken render is a
  // failure, not a silent pass.
  const kpiNum = (html, cls) => {
    const re = new RegExp(`text-lg font-bold ${cls}">([^<]*)<`, 'm');
    const m = html.match(re);
    return m ? m[1].trim() : null;
  };
  const inserted = (html) => kpiNum(html, 'text-emerald-400');
  const pruned = (html) => kpiNum(html, 'text-rose-400');

  const ev = (o) => ({
    event_id: o.id, run_id: 'run-1', repo_name: 'wrongtrace', file_path: 'f.go',
    node_signature: 'func:DoTask', node_type: 'function', action: o.action ?? 'MODIFIED',
    ast_content_hash: 'h1', lines_of_code: o.loc ?? 20, start_line: 1, end_line: 10,
    diff_snippet: '', event_time: '2026-09-29T00:00:00Z',
    added_lines: o.added ?? 0, deleted_lines: o.deleted ?? 0, ...o,
  });

  const render = (events) =>
    renderToStaticMarkup(React.createElement(CodeChurnTimeline, { events, loading: false }));

  // --- The defect: a genuine 0 must not become a fabricated magnitude -------
  // Empty-body ADDED: formatAddedDiff returned added=0, but LOC is 42 so the
  // pre-fix "unknown" arm would fabricate 42. Deletion-only MODIFIED: added=0
  // is real, but LOC is 50 so the arm would fabricate max(1, 15) = 15.
  {
    const h = render([
      ev({ id: 'a', action: 'ADDED', added: 0, deleted: 0, loc: 42 }),
      ev({ id: 'b', action: 'MODIFIED', added: 0, deleted: 3, loc: 50 }),
    ]);
    const ins = inserted(h);
    const del = pruned(h);
    check(ins !== null, 'setup: the "AST inserted code" KPI was not rendered');
    check(del !== null, 'setup: the "AST pruned code" KPI was not rendered');
    check(ins === '+0', `a genuine added_lines=0 (empty-body ADDED + deletion-only MODIFIED) must total +0; got ${ins} (LOC-based fabrication)`);
    check(del === '-3', `the real deleted_lines (3) must total -3; got ${del}`);
  }

  // --- Control: real non-zero values sum exactly ---------------------------
  {
    const h = render([
      ev({ id: 'c', action: 'MODIFIED', added: 5, deleted: 2, loc: 50 }),
      ev({ id: 'd', action: 'MODIFIED', added: 4, deleted: 1, loc: 50 }),
    ]);
    const ins = inserted(h);
    const del = pruned(h);
    check(ins === '+9', `control: real added_lines (5+4) must total +9; got ${ins}`);
    check(del === '-3', `control: real deleted_lines (2+1) must total -3; got ${del}`);
  }

  // --- Control: a zero-loc empty-body ADDED still totals 0 (no `|| 10`) -----
  {
    const h = render([ev({ id: 'e', action: 'ADDED', added: 0, deleted: 0, loc: 0 })]);
    const ins = inserted(h);
    check(ins === '+0', `an empty-body ADDED with LOC=0 must total +0; got ${ins}`);
  }
} catch (e) {
  blocked('unexpected harness error: ' + (e && e.stack));
}

if (failures.length > 0) {
  for (const f of failures) console.log('  - ' + f);
  cleanup();
  console.log(`FAIL: code-churn-real-lines (${failures.length} assertion(s))`);
  process.exit(1);
}
console.log('PASS: CodeChurnTimeline KPIs sum the real added/deleted lines (no LOC fabrication)');
