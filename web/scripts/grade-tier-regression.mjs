// Regression test: the Model Intelligence Matrix quality-tier badge must stay
// consistent with the ROI value rendered on the same row.
//
// Defect (pre-fix): getGradeBadge(90, 0, 0.05) returned "S-TIER" because the
// Go db.ModelComparison only computes cost_per_surviving_node when
// total_survived_nodes > 0 and otherwise leaves it 0, so a model with NO proven
// ROI arrives with costPerNode == 0 and trivially satisfies
// "costPerNode <= blendedCost" (0 <= 0.05). Any model onboarded < 14 days ago
// (all its nodes new, so survived_count == 0) with a high active/total survival
// therefore got the TOP tier badge while the ROI column on that very row
// rendered "—" (no data). A headline quality signal that contradicts its own
// source value, the same class fixed four times in the CLI.
//
// Contract (web/src/lib/grade.ts): the S-TIER "best ROI" path is gated on
// having real ROI data (total_survived_nodes > 0). A model with no ROI data is
// graded on survival alone (A/B/C), never promoted to top tier on a value that
// means "unknown". Genuinely cheap and genuinely free models that DO have ROI
// data keep S-TIER (controls below).
//
// Run: node web/scripts/grade-tier-regression.mjs
// Exit 0 = pass, 1 = fail. Transpiles the REAL helper with the repo's own vite.
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';

const scriptDir = path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
const root = path.resolve(scriptDir, '..', '..');

const webRequire = createRequire(pathToFileURL(path.join(root, 'web', 'package.json')));
const vite = webRequire('vite');

const src = fs.readFileSync(path.join(root, 'web', 'src', 'lib', 'grade.ts'), 'utf8');
const transformed = await vite.transformWithOxc(src, 'grade.ts');
if (transformed.errors && transformed.errors.length > 0) {
  console.log('FAIL: transform errors: ' + JSON.stringify(transformed.errors));
  process.exit(1);
}
const { getGradeBadge } = await import(
  'data:text/javascript;base64,' + Buffer.from(transformed.code).toString('base64')
);

const failures = [];
const check = (cond, msg) => { if (!cond) failures.push(msg); };

// getGradeBadge(survivalRate, costPerNode, blendedCost, hasRoiData)
// hasRoiData mirrors the row's `total_survived_nodes > 0`, which is exactly the
// condition under which the Go side produces a real cost_per_surviving_node
// (and under which the ROI column stops rendering "—").

// --- The defect: a new (<14 day) high-survival model with NO ROI data -------
{
  const badge = getGradeBadge(90, 0, 0.05, false);
  check(
    badge.label !== 'S-TIER',
    `a model with no ROI data (hasRoiData=false, cost 0) must not be promoted to S-TIER; got ${badge.label}`,
  );
  // Graded on survival alone: 90 >= 70 -> A-TIER (honest, not top).
  check(badge.label === 'A-TIER', `no-ROI 90% survival should grade A-TIER (on survival alone), got ${badge.label}`);
}

// --- Control 1: genuinely cheap model WITH ROI data keeps S-TIER -----------
{
  const badge = getGradeBadge(90, 0.02, 0.05, true);
  check(badge.label === 'S-TIER', `cheap model with ROI data must stay S-TIER, got ${badge.label}`);
}

// --- Control 2: genuinely free provider WITH ROI data keeps S-TIER ---------
{
  const badge = getGradeBadge(90, 0, 0.05, true);
  check(badge.label === 'S-TIER', `free model with ROI data must stay S-TIER, got ${badge.label}`);
}

// --- Control 3: cheap but low-survival is NOT S-TIER (existing behavior) ----
{
  const badge = getGradeBadge(60, 0.01, 0.05, true);
  check(badge.label === 'B-TIER', `60% survival should be B-TIER regardless of cost, got ${badge.label}`);
}

// --- Boundary: no-ROI at exactly the 85 survival threshold is still not S ---
{
  const badge = getGradeBadge(85, 0, 0.05, false);
  check(badge.label !== 'S-TIER', `no-ROI 85% survival must not be S-TIER; got ${badge.label}`);
}

// --- Source check: the component uses the shared helper ---------------------
const viewer = fs.readFileSync(path.join(root, 'web', 'src', 'components', 'ModelIntelligenceMatrix.tsx'), 'utf8');
check(/getGradeBadge/.test(viewer), 'ModelIntelligenceMatrix.tsx must use getGradeBadge from lib/grade');
check(/from '\.\.\/lib\/grade'/.test(viewer), 'ModelIntelligenceMatrix.tsx must import getGradeBadge from lib/grade');
// The call site must pass the ROI-data fact, else a free model is demoted.
check(/getGradeBadge\([^)]*total_survived_nodes\s*>\s*0/s.test(viewer) || /hasRoiData|total_survived_nodes\s*>\s*0[^)]*\)/s.test(viewer),
  'ModelIntelligenceMatrix.tsx must pass whether the row has ROI data (total_survived_nodes > 0)');

if (failures.length > 0) {
  for (const f of failures) console.log('  - ' + f);
  console.log(`FAIL: grade-tier (${failures.length} assertion(s))`);
  process.exit(1);
}
console.log('PASS: quality tier never claims top-tier on an unknown ROI value');
