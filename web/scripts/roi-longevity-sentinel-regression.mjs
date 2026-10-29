// Regression test: ROIAnalysis must not render the Go "no nodes" sentinel as a
// measured average lifetime.
//
// Producer (internal/db/queries.go, ModelComparison):
//     AVG(julianday(CASE WHEN is_dead = 1 THEN last_event_time ELSE CURRENT_TIMESTAMP END)
//         - julianday(birth_time)) AS avg_longevity
//     ...
//     COALESCE(lc.avg_longevity, 0)
// An AVG over no rows is NULL, so a model with NO code nodes arrives with
// avg_longevity_days == 0. That means "never computed" — a model that has only
// reported telemetry, or a fresh project — not "its code dies instantly".
//
// Defect (pre-fix): the "Avg Life" cell rendered {m.avg_longevity_days.toFixed(1)}d
// unconditionally, so every model in a fresh project showed a measured "0.0d".
// Both neighbouring conventions disagree with that: the same table's Cost/Survived
// cell renders "—" when it has no data (total_survived_nodes === 0), and the
// sibling consumer of this very field gates it (ModelIntelligenceMatrix.tsx
// renders "New" when avg_longevity_days is 0).
//
// Contract: avg_longevity_days === 0 renders the panel's no-data marker, not a
// measured "0.0d"; a real lifetime keeps its value — including a small one that
// still rounds to "0.0d", which is the control that proves the gate is on the
// sentinel rather than on the rendered string.
//
// Run: node web/scripts/roi-longevity-sentinel-regression.mjs
// Exit 0 = pass, 1 = fail. Transpiles and server-renders the REAL component with
// the repo's own vite, so this exercises production code, not a copy.
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
const work = fs.mkdtempSync(path.join(root, '.temp_files', 'wt-roi-longevity-'));
const failures = [];
const cleanup = () => { try { fs.rmSync(work, { recursive: true, force: true }); } catch {} };
process.on('exit', cleanup);
const blocked = (why) => { console.log('BLOCKED: ' + why); cleanup(); process.exit(2); };

try {
  fs.symlinkSync(path.join(webDir, 'node_modules'), path.join(work, 'node_modules'), process.platform === 'win32' ? 'junction' : 'dir');
  if (!fs.existsSync(path.join(work, 'node_modules'))) blocked('could not link web/node_modules');

  const transpile = async (rel, out) => {
    const res = await vite.transformWithOxc(fs.readFileSync(path.join(webDir, rel), 'utf8'), path.basename(rel));
    if (res.errors && res.errors.length > 0) blocked('transform errors in ' + rel + ': ' + JSON.stringify(res.errors));
    const outPath = path.join(work, out);
    fs.writeFileSync(outPath, res.code, 'utf8');
    return { src: fs.readFileSync(path.join(webDir, rel), 'utf8'), outPath };
  };

  const comp = await transpile(path.join('src', 'components', 'ROIAnalysis.tsx'), 'ROIAnalysis.mjs');
  await transpile(path.join('src', 'types', 'index.ts'), 'types.mjs');
  fs.writeFileSync(
    comp.outPath,
    fs.readFileSync(comp.outPath, 'utf8').replace(/(from\s*)(['"])\.\.\/types\2/g, '$1$2./types.mjs$2'),
    'utf8',
  );

  const React = (await import('react')).default;
  const { renderToStaticMarkup } = await import('react-dom/server');
  const { ROIAnalysis } = await import(pathToFileURL(comp.outPath).href);

  const model = (name, nodes, survived, longevity) => ({
    model: name, total_nodes: nodes, active_nodes: nodes, survival_rate_pct: 90,
    avg_longevity_days: longevity, total_cost_usd: 12.5,
    total_survived_nodes: survived, cost_per_surviving_node: survived > 0 ? 0.8 : 0, run_count: 4,
  });
  // No nodes at all: the AVG had no rows, so the producer COALESCEd the sentinel 0.
  const SENTINEL = model('sentinel-no-nodes', 0, 0, 0);
  // A real lifetime, and a real one small enough to round to "0.0d".
  const REAL = model('real-lifetime', 120, 50, 3.4);
  const REAL_SMALL = model('real-small-lifetime', 120, 50, 0.02);

  const render = (m) => renderToStaticMarkup(React.createElement(ROIAnalysis, { models: [m] }));
  const avgLifeCell = (html, name) => {
    const row = html.split('<tr').find((r) => r.includes(name));
    if (!row) return null;
    const cells = row.split('<td');
    return cells.length ? cells[cells.length - 1] : null;
  };
  const check = (cond, msg) => { if (!cond) failures.push(msg); };

  // --- The defect -----------------------------------------------------------
  const sentinelCell = avgLifeCell(render(SENTINEL), SENTINEL.model);
  check(sentinelCell !== null, 'setup: the no-nodes model row was not rendered');
  if (sentinelCell) {
    check(
      !/0\.0d/.test(sentinelCell),
      'a model with no nodes (avg_longevity_days = 0, the producer\'s COALESCE of an empty AVG) ' +
        'must not render "0.0d" as a measured average lifetime',
    );
  }

  // --- Control 1: a real lifetime keeps its value ---------------------------
  const realCell = avgLifeCell(render(REAL), REAL.model);
  check(realCell !== null, 'setup: the real-lifetime row was not rendered');
  if (realCell) {
    check(/3\.4d/.test(realCell), 'control: a real avg_longevity_days must still render as "3.4d"');
  }

  // --- Control 2: a real lifetime that ROUNDS to "0.0d" must still render ----
  // This is what separates "gate on the sentinel" from "gate on the string".
  const smallCell = avgLifeCell(render(REAL_SMALL), REAL_SMALL.model);
  check(smallCell !== null, 'setup: the small-real-lifetime row was not rendered');
  if (smallCell) {
    check(
      /0\.0d/.test(smallCell),
      'control: a genuine lifetime of 0.02 days must still render (it rounds to "0.0d"); ' +
        'suppressing it would hide a real measurement',
    );
  }
} catch (e) {
  blocked('unexpected harness error: ' + (e && e.stack));
}

if (failures.length > 0) {
  for (const f of failures) console.log('  - ' + f);
  cleanup();
  console.log(`FAIL: roi-longevity-sentinel (${failures.length} assertion(s))`);
  process.exit(1);
}
console.log('PASS: the no-nodes longevity sentinel is never rendered as a measured lifetime');
