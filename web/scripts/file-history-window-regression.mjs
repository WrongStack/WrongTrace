// Regression test: FileHistoryTimeline's revision window must keep the NEWEST
// revisions visible.
//
// Defect (pre-fix): `revisions` is chronological (index 0 = oldest) and the
// component rendered `revisions.slice(0, visibleCount)`, then reversed it for
// "Newest first". Past 40 revisions the list therefore showed the OLDEST 40:
// the latest revision and its "Current" badge were missing, "Show 40 older
// revisions" actually revealed NEWER ones, and sparkline clicks on bars outside
// the window did nothing (no element to scroll to, nothing expanded).
//
// Contract (web/src/lib/revisionWindow.ts):
//   - selectRevisionWindow keeps the newest `visibleCount` revisions in both
//     sort orders; hiddenCount counts the OLDER revisions left out.
//   - visibleCountToInclude grows (never shrinks) the count so a focused
//     revision is rendered.
// The component must use these helpers (source check below), so the guard
// cannot pass while the component regresses to its own slicing.
//
// Run: node web/scripts/file-history-window-regression.mjs
// Exit 0 = pass, 1 = fail. Transpiles the REAL helper with the repo's own vite.
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';

const scriptDir = path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
const root = path.resolve(scriptDir, '..', '..');

const webRequire = createRequire(pathToFileURL(path.join(root, 'web', 'package.json')));
const vite = webRequire('vite');

const src = fs.readFileSync(path.join(root, 'web', 'src', 'lib', 'revisionWindow.ts'), 'utf8');
const transformed = await vite.transformWithOxc(src, 'revisionWindow.ts');
if (transformed.errors && transformed.errors.length > 0) {
  console.log('FAIL: transform errors: ' + JSON.stringify(transformed.errors));
  process.exit(1);
}
const mod = await import('data:text/javascript;base64,' + Buffer.from(transformed.code).toString('base64'));
const { selectRevisionWindow, visibleCountToInclude } = mod;

const failures = [];
const check = (cond, msg) => { if (!cond) failures.push(msg); };
const keys = (arr) => arr.map((r) => r.key).join(',');

// 100 chronological revisions r1 (oldest) .. r100 (newest).
const revs = Array.from({ length: 100 }, (_, i) => ({ key: `r${i + 1}` }));

{
  const { displayed, hiddenCount } = selectRevisionWindow(revs, 40, true);
  check(displayed.length === 40, `newestFirst: expected 40 displayed, got ${displayed.length}`);
  check(displayed[0]?.key === 'r100', `newestFirst: first row must be the latest revision r100, got ${displayed[0]?.key}`);
  check(displayed[39]?.key === 'r61', `newestFirst: last row must be r61, got ${displayed[39]?.key}`);
  check(hiddenCount === 60, `newestFirst: expected 60 hidden older revisions, got ${hiddenCount}`);
}
{
  const { displayed, hiddenCount } = selectRevisionWindow(revs, 40, false);
  check(displayed[0]?.key === 'r61' && displayed[39]?.key === 'r100',
    `oldestFirst: expected r61..r100, got ${displayed[0]?.key}..${displayed[39]?.key}`);
  check(hiddenCount === 60, `oldestFirst: expected 60 hidden, got ${hiddenCount}`);
}
{
  // "Show older" (visibleCount 40 -> 80) must ADD older revisions and keep the newest.
  const before = new Set(selectRevisionWindow(revs, 40, true).displayed.map((r) => r.key));
  const after = selectRevisionWindow(revs, 80, true);
  const added = after.displayed.filter((r) => !before.has(r.key)).map((r) => Number(r.key.slice(1)));
  check(added.length === 40 && Math.max(...added) === 60 && Math.min(...added) === 21,
    `show older: expected to reveal r21..r60, revealed ${added.length} (${Math.min(...added)}..${Math.max(...added)})`);
  check(after.displayed[0]?.key === 'r100', 'show older: latest revision must stay first');
  check(after.hiddenCount === 20, `show older: expected 20 hidden, got ${after.hiddenCount}`);
}
{
  // Fewer revisions than the window: everything shows, nothing hidden.
  const small = revs.slice(0, 3);
  const w = selectRevisionWindow(small, 40, true);
  check(keys(w.displayed) === 'r3,r2,r1' && w.hiddenCount === 0, `small: got ${keys(w.displayed)} hidden=${w.hiddenCount}`);
  const w2 = selectRevisionWindow(small, 40, false);
  check(keys(w2.displayed) === 'r1,r2,r3', `small oldestFirst: got ${keys(w2.displayed)}`);
  check(keys(small) === 'r1,r2,r3', 'selectRevisionWindow must not mutate its input');
}
{
  // Sparkline focus on a hidden bar expands the window to include it.
  check(visibleCountToInclude(revs, 'r1', 40) === 100, `focus r1: expected 100, got ${visibleCountToInclude(revs, 'r1', 40)}`);
  check(visibleCountToInclude(revs, 'r50', 40) === 51, `focus r50: expected 51, got ${visibleCountToInclude(revs, 'r50', 40)}`);
  check(visibleCountToInclude(revs, 'r90', 40) === 40, 'focus visible revision must not change the count');
  check(visibleCountToInclude(revs, 'r50', 80) === 80, 'focus must never shrink the count');
  check(visibleCountToInclude(revs, 'nope', 40) === 40, 'unknown key must leave the count unchanged');
  const n = visibleCountToInclude(revs, 'r5', 40);
  check(selectRevisionWindow(revs, n, true).displayed.some((r) => r.key === 'r5'), 'focused revision must be rendered after expansion');
}

// The component must actually route through the helpers.
const component = fs.readFileSync(path.join(root, 'web', 'src', 'components', 'FileHistoryTimeline.tsx'), 'utf8');
check(/selectRevisionWindow\(\s*revisions\s*,\s*visibleCount\s*,\s*newestFirst\s*\)/.test(component),
  'FileHistoryTimeline must compute its window via selectRevisionWindow(revisions, visibleCount, newestFirst)');
check(/visibleCountToInclude\(/.test(component), 'FileHistoryTimeline.focusRevision must expand via visibleCountToInclude');
check(!/revisions\.slice\(\s*0\s*,\s*visibleCount\s*\)/.test(component), 'FileHistoryTimeline must not slice the OLDEST revisions');

if (failures.length > 0) {
  for (const f of failures) console.log('  - ' + f);
  console.log(`FAIL: file-history-window (${failures.length} assertion(s))`);
  process.exit(1);
}
console.log('PASS: file-history-window keeps the newest revisions, reveals older ones, and expands to focused bars');
