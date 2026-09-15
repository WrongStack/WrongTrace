// Regression test: the Dashboard's adaptive WebSocket coalescing window must
// actually widen under sustained traffic.
//
// Defect (pre-fix): the frame counter was reset to 0 when the timer fired, and
// the next window was armed by the very next frame -- so the count at arming
// time was always 1 and the delay was always 250ms. The 500ms / 1000ms tiers
// were unreachable; a busy agent run still triggered four refresh storms/sec.
//
// Contract (web/src/lib/wsCoalesce.ts): nextCoalesceMs(prevWindowFrames,
// msSincePrevWindowStart) chooses the delay from the PREVIOUS window's frames
// spread over the time since that window started, so sustained traffic widens
// the window and a quiet gap decays it back to 250ms. Dashboard.tsx must call
// it with the carried-over window (source check below).
//
// Run: node web/scripts/ws-coalesce-window-regression.mjs
// Exit 0 = pass, 1 = fail. Transpiles the REAL helper with the repo's own vite.
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';

const scriptDir = path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
const root = path.resolve(scriptDir, '..', '..');

const webRequire = createRequire(pathToFileURL(path.join(root, 'web', 'package.json')));
const vite = webRequire('vite');

const src = fs.readFileSync(path.join(root, 'web', 'src', 'lib', 'wsCoalesce.ts'), 'utf8');
const transformed = await vite.transformWithOxc(src, 'wsCoalesce.ts');
if (transformed.errors && transformed.errors.length > 0) {
  console.log('FAIL: transform errors: ' + JSON.stringify(transformed.errors));
  process.exit(1);
}
const { nextCoalesceMs } = await import('data:text/javascript;base64,' + Buffer.from(transformed.code).toString('base64'));

const failures = [];
const check = (cond, msg) => { if (!cond) failures.push(msg); };
const eq = (got, want, msg) => check(got === want, `${msg}: expected ${want}, got ${got}`);

eq(nextCoalesceMs(0, 0), 250, 'first window ever');
eq(nextCoalesceMs(1, 300), 250, 'light traffic');
eq(nextCoalesceMs(3, 260), 500, '~11.5 frames/s');
eq(nextCoalesceMs(8, 260), 1000, '~30 frames/s');
eq(nextCoalesceMs(24, 1010), 1000, 'sustained 24 frames/s stays wide');
eq(nextCoalesceMs(24, 6000), 250, 'busy window followed by a quiet gap decays');
eq(nextCoalesceMs(Number.NaN, 100), 250, 'NaN frames');

// Simulate the Dashboard loop: frames at a steady rate; each window is armed by
// the first frame after the previous flush, choosing its delay from the
// previous window. Under 40 frames/s the window must reach 1000ms; under
// 2 frames/s it must stay at 250ms.
function simulate(framesPerSec, seconds) {
  const gap = 1000 / framesPerSec;
  const delays = [];
  let prev = { frames: 0, startedAt: 0 };
  let windowStart = 0, frames = 0, timerEnd = null;
  for (let t = 0; t < seconds * 1000; t += gap) {
    if (timerEnd !== null && t >= timerEnd) {
      prev = { frames, startedAt: windowStart };
      frames = 0;
      timerEnd = null;
    }
    frames += 1;
    if (timerEnd === null) {
      const d = nextCoalesceMs(prev.frames, t - prev.startedAt);
      delays.push(d);
      windowStart = t;
      timerEnd = t + d;
    }
  }
  return delays;
}
const busy = simulate(40, 10);
check(busy.slice(-3).every((d) => d === 1000), `40 frames/s should settle at 1000ms, tail = ${busy.slice(-3)}`);
const medium = simulate(10, 10);
check(medium.slice(-3).every((d) => d === 500), `10 frames/s should settle at 500ms, tail = ${medium.slice(-3)}`);
const quiet = simulate(2, 10);
check(quiet.every((d) => d === 250), `2 frames/s should stay at 250ms, got ${[...new Set(quiet)]}`);

const dashboard = fs.readFileSync(path.join(root, 'web', 'src', 'pages', 'Dashboard.tsx'), 'utf8');
check(/nextCoalesceMs\(/.test(dashboard), 'Dashboard.tsx must choose its window via nextCoalesceMs');
check(!/wsBurstRef\.current\s*>\s*20\s*\?/.test(dashboard), 'Dashboard.tsx must not pick the delay from the just-reset burst counter');

if (failures.length > 0) {
  for (const f of failures) console.log('  - ' + f);
  console.log(`FAIL: ws-coalesce-window (${failures.length} assertion(s))`);
  process.exit(1);
}
console.log('PASS: ws-coalesce-window widens under sustained traffic and decays when quiet');
