// Regression test (round 95): the CodeAtlas fragile-count expression
// (web/src/components/CodeAtlas.tsx, the atlas?.packages?.reduce site) must
// survive the summary-mode wire shape that GET /api/atlas?summary=true
// actually serves. core.AtlasPackage carries json:"files,omitempty" and
// Handlers.Atlas sets pkg.Files = nil in summary mode (handlers.go:420-421),
// so the payload's packages have NO "files" key — while the component used to
// dereference p.files.filter(...) unguarded.
//
// Defect (pre-fix): the expression threw
//   TypeError: Cannot read properties of undefined (reading 'filter')
// on a summary-mode payload. Fix: `files?: AtlasFile[]` in the type mirror
// (web/src/types/index.ts) + `(p.files ?? [])` guards at every .files
// dereference in CodeAtlas.tsx.
//
// The expression is SLICED VERBATIM from the production file at runtime, so
// this harness always tests the code that actually ships (house pattern:
// atlas-symbol-sort-regression.mjs).
//
// Run: node web/scripts/atlas-summary-files-crash-regression.mjs
// Exit 0 = pass, 1 = fail.
import path from 'node:path';
import fs from 'node:fs';

const scriptDir = path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
const root = path.resolve(scriptDir, '..', '..');
const componentPath = path.join(root, 'web', 'src', 'components', 'CodeAtlas.tsx');
const source = fs.readFileSync(componentPath, 'utf8');

// --- Setup: slice the production fragile-count expression verbatim ----------
// Anchor on the stable prefix so the slice works against both the pre-fix
// (`acc + p.files.filter(...)`) and post-fix (`acc + (p.files ?? []).filter(...)`)
// forms of the line.
const anchor = 'atlas?.packages?.reduce((acc, p) => acc +';
const occurrences = source.split(anchor).length - 1;
if (occurrences !== 1) {
  console.log(`FAIL: setup — expected exactly 1 fragile-count anchor in CodeAtlas.tsx, found ${occurrences}`);
  process.exit(1);
}
const start = source.indexOf(anchor);
const endMarker = '?? 0}';
const end = source.indexOf(endMarker, start);
if (end < 0) {
  console.log('FAIL: setup — expression terminator "?? 0}" not found after the anchor');
  process.exit(1);
}
const expr = source.slice(start, end + endMarker.length - 1); // drop the trailing '}'
let fn;
try {
  fn = new Function('atlas', `return (${expr});`);
} catch (err) {
  console.log('FAIL: setup — sliced expression does not compile: ' + err.message);
  process.exit(1);
}

// --- Fixtures ---------------------------------------------------------------
// Summary-mode wire shape — exactly what the server emits for
// GET /api/atlas?summary=true (packages carry file_count /
// fragile_files_count / avg_health_score but NO "files" key; proven by the
// round-95 Go wire probe marshaling core.AtlasSnapshot after the handler's
// pkg.Files = nil transform).
const summaryAtlas = {
  repo: 'probe',
  generated_at: '0001-01-01T00:00:00Z',
  is_monorepo: false,
  total_packages: 1,
  packages: [
    {
      path: 'internal/ast',
      name: 'ast',
      file_count: 2,
      fragile_files_count: 1,
      avg_health_score: 85,
      total_loc: 20,
      is_fragile: true,
    },
  ],
  total_files: 2,
  total_loc: 20,
  total_nodes: 4,
  index_status: {
    is_indexing: false,
    total_discovered: 0,
    eligible_files: 0,
    indexed_files: 0,
    skipped_files: 0,
    failed_files: 0,
    percentage: 0,
    duration_ms: 0,
    last_indexed_at: '',
  },
};

// Full-mode control: files present, one fragile of two (must count 1 —
// proves the guards do not regress the normal path).
const fullAtlas = {
  ...summaryAtlas,
  packages: [
    {
      ...summaryAtlas.packages[0],
      files: [
        { path: 'a.go', is_fragile: true, recent_thrashing_count: 5 },
        { path: 'b.go', is_fragile: false, recent_thrashing_count: 0 },
      ],
    },
  ],
};

const fails = [];
const check = (ok, msg) => {
  if (!ok) {
    fails.push(msg);
    console.log('FAIL: ' + msg);
  }
};

// (a) THE contract: a summary-mode payload must yield a number, not throw.
let summaryCount;
try {
  summaryCount = fn(summaryAtlas);
  check(typeof summaryCount === 'number', `summary-mode payload must evaluate to a number, got ${String(summaryCount)}`);
} catch (err) {
  check(false, `summary-mode payload crashes the production expression: ${err.constructor.name}: ${err.message}`);
}

// (b) Control: a full-mode payload counts 1 fragile file.
let fullCount;
try {
  fullCount = fn(fullAtlas);
  check(fullCount === 1, `full-mode control must count 1 fragile file, got ${String(fullCount)}`);
} catch (err) {
  check(false, `full-mode control crashed: ${err.constructor.name}: ${err.message}`);
}

if (fails.length > 0) {
  console.log(`FAIL: ${fails.length} check(s) failed — atlas summary-mode files contract`);
  process.exit(1);
}
console.log(
  'PASS: CodeAtlas fragile-count expression survives the summary-mode wire shape (no files key) and still counts full-mode payloads',
);
