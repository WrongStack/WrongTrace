// Regression test: a dismissed WrongStack banner must not flash on reload.
//
// Defect (pre-fix): WrongStackBanner kept its dismissed flag in
// `useState(false)` and read localStorage in a `useEffect`. Effects run AFTER
// the first paint, so a user who had dismissed the banner got it back for one
// frame on every reload, then watched it disappear. The stored value was known
// at mount time; the component simply did not look at it until too late.
//
// Contract: the FIRST render must already honour the stored flag — a dismissed
// user sees the restore affordance, never the banner.
//
// The harness stubs a browser-like first render (`window` + `localStorage`) and
// calls renderToStaticMarkup once, which is exactly the pre-effect state a
// browser paints. Server-rendering the REAL component with the repo's own vite;
// no network or context dependencies.
//
// Run: node web/scripts/wrongstack-banner-flash-regression.mjs
// Exit 0 = pass, 1 = fail.
import { createRequire } from 'node:module';
import { pathToFileURL, fileURLToPath } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '..', '..');
const webRequire = createRequire(pathToFileURL(path.join(root, 'web', 'package.json')));
const vite = webRequire('vite');
const React = webRequire('react');
const { renderToStaticMarkup } = webRequire('react-dom/server');

const failures = [];
const check = (cond, msg) => { if (!cond) failures.push(msg); };

const BANNER_MARKER = 'Discover WrongStack';
const RESTORE_MARKER = 'Show WrongStack Ecosystem';

const tmpName = `.tmp-wrongstack-banner-${process.pid}`;
const tmpComp = path.join(here, `${tmpName}.mjs`);
try {
  const src = fs.readFileSync(
    path.join(root, 'web', 'src', 'components', 'WrongStackBanner.tsx'),
    'utf8',
  );
  const t = await vite.transformWithOxc(src, 'WrongStackBanner.tsx');
  if (t.errors && t.errors.length > 0) {
    console.log('FAIL: transform errors: ' + JSON.stringify(t.errors));
    process.exit(1);
  }
  fs.writeFileSync(tmpComp, t.code);
  const mod = await import(pathToFileURL(tmpComp).href);
  // Delete IMMEDIATELY after import, before any assertion can call
  // process.exit(). process.exit() skips `finally`, so a temp file removed only
  // there survives a FAIL run — and run-all.mjs auto-discovers *.mjs in this
  // directory, so the leak would become a phantom "guard" on the next run.
  fs.rmSync(tmpComp, { force: true });
  const { WrongStackBanner } = mod;

  // A browser-like first render: window exists and localStorage answers. The
  // component is rendered exactly once, so no effect ever runs — this is the
  // frame the user actually sees first.
  function firstRender(stored) {
    globalThis.window = globalThis;
    globalThis.localStorage = {
      getItem: (k) => (k === 'wrongstack_banner_dismissed' ? stored : null),
      setItem: () => {},
      removeItem: () => {},
    };
    return renderToStaticMarkup(React.createElement(WrongStackBanner));
  }

  // --- Control: nothing stored, so the banner is the correct first render.
  const fresh = firstRender(null);
  check(fresh.includes(BANNER_MARKER),
    'control: with nothing stored the banner must render on the first pass');

  // --- The defect: a stored dismissal must already be honoured at mount.
  const dismissed = firstRender('true');
  check(!dismissed.includes(BANNER_MARKER),
    'a stored dismissal must not render the banner on the first pass (this is the reload flash)');
  check(dismissed.includes(RESTORE_MARKER),
    'a stored dismissal must render the restore affordance on the first pass');

  // --- Control: the pre-effect state must not be hardcoded off either — a user
  // who restored the banner must see it again immediately.
  const restored = firstRender('false');
  check(restored.includes(BANNER_MARKER),
    'control: a stored non-dismissal must render the banner on the first pass');

  if (failures.length > 0) {
    for (const f of failures) console.log('  - ' + f);
    console.log(`FAIL: wrongstack-banner-flash (${failures.length} assertion(s))`);
    process.exit(1);
  }
  console.log('PASS: a dismissed WrongStack banner is absent from the first render');
} finally {
  fs.rmSync(tmpComp, { force: true });
  delete globalThis.localStorage;
  delete globalThis.window;
}
