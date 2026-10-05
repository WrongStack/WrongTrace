// Regression: web/src/types/index.ts isJunkModel must mirror the Go predicate
// models.IsJunkModel (internal/models/registry.go).
//
// Defect (pre-fix): the TS junk set carried a leading 'unknown' that the Go
// switch does not contain. Go is the authority here, and it deliberately does
// NOT treat bare "unknown" as junk: its own queries GENERATE that attribution
// bucket (`COALESCE(r.model_name, 'unknown')`, internal/db/queries.go) and the
// exported report filters rows with the same Go predicate (internal/report/
// report.go). The dashboard filters its model rows through this TS predicate
// (Dashboard.tsx, ROIAnalysis.tsx, ModelLeaderboard.tsx), so the extra entry
// made the UI silently hide a model row the API and the exported report both
// include — a mirror drift, the class the type-mirror gate exists to catch.
//
// Contract: isJunkModel("unknown") === false; the schema placeholders
// ("unknown-model", "unknown_model", "unknown-provider") stay junk; real model
// names stay non-junk.
//
// Run: node web/scripts/junk-model-unknown-mirror-regression.mjs
// Exit 0 = pass, 1 = fail. Transpiles the REAL module with the repo's own vite.
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';

const scriptDir = path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
const root = path.resolve(scriptDir, '..', '..');

const webRequire = createRequire(pathToFileURL(path.join(root, 'web', 'package.json')));
const vite = webRequire('vite');

const srcPath = path.join(root, 'web', 'src', 'types', 'index.ts');
const src = fs.readFileSync(srcPath, 'utf8');
const transformed = await vite.transformWithOxc(src, 'index.ts');
if (transformed.errors && transformed.errors.length > 0) {
  console.log('FAIL: transform errors: ' + JSON.stringify(transformed.errors));
  process.exit(1);
}
const { isJunkModel } = await import(
  'data:text/javascript;base64,' + Buffer.from(transformed.code).toString('base64')
);

const failures = [];
const check = (cond, msg) => { if (!cond) failures.push(msg); };

// The exact drift: the Go queries generate this bucket, so it is data, not junk.
check(
  isJunkModel('unknown') === false,
  'isJunkModel("unknown") must be false: the Go queries generate that attribution bucket and the exported report includes it',
);
// Controls: the genuine schema placeholders must stay junk...
for (const placeholder of ['unknown-model', 'unknown_model', 'unknown-provider']) {
  check(isJunkModel(placeholder) === true, `isJunkModel(${placeholder}) must stay true (schema placeholder)`);
}
// ...and real model names must stay non-junk, so the control cannot pass on a
// predicate that rejects everything.
check(isJunkModel('claude-3-7-sonnet') === false, 'real model must not be junk');
check(isJunkModel('MiniMax-M2.7-highspeed') === false, 'real model must not be junk');
// The empty/absent shapes still have their documented answer.
check(isJunkModel(undefined) === true && isJunkModel('') === true, 'absent or empty names are junk');

// Source check: the junk set literal must not carry a standalone 'unknown'
// entry again — the Go switch enumerates unknown-model/unknown_model/
// unknown-model-detected/unknown-provider and never bare "unknown".
const setLiteral = src.slice(src.indexOf('const junkSet = new Set(['), src.indexOf(']', src.indexOf('const junkSet = new Set([')));
check(
  !/['"]unknown['"]\s*,/.test(setLiteral) && !/['"]unknown['"]\s*\]/.test(setLiteral),
  "the junk set must not list a bare 'unknown' entry (mirror of Go models.IsJunkModel)",
);

if (failures.length > 0) {
  for (const f of failures) console.log('  - ' + f);
  console.log(`FAIL: junk-model unknown mirror (${failures.length} assertion(s))`);
  process.exit(1);
}
console.log('PASS: isJunkModel mirrors the Go predicate on the "unknown" attribution bucket');
