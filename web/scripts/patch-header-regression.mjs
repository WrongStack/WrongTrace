// Regression test: the "Download as .patch file" export in RichDiffViewer must
// produce a patch whose hunk header actually describes its body.
//
// Defect (pre-fix): handleDownloadPatch hardcoded the header
//   `--- a/<file>\n+++ b/<file>\n@@ -1 +1 @@\n`
// "1 old line -> 1 new line" for EVERY diff, but the body it prepends to is
// internal/ast's multi-line diff_snippet. A unified-diff reader bounds the body
// by the header's declared counts, so any snippet with more than one line made
// the counts disagree and git rejected the whole file:
//   error: corrupt patch at exported.patch:6
// The feature literally labelled "Download as .patch file" could therefore
// never emit an applicable patch. Fixed in web/src/lib/patch.ts (buildUnifiedPatch),
// which derives old/new counts from the body's own "+ "/"- "/"context lines.
//
// Contract pinned here:
//   1. buildUnifiedPatch's header counts EQUAL the body's actual classification
//      (a "+" line is new-only, a "-" line is old-only, any other line is
//      context present on both sides) -- the invariant that makes the envelope
//      structurally valid. Checked deterministically, no git required.
//   2. Real `git apply --check` on the produced patch does NOT report
//      "corrupt" (it may report a benign content mismatch for a fragment; the
//      defect was "corrupt", the count contradiction).
//   3. RichDiffViewer.tsx no longer hardcodes the buggy "@@ -1 +1 @@" header
//      and does call buildUnifiedPatch (source check).
//
// Run: node web/scripts/patch-header-regression.mjs
// Exit 0 = pass, 1 = fail. Transpiles the REAL helper with the repo's own vite.
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import fs from 'node:fs';
import os from 'node:os';

const scriptDir = path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
const root = path.resolve(scriptDir, '..', '..');

const webRequire = createRequire(pathToFileURL(path.join(root, 'web', 'package.json')));
const vite = webRequire('vite');

const patchSrc = fs.readFileSync(path.join(root, 'web', 'src', 'lib', 'patch.ts'), 'utf8');
const transformed = await vite.transformWithOxc(patchSrc, 'patch.ts');
if (transformed.errors && transformed.errors.length > 0) {
  console.log('FAIL: transform errors: ' + JSON.stringify(transformed.errors));
  process.exit(1);
}
const { buildUnifiedPatch } = await import(
  'data:text/javascript;base64,' + Buffer.from(transformed.code).toString('base64')
);

const failures = [];
const check = (cond, msg) => { if (!cond) failures.push(msg); };
const eq = (got, want, msg) => check(got === want, `${msg}: expected ${want}, got ${got}`);

// Classify a body line the same way the header must: "+" new-only, "-" old-only,
// anything else context (both sides). Mirrors buildUnifiedPatch's counting.
function classify(snippet) {
  let oldCount = 0;
  let newCount = 0;
  for (const line of snippet.split('\n')) {
    if (line.startsWith('-')) oldCount += 1;
    else if (line.startsWith('+')) newCount += 1;
    else { oldCount += 1; newCount += 1; }
  }
  return { oldCount, newCount };
}

function headerCounts(patch) {
  const m = patch.match(/^@@ -(\d+),(\d+) \+(\d+),(\d+) @@$/m);
  if (!m) return null;
  return { oldStart: Number(m[1]), oldCount: Number(m[2]), newStart: Number(m[3]), newCount: Number(m[4]) };
}

// --- The 5 contract cases, all in internal/ast's DISPLAY prefix format -------
const snippetCases = [
  ['single added line', '+ added one line'],
  ['multi-line added (the pre-fix shape)', '+ func Alpha(a int) int {\n+\treturn a + 1\n+}'],
  ['added with context', '  ctx line\n+ inserted\n+ more'],
  ['add and delete', '- removed\n  kept\n+ inserted'],
  ['omission markers present', '+ new body\n  … unchanged lines omitted …\n- old body'],
];

for (const [name, snippet] of snippetCases) {
  const patch = buildUnifiedPatch(snippet, 'internal/demo/alpha.go');
  const counts = headerCounts(patch);
  if (!counts) {
    failures.push(`${name}: header missing or malformed -> ${JSON.stringify(patch.split('\n')[2])}`);
    continue;
  }
  const want = classify(snippet);
  eq(counts.oldCount, want.oldCount, `${name}: declared old count`);
  eq(counts.newCount, want.newCount, `${name}: declared new count`);
  // Pre-fix behaviour on this same case would have been old=1,new=1.
  check(patch.startsWith('--- a/internal/demo/alpha.go\n+++ b/internal/demo/alpha.go\n'),
    `${name}: envelope must keep the a/ b/ file headers`);
}

// --- Real git corroboration: the produced patch must not be "corrupt" -------
function gitSaysCorrupt(patch) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'patchguard-'));
  try {
    for (const args of [['init', '-q'], ['config', 'user.email', 'p@e.com'], ['config', 'user.name', 'p']]) {
      spawnSync('git', args, { cwd: dir });
    }
    const pf = path.join(dir, 'p.patch');
    fs.writeFileSync(pf, patch);
    const r = spawnSync('git', ['apply', '--check', pf], { cwd: dir, encoding: 'utf8' });
    const out = (r.stdout || '') + (r.stderr || '');
    return { corrupt: /corrupt/i.test(out), out };
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

for (const [name, snippet] of snippetCases) {
  const patch = buildUnifiedPatch(snippet, 'internal/demo/alpha.go');
  const { corrupt, out } = gitSaysCorrupt(patch);
  check(!corrupt, `${name}: git must not report a corrupt patch; got: ${out.trim()}`);
}

// Pre-fix control: the OLD hardcoded header on a multi-line body IS corrupt.
// If git somehow stopped flagging it, this guard's premise is void.
{
  const bugPatch = '--- a/internal/demo/alpha.go\n+++ b/internal/demo/alpha.go\n@@ -1 +1 @@\n+ a\n+ b\n+ c\n';
  const { corrupt } = gitSaysCorrupt(bugPatch);
  check(corrupt, 'pre-fix "@@ -1 +1 @@" header over a 3-line body must be reported corrupt (guards the premise)');
}

// --- Source check: component must use the helper, not the hardcoded header ----
const viewer = fs.readFileSync(path.join(root, 'web', 'src', 'components', 'RichDiffViewer.tsx'), 'utf8');
check(!/@@ -1 \+1 @@/.test(viewer), 'RichDiffViewer.tsx must not hardcode the "@@ -1 +1 @@" header');
check(/buildUnifiedPatch\(/.test(viewer), 'RichDiffViewer.tsx must build the patch via buildUnifiedPatch');

if (failures.length > 0) {
  for (const f of failures) console.log('  - ' + f);
  console.log(`FAIL: patch-header (${failures.length} assertion(s))`);
  process.exit(1);
}
console.log('PASS: patch-header export declares a hunk header that matches its body');
