// Regression test: ModelLeaderboard must never present the Go "ROI unknown"
// sentinel as a real measurement.
//
// Defect (pre-fix): the Go producer db.ModelComparison computes
// cost_per_surviving_node ONLY when total_survived_nodes > 0 and otherwise
// leaves it 0, so a 0 there means "never computed", not "free". Every model in
// a project onboarded <14 days ago has survived_count == 0 (all its nodes are
// new), and ModelLeaderboard rendered that sentinel in two places:
//
//   1. the roster table printed "$0.0000" as a real Cost/Survived price, and
//   2. the chart series mapped it to 0 — the BEST possible value on a
//      lower-is-better axis — so a model with no proven ROI outranked models
//      that actually have one.
//
// That is the same defect class already fixed for the sibling consumers
// (ROIAnalysis.tsx renders an em-dash; lib/grade.ts gained an explicit
// hasRoiData gate), fixed here in the remaining consumer.
//
// Contract (web/src/components/ModelLeaderboard.tsx): the sentinel is gated on
// total_survived_nodes > 0. No-ROI models render the em-dash and plot as a
// gap; models with genuine ROI data keep their real price everywhere
// (controls below).
//
// Run: node web/scripts/leaderboard-roi-regression.mjs
// Exit 0 = pass, 1 = fail. Transpiles and server-renders the REAL component
// with the repo's own vite, so this exercises production code, not a copy.
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import fs from 'node:fs';

const scriptDir = path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
const root = path.resolve(scriptDir, '..', '..');
const webDir = path.join(root, 'web');

const webRequire = createRequire(pathToFileURL(path.join(webDir, 'package.json')));
const vite = webRequire('vite');

// Render into a temp dir that can resolve react/recharts/lucide-react, and
// clean it up afterwards so the guard leaves nothing behind.
const work = fs.mkdtempSync(path.join(root, '.temp_files', 'wt-leaderboard-roi-'));
const failures = [];
const cleanup = () => { try { fs.rmSync(work, { recursive: true, force: true }); } catch {} };
process.on('exit', cleanup);

const blocked = (why) => { console.log('BLOCKED: ' + why); cleanup(); process.exit(2); };

try {
  spawnSync('cmd', ['/c', 'mklink', '/J', path.join(work, 'node_modules'), path.join(webDir, 'node_modules')]);
  if (!fs.existsSync(path.join(work, 'node_modules'))) blocked('could not link web/node_modules');

  const transpile = async (rel, out) => {
    const res = await vite.transformWithOxc(fs.readFileSync(path.join(webDir, rel), 'utf8'), path.basename(rel));
    if (res.errors && res.errors.length > 0) blocked('transform errors in ' + rel + ': ' + JSON.stringify(res.errors));
    const outPath = path.join(work, out);
    fs.writeFileSync(outPath, res.code, 'utf8');
    return { src: fs.readFileSync(path.join(webDir, rel), 'utf8'), outPath };
  };

  const comp = await transpile(path.join('src', 'components', 'ModelLeaderboard.tsx'), 'ModelLeaderboard.mjs');
  await transpile(path.join('src', 'types', 'index.ts'), 'types.mjs');
  // Repoint the sibling type import at the transpiled copy.
  fs.writeFileSync(
    comp.outPath,
    fs.readFileSync(comp.outPath, 'utf8').replace(/(from\s*)(['"])\.\.\/types\2/g, '$1$2./types.mjs$2'),
    'utf8',
  );

  const React = (await import('react')).default;
  const { renderToStaticMarkup } = await import('react-dom/server');
  const { ModelLeaderboard } = await import(pathToFileURL(comp.outPath).href);

  const row = (survived, cost) => ({
    model: 'claude-sonnet-4-20250514', total_nodes: 120, active_nodes: 108,
    survival_rate_pct: 90, avg_longevity_days: 2.5, total_cost_usd: 12.5,
    total_survived_nodes: survived, cost_per_surviving_node: cost, run_count: 4,
  });
  // <14 days old: every node is new, so ROI was never computed and the Go
  // sentinel 0 arrives here.
  const NO_ROI = row(0, 0);
  // Genuine ROI data, a real non-zero price, and the free-but-proven case.
  const HAS_ROI = row(50, 0.8);
  const FREE_BUT_PROVEN = row(12, 0);

  const html = renderToStaticMarkup(
    React.createElement(ModelLeaderboard, { models: [NO_ROI], loading: false }),
  );
  const noRoiRow = html.split('<tr').find((r) => r.includes(NO_ROI.model));
  const check = (cond, msg) => { if (!cond) failures.push(msg); };

  check(!!noRoiRow, 'setup: the no-ROI model row was not rendered');
  if (noRoiRow) {
    check(
      !/\$0\.0000/.test(noRoiRow),
      'a model with no ROI data (total_survived_nodes=0) must not render "$0.0000" as a real price',
    );
  }

  // --- Controls: real ROI data keeps its real price -------------------------
  for (const [label, m, expected] of [
    ['a model with real ROI data', HAS_ROI, /\$0\.8000/],
    ['a genuinely free model that HAS ROI data', FREE_BUT_PROVEN, /\$0\.0000/],
  ]) {
    const h = renderToStaticMarkup(React.createElement(ModelLeaderboard, { models: [m], loading: false }));
    const r = h.split('<tr').find((x) => x.includes(m.model));
    check(!!r, 'setup: control row not rendered (' + label + ')');
    if (r) check(expected.test(r), 'control: ' + label + ' must still render its real price');
  }

  // --- The chart series must consult the sentinel ---------------------------
  const series = comp.src.match(/costPerSurvived:([\s\S]{0,320}?),/);
  check(!!series, 'setup: could not locate the costPerSurvived series mapping');
  if (series) {
    check(
      /total_survived_nodes/.test(series[1]),
      'the costPerSurvived series must consult total_survived_nodes so the "never computed" ' +
        'sentinel is not plotted as the best (lowest) value on a lower-is-better axis',
    );
  }
} catch (e) {
  blocked('unexpected harness error: ' + (e && e.stack));
}

if (failures.length > 0) {
  for (const f of failures) console.log('  - ' + f);
  cleanup();
  console.log(`FAIL: leaderboard-roi (${failures.length} assertion(s))`);
  process.exit(1);
}
console.log('PASS: unknown ROI is never displayed or plotted as a real measurement');
