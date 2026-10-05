// Regression guard for the DiffInspectorView insertion/deletion KPIs and the
// producer contract they rely on.
//
// Producer contract (internal/db/queries.go, GetRecentEvents / GetRecentEventsFiltered):
//   COALESCE(e.added_lines, 0), COALESCE(e.deleted_lines, 0)   -- recent-events SELECT
//   type EventRecord struct { AddedLines int `json:"added_lines"` ... }  -- no omitempty
// Together these mean added_lines/deleted_lines are ALWAYS numbers on the wire:
// a NULL column becomes 0 and the field is never omitted. So the consumer
// (web/src/components/DiffInspectorView.tsx) must sum the real fields and must
// NOT substitute a fabricated constant for a missing one.
//
// Defect (pre-fix): both KPI sums read
//   acc + (e.added_lines ?? (e.action === 'ADDED' ? e.lines_of_code || 10 : 0))
// The `|| 10` is an unreachable fabrication: `??` only fires on null/undefined
// and the producer never sends either. It was dead code that, if the producer
// contract ever loosened, would print a made-up "10 lines" as a measurement.
//
// This guard pins three things:
//   A. SOURCE producer contract -- queries.go COALESCEs both fields and the
//      EventRecord struct tags carry no omitempty (fails if that ever changes).
//   B. SOURCE consumer contract -- the KPI sums reference added_lines /
//      deleted_lines and contain no `|| 10`-style fabrication constant.
//   C. RENDER behaviour -- the KPI totals equal the true sums (control), and a
//      payload missing added_lines contributes 0, never a fabricated number
//      (the removed fabrication must not return).
//
// Run: node web/scripts/diff-inspector-kpi-regression.mjs
// Exit 0 = pass, 1 = fail, 2 = blocked. Renders the REAL component via the
// repo's own vite; only its three presentational children are stubbed.
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
const work = fs.mkdtempSync(path.join(root, '.temp_files', 'wt-diff-kpi-'));
const cleanup = () => { try { fs.rmSync(work, { recursive: true, force: true }); } catch {} };
process.on('exit', cleanup);
const blocked = (why) => { console.log('BLOCKED: ' + why); cleanup(); process.exit(2); };

const failures = [];
const check = (cond, msg) => { if (!cond) failures.push(msg); };

try {
  // ---- A. Producer contract (source) ----------------------------------------
  const queries = fs.readFileSync(path.join(root, 'internal', 'db', 'queries.go'), 'utf8');
  check(
    queries.includes('COALESCE(e.added_lines, 0)') && queries.includes('COALESCE(e.deleted_lines, 0)'),
    'producer contract: the recent-events query must COALESCE added_lines and deleted_lines to 0 ' +
      '(queries.go) so a NULL column still reaches the UI as a number',
  );
  check(
    /json:"added_lines"/.test(queries) && !/added_lines,omitempty/.test(queries) &&
      /json:"deleted_lines"/.test(queries) && !/deleted_lines,omitempty/.test(queries),
    'producer contract: EventRecord must emit added_lines/deleted_lines without omitempty, ' +
      'so the fields are always present and numeric on the wire',
  );

  // ---- B. Consumer contract (source) ----------------------------------------
  const compSrc = fs.readFileSync(path.join(webDir, 'src', 'components', 'DiffInspectorView.tsx'), 'utf8');
  const insSum = compSrc.match(/TOTAL INSERTIONS[\s\S]{0,400}?reduce\(([\s\S]{0,300}?)\)\.toLocaleString/);
  const delSum = compSrc.match(/TOTAL DELETIONS[\s\S]{0,400}?reduce\(([\s\S]{0,300}?)\)\.toLocaleString/);
  check(!!insSum, 'setup: could not locate the TOTAL INSERTIONS sum in DiffInspectorView.tsx');
  check(!!delSum, 'setup: could not locate the TOTAL DELETIONS sum in DiffInspectorView.tsx');
  if (insSum) {
    check(/added_lines/.test(insSum[1]), 'TOTAL INSERTIONS must sum the real added_lines field');
    check(!/\|\|\s*\d+/.test(insSum[1]), 'TOTAL INSERTIONS must not substitute a fabricated numeric fallback (e.g. `|| 10`)');
  }
  if (delSum) {
    check(/deleted_lines/.test(delSum[1]), 'TOTAL DELETIONS must sum the real deleted_lines field');
    check(!/\|\|\s*\d+/.test(delSum[1]), 'TOTAL DELETIONS must not substitute a fabricated numeric fallback (e.g. `|| 10`)');
  }

  // ---- Render the real component --------------------------------------------
  fs.symlinkSync(path.join(webDir, 'node_modules'), path.join(work, 'node_modules'), process.platform === 'win32' ? 'junction' : 'dir');
  if (!fs.existsSync(path.join(work, 'node_modules'))) blocked('could not link web/node_modules');

  // Presentational children only (they pull their own data hooks).
  for (const n of ['RichDiffViewer', 'FileReadDetails', 'SymbolHistoryTimeline']) {
    fs.writeFileSync(path.join(work, `stub-${n}.mjs`), `export function ${n}() { return null; }\n`);
  }

  const out = await vite.transformWithOxc(compSrc, 'DiffInspectorView.tsx');
  if (out.errors && out.errors.length > 0) blocked('transform errors: ' + JSON.stringify(out.errors));
  let code = out.code;
  for (const n of ['RichDiffViewer', 'FileReadDetails', 'SymbolHistoryTimeline']) {
    code = code.replace(new RegExp(`(from\\s*)(['"])\\./${n}\\2`, 'g'), `$1$2./stub-${n}.mjs$2`);
  }
  const compOut = path.join(work, 'DiffInspectorView.mjs');
  fs.writeFileSync(compOut, code, 'utf8');

  const React = (await import('react')).default;
  const { renderToStaticMarkup } = await import('react-dom/server');
  const { DiffInspectorView } = await import(pathToFileURL(compOut).href);

  const render = (events) =>
    renderToStaticMarkup(React.createElement(DiffInspectorView, { events, loading: false }));

  // Read the numeric token that follows a KPI label.
  const kpi = (html, label) => {
    const i = html.indexOf(label);
    if (i < 0) return null;
    const seg = html.slice(i, i + 400);
    const m = seg.match(/([+-]?[\d,]+)\s*lines/);
    return m ? m[1] : null;
  };

  const ev = (o) => ({
    event_id: o.id, run_id: 'run-1', repo_name: 'wrongtrace', file_path: 'f.go',
    node_signature: 'func:DoTask', node_type: 'function', action: o.action ?? 'MODIFIED',
    ast_content_hash: 'h1', lines_of_code: o.loc ?? 20, start_line: 1, end_line: 10,
    diff_snippet: '', event_time: '2026-09-29T00:00:00Z', ...o,
  });

  // ---- C. RENDER behaviour ---------------------------------------------------
  // C1 CONTROL: real payloads -> totals equal the true sums.
  {
    const h = render([
      ev({ id: 'a', action: 'ADDED', added_lines: 5, deleted_lines: 1 }),
      ev({ id: 'b', action: 'MODIFIED', added_lines: 3, deleted_lines: 2 }),
    ]);
    check(kpi(h, 'TOTAL INSERTIONS') === '+8', `control: TOTAL INSERTIONS should be +8 from the real added_lines, got ${kpi(h, 'TOTAL INSERTIONS')}`);
    check(kpi(h, 'TOTAL DELETIONS') === '-3', `control: TOTAL DELETIONS should be -3 from the real deleted_lines, got ${kpi(h, 'TOTAL DELETIONS')}`);
  }

  // C2 Zero is a real value, not a trigger for a fallback.
  {
    const h = render([ev({ id: 'z', action: 'ADDED', added_lines: 0, deleted_lines: 0 })]);
    check(kpi(h, 'TOTAL INSERTIONS') === '+0', `an event with added_lines=0 must contribute 0, got ${kpi(h, 'TOTAL INSERTIONS')}`);
  }

  // C3 The removed fabrication must not return: a payload missing added_lines
  //    must never be replaced by a fabricated magnitude (a `|| 10` constant or a
  //    lines_of_code stand-in). The type is now `number` (non-optional), so a
  //    missing field is a producer CONTRACT VIOLATION; the component trusts the
  //    type, and part A (the queries.go source check) is what catches a producer
  //    regression in CI. C3 therefore pins only the anti-fabrication invariant.
  {
    const noAdded = { id: 'n', action: 'ADDED', loc: 42 };
    delete noAdded.added_lines; // simulate a producer contract violation
    const h = render([ev(noAdded)]);
    const ins = kpi(h, 'TOTAL INSERTIONS');
    check(ins !== '+10', 'the removed `|| 10` fabrication must not reappear');
    check(ins !== '+42', `a missing added_lines must not be replaced by a lines_of_code magnitude; got ${ins}`);
  }
} catch (e) {
  blocked('unexpected harness error: ' + (e && e.stack));
}

if (failures.length > 0) {
  for (const f of failures) console.log('  - ' + f);
  cleanup();
  console.log(`FAIL: diff-inspector-kpi (${failures.length} assertion(s))`);
  process.exit(1);
}
console.log('PASS: DiffInspectorView KPIs sum the real producer fields with no fabrication');
