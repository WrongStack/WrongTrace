// Regression test: the WrongStack session badge must not invent a count the Go
// producer deliberately withheld.
//
// Producer (internal/core/projects.go, ScanAgentSessions):
//     if sessCnt > 0 { counts["wrongstack"] = sessCnt }
//     ...
//     if counts["wrongstack"] == 0 && dirExists(filepath.Join(root, ".wrongstack")) {
//         counts["wrongstack"] = 1
//     }
// DiscoveredSessions is map[string]int with no omitempty, so an absent KEY is
// how the producer says "I could not claim any session". It omits the key
// whenever no .wrongstack directory exists under the root, and omits the whole
// block when homeDir is empty. It sets 1 ITSELF, but only behind that
// dirExists() guard.
//
// Defect (pre-fix): the badge read {sessions.wrongstack || 1} — a client-side
// duplicate of the producer's fallback-to-1 rule with the dirExists() condition
// dropped. So the one case the producer deliberately declined to claim is
// exactly the case the client invents. A project with no WrongStack presence
// renders "1 sessions" inside a panel headed "Discovered Coding Agent Sessions"
// / "Auto-detected without manual paths" — a fabricated measurement advertised
// as auto-detected. All eleven sibling badges use || 0; only the product's own
// ecosystem badge could not show a zero.
//
// Contract: the badge renders exactly what the producer sent. The producer's
// conditional 1 still displays (the key arrives as 1), while an absent key
// renders 0 like every sibling.
//
// Run: node web/scripts/project-identity-wrongstack-count-regression.mjs
// Exit 0 = pass, 1 = fail. Server-renders the REAL component with the repo's own
// vite; it has no network or context dependencies, so nothing is stubbed.
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

const tmpName = `.tmp-proj-identity-${process.pid}`;
const tmpComp = path.join(here, `${tmpName}.mjs`);
try {
  const src = fs.readFileSync(
    path.join(root, 'web', 'src', 'components', 'ProjectIdentityModal.tsx'),
    'utf8',
  );
  const t = await vite.transformWithOxc(src, 'ProjectIdentityModal.tsx');
  if (t.errors && t.errors.length > 0) {
    console.log('FAIL: transform errors: ' + JSON.stringify(t.errors));
    process.exit(1);
  }
  fs.writeFileSync(tmpComp, t.code);
  const mod = await import(pathToFileURL(tmpComp).href);
  // Delete IMMEDIATELY after import, before any assertion can call
  // process.exit(). process.exit() skips `finally`, so a temp file removed only
  // there survives a FAIL run — and run-all.mjs auto-discovers every *.mjs in
  // this directory, so the leak silently becomes a phantom "guard" on the next
  // run. Cleanup is idempotent, so the finally below is only a safety net for
  // a failure that happens before this line.
  fs.rmSync(tmpComp, { force: true });
  const { ProjectIdentityModal } = mod;

  const baseProject = {
    id: 'p1',
    name: 'demo',
    description: 'demo project',
    db_path: '/tmp/demo.db',
    primary_language: 'Go',
    claude_logs_path: '',
    cursor_logs_path: '',
    cline_logs_path: '',
    aider_logs_path: '',
    custom_logs_path: '',
    wrongstack_logs_path: '',
    created_at: '2026-09-29T00:00:00Z',
    is_active: true,
  };

  const render = (discovered_sessions) =>
    renderToStaticMarkup(
      React.createElement(ProjectIdentityModal, {
        project: { ...baseProject, discovered_sessions },
        isOpen: true,
        onClose: () => {},
        onUpdated: () => {},
      }),
    );

  // Reads the number out of a badge: the value div follows its label div.
  // The WrongStack label is anchored on the emoji so the word "WrongStack",
  // which also appears in the panel's prose, cannot match first.
  function badge(html, label) {
    const i = html.indexOf(label);
    if (i < 0) return null;
    const after = html.slice(i + label.length);
    const m = after.match(/<\/div>\s*<div[^>]*>\s*([\d,]+)/);
    return m ? m[1] : null;
  }

  // --- The defect: the producer omitted the key because it claimed nothing.
  const absent = render({});
  check(absent.includes('⚡ WrongStack'),
    'setup: the WrongStack badge must be present in the render');
  if (failures.length > 0) {
    for (const f of failures) console.log('  - ' + f);
    console.log('FAIL: project-identity-wrongstack-count (setup)');
    process.exit(1);
  }
  check(badge(absent, '⚡ WrongStack') === '0',
    `with discovered_sessions={} the producer claimed no WrongStack session, so the badge must show 0; got ${badge(absent, '⚡ WrongStack')}`);

  // --- Control 1: an honest zero (producer sent the key as 0) stays 0.
  const zero = render({ wrongstack: 0 });
  check(badge(zero, '⚡ WrongStack') === '0',
    `a producer-sent wrongstack: 0 must render 0; got ${badge(zero, '⚡ WrongStack')}`);

  // --- Control 2: a real count is displayed verbatim. This is also the
  // producer's OWN fallback: it sets the key to 1 when .wrongstack exists, and
  // that must still display as 1 through the honest branch.
  const real = render({ wrongstack: 7 });
  check(badge(real, '⚡ WrongStack') === '7',
    `a real wrongstack count must be displayed verbatim; got ${badge(real, '⚡ WrongStack')}`);

  const producerFallback = render({ wrongstack: 1 });
  check(badge(producerFallback, '⚡ WrongStack') === '1',
    `the producer's own dirExists() fallback of 1 must still display as 1; got ${badge(producerFallback, '⚡ WrongStack')}`);

  // --- Control 3: an entirely absent discovered_sessions must behave the same.
  const none = render(undefined);
  check(badge(none, '⚡ WrongStack') === '0',
    `with no discovered_sessions at all the badge must show 0; got ${badge(none, '⚡ WrongStack')}`);

  // --- Consistency: a sibling badge with an absent key must also show 0, so
  // the WrongStack badge is provably held to the same standard.
  check(badge(absent, 'Cursor AI') === '0',
    `sibling badge with an absent key shows ${badge(absent, 'Cursor AI')}, want "0" — the WrongStack badge must match it`);

  // --- Source: the duplicated fallback-to-1 default must be gone.
  check(!/sessions\.wrongstack\s*\|\|\s*1\b/.test(src),
    'the WrongStack badge must not default an absent producer value to 1');

  if (failures.length > 0) {
    for (const f of failures) console.log('  - ' + f);
    console.log(`FAIL: project-identity-wrongstack-count (${failures.length} assertion(s))`);
    process.exit(1);
  }
  console.log('PASS: the WrongStack badge shows exactly what the producer sent, never an invented 1');
} finally {
  fs.rmSync(tmpComp, { force: true });
}
