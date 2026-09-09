// Runs every standalone web regression guard in this directory and fails if
// any of them exits non-zero. Exposed as `npm run regressions` and wired into
// the CI frontend job so guard regressions surface automatically instead of
// via the manual PowerShell one-liner.
//
// Contract: each guard's stdout/stderr is inherited, so its single terminal
// PASS/FAIL line stays visible in CI logs; the runner adds a per-guard status
// line and a final summary, then exits 1 if any guard failed or could not be
// spawned. Guards are discovered by listing this directory (*.mjs except this
// runner), so adding guard #12 requires no edit here. Each guard gets a hard
// 2-minute kill timeout — a hung guard must fail CI, not hang it. Guards run
// with the repo root as cwd, matching the verified manual invocation shape
// (`node web/scripts/<name>.mjs` from the root).

import { readdirSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, '..', '..');
const SELF = 'run-all.mjs';
const PER_GUARD_TIMEOUT_MS = 2 * 60 * 1000;

const guards = readdirSync(here)
  .filter((f) => f.endsWith('.mjs') && f !== SELF)
  .sort();

if (guards.length === 0) {
  console.error(`run-all: no guards found in ${here}`);
  process.exit(1);
}

console.log(`run-all: ${guards.length} guards, cwd=${repoRoot}\n`);
const failed = [];
for (const guard of guards) {
  const started = Date.now();
  const res = spawnSync(process.execPath, [path.join(here, guard)], {
    stdio: 'inherit',
    cwd: repoRoot,
    timeout: PER_GUARD_TIMEOUT_MS,
  });
  const secs = ((Date.now() - started) / 1000).toFixed(1);
  if (res.error) {
    failed.push(guard);
    console.log(`ERROR: ${guard} could not be spawned: ${res.error.message} (${secs}s)`);
  } else if (res.status !== 0 || res.signal) {
    failed.push(guard);
    console.log(
      `FAIL: ${guard} (exit ${res.status ?? 'null'}${res.signal ? `, signal ${res.signal}` : ''}, ${secs}s)`,
    );
  } else {
    console.log(`ok: ${guard} (${secs}s)`);
  }
}

console.log('');
if (failed.length > 0) {
  console.log(`run-all: ${failed.length}/${guards.length} FAILED:`);
  for (const f of failed) console.log(`  - ${f}`);
  process.exit(1);
}
console.log(`run-all: ${guards.length}/${guards.length} guards passed`);
