// Regression test: ModelFrictionMatrix must not render the Go top_friction_pair
// sentinel as a statement about collisions.
//
// Contract (internal/db/queries.go, ModelFrictionMatrix):
//     func frictionPair(author, overwriter string) bool {
//         return author != overwriter && author != "unknown" && overwriter != "unknown"
//     }
//   TopFrictionPair is assigned only for an edge passing frictionPair
//   (queries.go:1955-1961), while TotalCollisions sums EVERY edge
//   (queries.go:1855). So top_friction_pair == "" means "no qualifying
//   INTER-MODEL pair" — it does NOT mean "there were no collisions".
//
// Defect (pre-fix): the KPI card rendered that sentinel as the literal string
// "No collisions yet", in the same strip that shows "Total Overwrites: N". A
// solo agent rewriting its own nodes produces exactly this shape — self-thrash
// edges are real, counted collisions that frictionPair deliberately skips — so
// the panel printed "Total Overwrites: 2" beside "No collisions yet".
//
// The fixture below is REAL producer output (captured from db.Store with one
// model modifying its own symbol), not a hand-written shape.
//
// Contract (web/src/components/ModelFrictionMatrix.tsx): the empty pair is
// reported as "No inter-model friction" when collisions exist, and the honest
// "No collisions yet" is reserved for a genuinely empty report (controls).
//
// Run: node web/scripts/friction-top-pair-regression.mjs
// Exit 0 = pass, 1 = fail, 2 = blocked. Renders the REAL component via the
// repo's own vite; only the network hook and a presentational child are stubbed.
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
const work = fs.mkdtempSync(path.join(root, '.temp_files', 'wt-friction-top-pair-'));
const cleanup = () => { try { fs.rmSync(work, { recursive: true, force: true }); } catch {} };
process.on('exit', cleanup);
const blocked = (why) => { console.log('BLOCKED: ' + why); cleanup(); process.exit(2); };

// REAL db.Store.ModelFrictionMatrix output for a self-thrash-only project.
const PRODUCER_REPORT = {
  edges: [
    { author_model: 'm1', overwriter_model: 'm1', conflict_count: 2, lines_modified: 0, lines_deleted: 0, is_self_thrash: true, wasted_cost_usd: 0 },
  ],
  recent_collisions: [
    { event_id: 'ev-3', file_path: 'f.go', node_signature: 'func:DoTask', action: 'MODIFIED',
      author_model: 'm1', author_run_id: 'run-a', author_time: '2026-09-29T04:02:34Z',
      overwriter_model: 'm1', overwriter_run_id: 'run-a', overwriter_time: '2026-09-29T04:03:34Z',
      time_delta_seconds: 60, added_lines: 0, deleted_lines: 0, diff_snippet: '', is_cross_agent: false },
    { event_id: 'ev-2', file_path: 'f.go', node_signature: 'func:DoTask', action: 'MODIFIED',
      author_model: 'm1', author_run_id: 'run-a', author_time: '2026-09-29T04:01:34Z',
      overwriter_model: 'm1', overwriter_run_id: 'run-a', overwriter_time: '2026-09-29T04:02:34Z',
      time_delta_seconds: 60, added_lines: 0, deleted_lines: 0, diff_snippet: '', is_cross_agent: false },
  ],
  total_collisions: 2,
  cross_agent_ratio_pct: 0,
  top_friction_pair: '',
};

const failures = [];
const check = (cond, msg) => { if (!cond) failures.push(msg); };

try {
  fs.symlinkSync(path.join(webDir, 'node_modules'), path.join(work, 'node_modules'), process.platform === 'win32' ? 'junction' : 'dir');
  if (!fs.existsSync(path.join(work, 'node_modules'))) blocked('could not link web/node_modules');

  // Network boundary (useModelFriction) and presentational child only.
  fs.writeFileSync(
    path.join(work, 'stub-useMetrics.mjs'),
    'let report = null;\nexport function __setReport(r) { report = r; }\n' +
      'export function useModelFriction() { return { data: report, isLoading: false }; }\n',
  );
  fs.writeFileSync(path.join(work, 'stub-RichDiffViewer.mjs'), 'export function RichDiffViewer() { return null; }\n');

  const src = fs.readFileSync(path.join(webDir, 'src', 'components', 'ModelFrictionMatrix.tsx'), 'utf8');
  const out = await vite.transformWithOxc(src, 'ModelFrictionMatrix.tsx');
  if (out.errors && out.errors.length > 0) blocked('transform errors: ' + JSON.stringify(out.errors));
  const compOut = path.join(work, 'ModelFrictionMatrix.mjs');
  fs.writeFileSync(
    compOut,
    out.code
      .replace(/(from\s*)(['"])\.\.\/hooks\/useMetrics\2/g, '$1$2./stub-useMetrics.mjs$2')
      .replace(/(from\s*)(['"])\.\/RichDiffViewer\2/g, '$1$2./stub-RichDiffViewer.mjs$2'),
    'utf8',
  );

  const React = (await import('react')).default;
  const { renderToStaticMarkup } = await import('react-dom/server');
  const { __setReport } = await import(pathToFileURL(path.join(work, 'stub-useMetrics.mjs')).href);
  const { ModelFrictionMatrix } = await import(pathToFileURL(compOut).href);

  const render = (report) => {
    __setReport(report);
    return renderToStaticMarkup(React.createElement(ModelFrictionMatrix, {}));
  };

  // --- The defect -----------------------------------------------------------
  const html = render(PRODUCER_REPORT);
  check(
    !html.includes('No collisions yet'),
    `the Top Friction Vector card must not claim "No collisions yet" when the report carries ` +
      `${PRODUCER_REPORT.total_collisions} collision(s): the empty top_friction_pair is the ` +
      `"no qualifying inter-model pair" sentinel, not "no collisions"`,
  );

  // --- Control 1: a real top pair still renders -----------------------------
  {
    const pair = 'm1 ➔ m2 (7 collisions)';
    const h = render({ ...PRODUCER_REPORT, top_friction_pair: pair });
    check(h.includes(pair), 'control: a real top_friction_pair must still be rendered');
    check(!h.includes('No collisions yet'), 'control: the empty-state text must not appear when a pair exists');
  }

  // --- Control 2: a genuinely empty report may still say "No collisions yet" --
  {
    const empty = { edges: [], recent_collisions: [], total_collisions: 0, cross_agent_ratio_pct: 0, top_friction_pair: '' };
    const h = render(empty);
    check(
      h.includes('No collisions yet'),
      'control: a report with no collisions and no pair must still say "No collisions yet"',
    );
  }
} catch (e) {
  blocked('unexpected harness error: ' + (e && e.stack));
}

if (failures.length > 0) {
  for (const f of failures) console.log('  - ' + f);
  cleanup();
  console.log(`FAIL: friction-top-pair (${failures.length} assertion(s))`);
  process.exit(1);
}
console.log('PASS: the top-pair sentinel is never rendered as "no collisions"');
