// Regression test (round 5): every copy button in the dashboard must handle
// clipboard failure honestly.
//
// Defect (pre-fix): five components called navigator.clipboard.writeText()
// UNGUARDED and UNAWAITED, then flashed "Copied" unconditionally:
//   1. crash — where navigator.clipboard is undefined (Clipboard API only
//      exists in secure contexts; the dashboard is served over plain HTTP on
//      configurable addresses) the handler threw a TypeError and the button
//      silently did nothing;
//   2. false success — where writeText() rejected (Chrome refuses when the
//      document loses focus) the UI still flashed "Copied" on a FAILED write;
//   3. floating rejection — the rejected promise was never handled (Node
//      treats unhandledRejection as fatal; browsers log it silently).
// Affected sites: RichDiffViewer, IPCTrafficView, LiveEventFeed,
// ProxyRoutingView, AgentSessionsView. Fixed by the shared
// web/src/lib/clipboard.ts helper: copyToClipboard(text) -> Promise<boolean>,
// secure-context guarded, awaited, catch-to-false — handlers flash "Copied"
// ONLY on true.
//
// Contract: copy handlers never throw in insecure contexts, never report
// success for a failed write, leave no unhandled rejections, and no component
// calls navigator.clipboard directly anymore.
//
// Run: node web/scripts/clipboard-copy-failure-regression.mjs
// Exit 0 = pass, 1 = fail. Transpiles the REAL component and helper with the
// repo's own vite; no test framework or extra dependency.
//
// Harness notes for the next editor (each cost a debugging cycle once):
//   - The global navigator is getter-only in Node 24 and cannot be shimmed;
//     the sliced handler is instead evaluated through new Function with a
//     `navigator` PARAMETER, which shadows the global legally and lets each
//     scenario inject its own clipboard shape (undefined / rejecting /
//     succeeding).
//   - Scenario assertions must be flip-aware: scenario 1 asserts "never
//     throws AND no Copied state", which fails BOTH for the pre-fix crash
//     and for a post-fix regression that lies about success.
//   - Scenario 1 (helper Part 1) reads web/src/lib/clipboard.ts if present;
//     pre-fix runs skip it with a note and the FAILs come from Parts 2-4.
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';

const scriptDir = path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
const root = path.resolve(scriptDir, '..', '..');

const webRequire = createRequire(pathToFileURL(path.join(root, 'web', 'package.json')));
const vite = webRequire('vite');

const fails = [];
function fail(msg) {
  fails.push(msg);
  console.log('FAIL: ' + msg);
}

// ---------------------------------------------------------------------------
// Part 1: the shared helper (web/src/lib/clipboard.ts) must return false (no
// throw) where navigator.clipboard is missing — Node's real global has
// navigator but no clipboard, the exact insecure-context shape.
// ---------------------------------------------------------------------------
const libPath = path.join(root, 'web', 'src', 'lib', 'clipboard.ts');
if (fs.existsSync(libPath)) {
  const libSource = fs.readFileSync(libPath, 'utf8');
  const libT = await vite.transformWithOxc(libSource, 'clipboard.ts');
  if (libT.errors && libT.errors.length > 0) {
    fail('setup: clipboard.ts transform errors: ' + JSON.stringify(libT.errors));
  } else {
    let libBody = libT.code.replace(/^export\s+/gm, '');
    libBody += '\n__exports.copyToClipboard = copyToClipboard;';
    const libFn = new Function('__exports', libBody);
    const libExports = {};
    libFn(libExports);
    const realHelper = libExports.copyToClipboard;
    const ok = await realHelper('round-5 probe')
      .then((v) => v)
      .catch((e) => {
        fail(`helper crashed in insecure context: ${e && e.message}`);
        return null;
      });
    if (ok === true) {
      fail('helper reported success with no clipboard available — dishonest success');
    }
  }
} else {
  console.log('note: web/src/lib/clipboard.ts does not exist — helper checks skipped (component checks still run)');
}

// ---------------------------------------------------------------------------
// Part 2-4: the REAL handleCopy from RichDiffViewer, sliced verbatim from the
// transpiled component and driven through three clipboard scenarios via a
// navigator PARAMETER (the global is getter-only in Node 24; a parameter
// shadows it legally).
// ---------------------------------------------------------------------------
const tsxSource = fs.readFileSync(path.join(root, 'web', 'src', 'components', 'RichDiffViewer.tsx'), 'utf8');
const transformed = await vite.transformWithOxc(tsxSource, 'RichDiffViewer.tsx');
if (transformed.errors && transformed.errors.length > 0) {
  console.log('FAIL: transform errors: ' + JSON.stringify(transformed.errors));
  process.exit(1);
}
const code = transformed.code;

const sliceRe = /^[ \t]*const handleCopy = (?:async )?\(\) => \{[\s\S]*?^[ \t]*\};/m;
const matches = code.match(new RegExp(sliceRe.source, 'gm'));
if (!matches || matches.length !== 1) {
  console.log(`FAIL: setup — expected exactly 1 handleCopy slice, got ${matches ? matches.length : 0}`);
  process.exit(1);
}
const slice = matches[0];

async function drive(navigatorStub, clipboardImpl) {
  const calls = { setCopied: [], unhandled: 0 };
  const onUnhandled = () => {
    calls.unhandled++;
  };
  process.on('unhandledRejection', onUnhandled);
  try {
    const fn = new Function('navigator', 'setCopied', 'diff', 'setTimeout', 'copyToClipboard', `${slice}\nreturn handleCopy;`);
    const handler = fn(navigatorStub, (v) => calls.setCopied.push(v), 'diff-body', () => 0, clipboardImpl);
    await handler();
    // Let any floating rejection surface for the count.
    await new Promise((resolve) => setImmediate(resolve));
  } finally {
    process.off('unhandledRejection', onUnhandled);
  }
  return calls;
}

// Scenario 1 — crash path: insecure context (navigator.clipboard undefined).
// The handler must NEVER throw here, and must NOT flash "Copied" either —
// honest failure means no state change at all.
try {
  const calls = await drive({}, async () => false);
  if (calls.setCopied.includes(true)) {
    fail(`FALSE SUCCESS: "Copied" state was set in an insecure context (navigator.clipboard undefined) — the write cannot have happened`);
  }
} catch (e) {
  fail(`CRASH — handleCopy threw with navigator.clipboard undefined: ${e && e.constructor.name}: ${e && e.message}`);
}

// Scenario 2 — reject path: writeText rejects (e.g. document not focused).
try {
  const rejectingNavigator = {
    clipboard: {
      writeText: async () => {
        throw new Error('NotAllowedError: Document is not focused');
      },
    },
  };
  const calls = await drive(rejectingNavigator, async () => false);
  if (calls.setCopied.includes(true)) {
    fail('FALSE SUCCESS: "Copied" state was set even though the clipboard write REJECTED');
  }
  if (calls.unhandled > 0) {
    fail(`FLOATING REJECTION: ${calls.unhandled} unhandled promise rejection(s) escaped the copy handler`);
  }
} catch (e) {
  fail(`reject-path scenario threw instead of degrading: ${e && e.message}`);
}

// Scenario 3 — control: success path must still set the Copied state.
try {
  const successNavigator = {
    clipboard: {
      writeText: async () => {},
    },
  };
  const calls = await drive(successNavigator, async () => true);
  if (!calls.setCopied.includes(true)) {
    fail('control: successful clipboard write did not set the Copied state');
  }
} catch (e) {
  fail(`control scenario threw: ${e && e.message}`);
}

// ---------------------------------------------------------------------------
// Part 4: source sweep — no component may call navigator.clipboard directly
// anymore; all copy paths go through the shared helper.
// ---------------------------------------------------------------------------
const components = [
  'RichDiffViewer.tsx',
  'IPCTrafficView.tsx',
  'LiveEventFeed.tsx',
  'ProxyRoutingView.tsx',
  'AgentSessionsView.tsx',
];
for (const name of components) {
  const src = fs.readFileSync(path.join(root, 'web', 'src', 'components', name), 'utf8');
  const hits = src.match(/navigator\.clipboard/g);
  if (hits) {
    fail(`RAW CLIPBOARD ACCESS: ${name} still calls navigator.clipboard directly (${hits.length} site(s)) — unguarded, unawaited, false-success pattern`);
  }
}

if (fails.length > 0) {
  console.log(`FAIL: ${fails.length} check(s) failed — clipboard copy failure handling`);
  process.exit(1);
}
console.log(
  'PASS: copy handlers degrade honestly in insecure contexts, never flash "Copied" on failure, leave no floating rejections, and all five components use the shared helper',
);
