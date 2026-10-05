// Regression test: every card in the DiffInspectorView summary KPI strip must
// describe the SAME set of events.
//
// Defect (pre-fix): the strip is gated on filteredEvents.length > 0 and three of
// its four cards reduce over filteredEvents, but "UNIQUE FILES CHURNED" read
// uniqueFiles.length — a memo built over the UNFILTERED `events` prop. Selecting
// a file (or an action, or typing in the search box) therefore updated
// insertions/deletions/mutation-rate while the file count kept reporting the
// whole history. A summary strip whose cards disagree about which rows they are
// describing.
//
// The file/search/action controls are live, user-reachable <select>/<input>
// elements, so this misreports immediately on ordinary use.
//
// Contract (web/src/components/DiffInspectorView.tsx): the four cards describe
// one filtered set, so they stay mutually consistent. The FILE DROPDOWN is a
// control, not a card: it must keep listing every file so the user can select
// one — the fix must not derive the option list from the filtered rows, which
// would make a selected file impossible to change.
//
// Run: node web/scripts/diff-inspector-filter-kpi-regression.mjs
// Exit 0 = pass, 1 = fail. Transpiles and server-renders the REAL component with
// the repo's own vite; only the three detail-pane children are stubbed, because
// they are outside the strip under test and pull in the query client.
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import fs from 'node:fs';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '..', '..');
const webRequire = createRequire(pathToFileURL(path.join(root, 'web', 'package.json')));
const vite = webRequire('vite');
const React = webRequire('react');
const { renderToStaticMarkup } = webRequire('react-dom/server');

const failures = [];
const check = (cond, msg) => { if (!cond) failures.push(msg); };

// ---------------------------------------------------------------- fixtures ---
// Three files, one event each, chosen so the three filtered cards and the
// unique-file card have DIFFERENT unfiltered vs filtered values. A fix that
// only moved one card cannot make them agree.
const ev = (id, file, action, added, deleted) => ({
  event_id: id,
  run_id: 'r1',
  repo_name: 'wrongtrace',
  file_path: file,
  node_signature: `func:${id}`,
  node_type: 'function',
  action,
  ast_content_hash: 'h',
  lines_of_code: 20,
  start_line: 1,
  end_line: 20,
  diff_snippet: '',
  added_lines: added,
  deleted_lines: deleted,
  event_time: `2026-09-29T0${id}:00:00Z`,
});

// unfiltered totals: +22 insertions, -3 deletions, 3 files, 2 modifications
const ALL_EVENTS = [
  ev('1', 'a.ts', 'ADDED', 10, 0),
  ev('2', 'b.ts', 'MODIFIED', 5, 2),
  ev('3', 'c.ts', 'MODIFIED', 7, 1),
];
// filtered to b.ts: +5, -2, 1 file, 1 modification
const B_ONLY = ALL_EVENTS.filter((e) => e.file_path === 'b.ts');

// ------------------------------------------------------------- assertions ---
// Reads a card's value: the text inside the <div> that follows its label.
function card(html, label) {
  const i = html.indexOf(label);
  if (i < 0) return null;
  const after = html.slice(i + label.length);
  const m = after.match(/<\/div>\s*<div[^>]*>([^<]*)</);
  return m ? m[1].trim() : null;
}

async function renderWithFileFilter(file) {
  // A DISTINCT filename per filter value: Node's ESM registry caches by resolved
  // URL, so reusing one path would hand the second render the first render's
  // already-imported component and silently drop the filter.
  const tmpName = `.tmp-diff-filter-kpi-${process.pid}-${file === 'ALL' ? 'all' : 'sel'}`;
  const tmpBase = path.join(here, tmpName);
  const tmpComp = `${tmpBase}.mjs`;
  const stubs = [
    ['RichDiffViewer', 'RichDiffViewer'],
    ['FileReadDetails', 'FileReadDetails'],
    ['SymbolHistoryTimeline', 'SymbolHistoryTimeline'],
  ];
  const stubFiles = [];
  try {
    for (const [name, exportName] of stubs) {
      const p = `${tmpBase}-STUB-${name}.mjs`;
      fs.writeFileSync(p, `export function ${exportName}() { return null; }\n`);
      stubFiles.push(p);
    }

    const src = fs.readFileSync(
      path.join(root, 'web', 'src', 'components', 'DiffInspectorView.tsx'),
      'utf8',
    );
    const t = await vite.transformWithOxc(src, 'DiffInspectorView.tsx');
    if (t.errors && t.errors.length > 0) {
      return { error: 'transform errors: ' + JSON.stringify(t.errors) };
    }
    let code = t.code;

    // Redirect the three detail-pane children to local stubs.
    for (const [name] of stubs) {
      const re = new RegExp(`from\\s+['"]\\./${name}['"]`, 'g');
      if (!re.test(code)) {
        return { error: `could not find the import of ./${name} in the transpiled component` };
      }
      code = code.replace(re, `from "./${tmpName}-STUB-${name}.mjs"`);
    }

    // Pre-apply the file filter the way the <select> would, by changing only
    // the selectedFile initial state. setSelectedFile survives transpilation,
    // so this anchor is unique; 'ALL' is the unfiltered default.
    if (file !== 'ALL') {
      const stateRe = /(setSelectedFile\s*\]?\s*=\s*useState\(\s*)["']ALL["']/;
      if (!stateRe.test(code)) {
        return { error: 'could not locate the selectedFile initial state' };
      }
      code = code.replace(stateRe, `$1"${file}"`);
    }

    fs.writeFileSync(tmpComp, code);
    const mod = await import(pathToFileURL(tmpComp).href);
    const Comp = mod.DiffInspectorView;

    return {
      html: renderToStaticMarkup(
        React.createElement(Comp, { events: ALL_EVENTS, loading: false, currentProject: null }),
      ),
    };
  } finally {
    fs.rmSync(tmpComp, { force: true });
    for (const p of stubFiles) fs.rmSync(p, { force: true });
  }
}

const all = await renderWithFileFilter('ALL');
const one = await renderWithFileFilter('b.ts');

if (all.error) { console.log('FAIL: ' + all.error); process.exit(1); }
if (one.error) { console.log('FAIL: ' + one.error); process.exit(1); }

// --- Setup: the strip must actually be present, or nothing below is evidence.
for (const label of ['TOTAL INSERTIONS', 'TOTAL DELETIONS', 'UNIQUE FILES CHURNED', 'AST MUTATION RATE']) {
  check(all.html.includes(label), `setup: the unfiltered render must contain the ${label} card`);
  check(one.html.includes(label), `setup: the filtered render must contain the ${label} card`);
}
if (failures.length > 0) {
  for (const f of failures) console.log('  - ' + f);
  console.log('FAIL: diff-inspector-filter-kpi (setup)');
  process.exit(1);
}

// --- Control: the unfiltered render must match the fixture's own totals. If
// this fails the fixture is wrong, not the component.
check(card(all.html, 'TOTAL INSERTIONS') === '+22 lines',
  `control: unfiltered insertions = ${card(all.html, 'TOTAL INSERTIONS')}, want "+22 lines"`);
check(card(all.html, 'TOTAL DELETIONS') === '-3 lines',
  `control: unfiltered deletions = ${card(all.html, 'TOTAL DELETIONS')}, want "-3 lines"`);
check(card(all.html, 'UNIQUE FILES CHURNED') === '3 files',
  `control: ununique files = ${card(all.html, 'UNIQUE FILES CHURNED')}, want "3 files"`);
check(card(all.html, 'AST MUTATION RATE') === '2 modifications',
  `control: unfiltered mutations = ${card(all.html, 'AST MUTATION RATE')}, want "2 modifications"`);

// --- The three already-filtered siblings, proving the filter is in effect.
check(card(one.html, 'TOTAL INSERTIONS') === '+5 lines',
  `filtered insertions = ${card(one.html, 'TOTAL INSERTIONS')}, want "+5 lines"`);
check(card(one.html, 'TOTAL DELETIONS') === '-2 lines',
  `filtered deletions = ${card(one.html, 'TOTAL DELETIONS')}, want "-2 lines"`);
check(card(one.html, 'AST MUTATION RATE') === '1 modifications',
  `filtered mutations = ${card(one.html, 'AST MUTATION RATE')}, want "1 modifications"`);

// --- The defect: with the filter live, the file count must describe the SAME
// single file the three siblings just counted, not all three.
check(card(one.html, 'UNIQUE FILES CHURNED') === '1 files',
  `with the file filter set to b.ts, "UNIQUE FILES CHURNED" must be "1 files" (the file the other three cards counted); got ${card(one.html, 'UNIQUE FILES CHURNED')}`);

// --- Control B: the DROPDOWN is a control, not a card. It must keep listing
// every file even while filtered, or a selected file could never be changed.
for (const f of ['a.ts', 'b.ts', 'c.ts']) {
  check(one.html.includes(f), `the file dropdown must still offer ${f} while filtered to b.ts`);
}
check(/All Files \(3\)/.test(one.html),
  'the dropdown\'s "All Files" option must still count every file while filtered');

// --- Source: the KPI must not read the unfiltered memo.
const src = fs.readFileSync(
  path.join(root, 'web', 'src', 'components', 'DiffInspectorView.tsx'),
  'utf8',
);
const kpiBlock = src.slice(src.indexOf('UNIQUE FILES CHURNED') - 400, src.indexOf('UNIQUE FILES CHURNED') + 200);
check(!/uniqueFiles\.length\s*\}\s*files/.test(kpiBlock),
  'the UNIQUE FILES CHURNED card must not render the unfiltered uniqueFiles.length');

if (failures.length > 0) {
  for (const f of failures) console.log('  - ' + f);
  console.log(`FAIL: diff-inspector-filter-kpi (${failures.length} assertion(s))`);
  process.exit(1);
}
console.log('PASS: every DiffInspectorView KPI card describes the same filtered set of events');
