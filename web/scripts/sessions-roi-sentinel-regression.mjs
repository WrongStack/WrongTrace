// Regression test: AgentSessionsView's model cards must never present the Go
// "ROI unknown" sentinel as a real measurement.
//
// Contract (internal/db/queries.go:671-676, ModelComparison):
//     if r.TotalNodes > 0        { r.SurvivalRatePct  = ... }
//     if r.TotalSurvivedNodes > 0 { r.CostPerSurvNode = r.TotalCostUSD / float64(r.TotalSurvivedNodes) }
// so cost_per_surviving_node == 0 means "never computed" (no node survived the
// 14-day window), NOT "free". It is NOT a legitimate 0: the sibling Go guard
// and the em-dash convention in ROIAnalysis.tsx both treat it as unknown.
//
// Defect (pre-fix): the "Model Cards Overview" grid in the Active Sessions tab
// printed `${m.cost_per_surviving_node.toFixed(4)} / node` unconditionally, so
// a model onboarded <14 days ago (survived_count == 0 for every model) showed a
// fabricated "$0.0000 / node" — on the SAME card line that states
// "0 / 120 nodes alive", contradicting itself. This is the last unfixed
// ModelRow consumer; ROIAnalysis, ModelIntelligenceMatrix and ModelLeaderboard
// were fixed in rounds 22/23/78.
//
// Contract (web/src/components/AgentSessionsView.tsx): the sentinel is gated on
// total_survived_nodes > 0. No-ROI models render the em-dash; models with real
// ROI data keep their real price, INCLUDING a genuinely free model that does
// have ROI data (control 3).
//
// Run: node web/scripts/sessions-roi-sentinel-regression.mjs
// Exit 0 = pass, 1 = fail, 2 = blocked. Renders the REAL component through the
// repo's own vite; only the network hooks and a presentational child are stubbed.
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
const work = fs.mkdtempSync(path.join(root, '.temp_files', 'wt-sessions-roi-'));
const cleanup = () => { try { fs.rmSync(work, { recursive: true, force: true }); } catch {} };
process.on('exit', cleanup);
const blocked = (why) => { console.log('BLOCKED: ' + why); cleanup(); process.exit(2); };

const failures = [];
const check = (cond, msg) => { if (!cond) failures.push(msg); };

try {
  fs.symlinkSync(path.join(webDir, 'node_modules'), path.join(work, 'node_modules'), process.platform === 'win32' ? 'junction' : 'dir');
  if (!fs.existsSync(path.join(work, 'node_modules'))) blocked('could not link web/node_modules');

  const transpile = async (rel, out) => {
    const res = await vite.transformWithOxc(fs.readFileSync(path.join(webDir, rel), 'utf8'), path.basename(rel));
    if (res.errors && res.errors.length > 0) blocked('transform errors in ' + rel + ': ' + JSON.stringify(res.errors));
    const outPath = path.join(work, out);
    fs.writeFileSync(outPath, res.code, 'utf8');
    return outPath;
  };

  // Network boundaries (catalog queries) and a presentational child.
  fs.writeFileSync(
    path.join(work, 'stub-useMetrics.mjs'),
    'export function useModelCatalog() { return { data: [], isLoading: false }; }\n' +
      'export function useProviderCatalog() { return { data: [], isLoading: false }; }\n',
  );
  fs.writeFileSync(path.join(work, 'stub-RichDiffViewer.mjs'), 'export function RichDiffViewer() { return null; }\n');

  const types = await transpile(path.join('src', 'types', 'index.ts'), 'types.mjs');
  await transpile(path.join('src', 'lib', 'clipboard.ts'), 'clipboard.mjs');

  const src = fs.readFileSync(path.join(webDir, 'src', 'components', 'AgentSessionsView.tsx'), 'utf8');
  const out = await vite.transformWithOxc(src, 'AgentSessionsView.tsx');
  if (out.errors && out.errors.length > 0) blocked('transform errors in AgentSessionsView: ' + JSON.stringify(out.errors));

  let code = out.code
    .replace(/(from\s*)(['"])\.\.\/types\2/g, '$1$2./types.mjs$2')
    .replace(/(from\s*)(['"])\.\.\/lib\/clipboard\2/g, '$1$2./clipboard.mjs$2')
    .replace(/(from\s*)(['"])\.\.\/hooks\/useMetrics\2/g, '$1$2./stub-useMetrics.mjs$2')
    .replace(/(from\s*)(['"])\.\/RichDiffViewer\2/g, '$1$2./stub-RichDiffViewer.mjs$2');

  // The model cards live in the 'sessions' sub-tab, but activeSubTab defaults to
  // 'catalog'. Flip ONLY that initializer to reach the code under test; every
  // line of rendering logic is untouched.
  const before = code;
  code = code.replace(/setActiveSubTab\] = useState\(["']catalog["']\)/, 'setActiveSubTab] = useState("sessions")');
  check(code !== before, 'setup: could not reach the sessions tab (activeSubTab initializer not found)');
  const compOut = path.join(work, 'AgentSessionsView.mjs');
  fs.writeFileSync(compOut, code, 'utf8');

  const React = (await import('react')).default;
  const { renderToStaticMarkup } = await import('react-dom/server');
  const { AgentSessionsView } = await import(pathToFileURL(compOut).href);
  void types;

  const model = (survived, cost, total, active) => ({
    model: 'claude-sonnet-4-20250514', total_nodes: total, active_nodes: active,
    survival_rate_pct: (active * 100) / total, avg_longevity_days: 3.0,
    total_cost_usd: 12.5, total_survived_nodes: survived,
    cost_per_surviving_node: cost, run_count: 4,
  });

  const render = (m) =>
    renderToStaticMarkup(
      React.createElement(AgentSessionsView, { activeRuns: [], models: [m], loading: false, recentEvents: [] }),
    );

  // --- The defect -----------------------------------------------------------
  const noRoi = render(model(0, 0, 120, 108));
  check(
    !/\$0\.0000\s*\/\s*node/.test(noRoi),
    'a model with no ROI data (total_survived_nodes=0) must not render "$0.0000 / node" as a real ' +
      'price: the Go sentinel 0 means "never computed". The same card also states "0 / 120 nodes alive", ' +
      'so the two lines contradict each other',
  );

  // --- Control 1: real ROI data keeps its real price -------------------------
  {
    const h = render(model(50, 0.8, 200, 140));
    check(
      /\$0\.8000\s*\/\s*node/.test(h),
      'control: a model WITH roi data (0.8) must still render its real "$0.8000 / node" price',
    );
  }

  // --- Control 2: a genuinely free model that HAS ROI data stays $0.0000 ------
  {
    const h = render(model(12, 0, 200, 140));
    check(
      /\$0\.0000\s*\/\s*node/.test(h),
      'control: a genuinely free model that DOES have roi data (12 survived nodes) must keep "$0.0000 / node"',
    );
  }

  // --- Control 3: the honest no-data case still says it is unknown ------------
  {
    const h = render(model(0, 0, 120, 108));
    check(
      h.includes('0 / 120 nodes alive'),
      'control: the survived/total line must still render its real counts',
    );
  }
} catch (e) {
  blocked('unexpected harness error: ' + (e && e.stack));
}

if (failures.length > 0) {
  for (const f of failures) console.log('  - ' + f);
  cleanup();
  console.log(`FAIL: sessions-roi-sentinel (${failures.length} assertion(s))`);
  process.exit(1);
}
console.log('PASS: unknown ROI is never displayed as a real per-node price');
