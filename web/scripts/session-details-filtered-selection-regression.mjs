// Regression test (round r5, F213): AgentSessionsView's Session Details panel
// must show only a run the session list shows. It read the unfiltered
// activeRuns, so a sibling project's run -- or one hidden as a junk model
// ("unknown-model") -- filled the details panel, with its diffs, beside a list
// saying no sessions were connected.
//
// Run: node web/scripts/session-details-filtered-selection-regression.mjs
// Exit 0 = pass, 1 = fail. Transpiles the real component with the repo's vite.
// Harness: transpile a REAL web/src component with the repo's own vite,
// shim its imports onto stubs, and evaluate it via new Function (no temp file).
import { createRequire } from 'node:module';
import { pathToFileURL, fileURLToPath } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '..', '..');
const webRequire = createRequire(pathToFileURL(path.join(root, 'web', 'package.json')));
const vite = webRequire('vite');
const RealReact = webRequire('react');
const RealReactDOMServer = webRequire('react-dom/server');

const realElement = (type, props, key) => {
  const el = RealReact.createElement(type, key !== undefined ? { ...props, key } : props);
  if (el && el._store) el._store.validated = true;
  return el;
};
const lucideStub = new Proxy({}, { get: (_, n) => (n === '__esModule' ? false : () => null) });

function makeHooks(store) {
  return {
    ...RealReact,
    useState(initial) {
      const i = store.idx++;
      if (!(i in store.values)) store.values[i] = typeof initial === 'function' ? initial() : initial;
      const set = (v) => { store.values[i] = typeof v === 'function' ? v(store.values[i]) : v; };
      return [store.values[i], set];
    },
    useMemo: (fn) => fn(),
    useEffect() {},
    useLayoutEffect() {},
    useCallback: (fn) => fn,
    useRef: (v) => ({ current: v }),
  };
}

async function loadBody(relPath, exportNames) {
  const src = fs.readFileSync(path.join(root, 'web', 'src', ...relPath.split('/')), 'utf8');
  const t = await vite.transformWithOxc(src, path.basename(relPath));
  if (t.errors && t.errors.length) throw new Error('transform errors: ' + JSON.stringify(t.errors));
  let body = t.code.replace(/^import\s+type\s+.*?;?\s*$/gm, '');
  const conv = (names) => names.split(',').map((p) => {
    const s = p.trim(); if (!s) return '';
    const m = s.match(/^(\S+)\s+as\s+(\S+)$/); return m ? `${m[1]}: ${m[2]}` : s;
  }).filter(Boolean).join(', ');
  body = body.replace(/^import\s+(\w+)(?:\s*,\s*)?\s*\{([^}]*)\}\s*from\s*['"]([^'"]+)['"];?\s*$/gm,
    (_, d, n, s) => `const {${conv(n)}} = __mods[${JSON.stringify(s)}]; const ${d} = __mods[${JSON.stringify(s)}].default;`);
  body = body.replace(/^import\s*\{([^}]*)\}\s*from\s*['"]([^'"]+)['"];?\s*$/gm,
    (_, n, s) => `const {${conv(n)}} = __mods[${JSON.stringify(s)}];`);
  body = body.replace(/^import\s+(\w+)\s+from\s*['"]([^'"]+)['"];?\s*$/gm,
    (_, d, s) => `const ${d} = __mods[${JSON.stringify(s)}].default;`);
  body = body.replace(/^export\s+/gm, '');
  body += '\n' + exportNames.map((n) => `__exports.${n} = ${n};`).join('\n');
  if (/^\s*import\s/m.test(body)) throw new Error('unresolved import lines remain');
  return body;
}

function evalBody(body, mods) {
  const exportsRef = {};
  new Function('__mods', '__exports', body)(mods, exportsRef);
  return exportsRef;
}


const body = await loadBody('components/AgentSessionsView.tsx', ['AgentSessionsView']);
// REAL isJunkModel / formatCleanModel from web/src/types/index.ts
const typesMod = evalBody(await loadBody('types/index.ts', ['isJunkModel', 'formatCleanModel']), {});
globalThis.window = { location: { origin: 'http://localhost:3444' } };

const currentProject = { id: 'proj-mine', name: 'api-v2', path: 'D:/ws/api-v2' };
const run = (id, extra) => ({
  run_id: `run-${id}`, agent_name: `ag-${id}`, model_name: 'claude-sonnet-4', task_id: `task-${id}`,
  started_at: '2026-10-08T10:00:00Z', last_seen: '2026-10-08T10:01:00Z', ...extra,
});
const sibling = run('sibling', { project_id: 'proj-other', project_slug: 'api' });
const mine = run('mine', { project_id: 'proj-mine' });
const events = [{
  event_id: 'ev-sib', run_id: 'run-sibling', file_path: 'SIBLING_SECRET/file.go', node_signature: 'func Leak()',
  action: 'MODIFIED', diff_snippet: '+x', start_line: 1, end_line: 2, event_time: '2026-10-08T10:00:30Z',
}];

function render(activeRuns, proj) {
  const store = { values: [, 'sessions'], idx: 0 };
  const mods = {
    react: makeHooks(store),
    'react/jsx-runtime': { jsx: realElement, jsxs: realElement, Fragment: RealReact.Fragment },
    'lucide-react': lucideStub,
    './RichDiffViewer': { RichDiffViewer: () => null },
    '../lib/clipboard': { copyToClipboard: async () => true },
    '../hooks/useMetrics': { useModelCatalog: () => ({ data: [], refetch() {} }), useProviderCatalog: () => ({ data: [], refetch() {} }) },
    '../types': typesMod,
  };
  const { AgentSessionsView } = evalBody(body, mods);
  return RealReactDOMServer.renderToStaticMarkup(AgentSessionsView({
    activeRuns, models: [], overview: undefined, recentEvents: events, loading: false, currentProject: proj,
  }));
}

let bad = 0;
// Scenario A: sibling run first in the snapshot, own run second.
const a = render([sibling, mine], currentProject);
const aListHasSibling = a.includes('Run: run-sibling');
const aDetailsSibling = a.includes('run-sibling') && !aListHasSibling;
console.log('A) activeRuns=[sibling(proj-other), mine(proj-mine)], scope=api-v2');
console.log('   EXPECTED: list shows only run-mine; Session Details shows run-mine (or nothing) — never run-sibling');
console.log(`   ACTUAL:   list has run-sibling=${aListHasSibling}; markup contains run-sibling (details)=${a.includes('run-sibling')}; foreign diff path rendered=${a.includes('SIBLING_SECRET/file.go')}`);
if (aDetailsSibling) bad++;

// Scenario B: only a sibling run exists -> list empty, details still populated.
const b = render([sibling], currentProject);
const bEmpty = b.includes('No active agent sessions currently connected.');
console.log('B) activeRuns=[sibling] only, scope=api-v2');
console.log('   EXPECTED: empty list AND empty details (no "In-Flight Live")');
console.log(`   ACTUAL:   emptyListMsg=${bEmpty}; In-Flight Live=${b.includes('In-Flight Live')}; details run-sibling=${b.includes('run-sibling')}; foreign diff=${b.includes('SIBLING_SECRET/file.go')}`);
if (bEmpty && b.includes('run-sibling')) bad++;

// CONTROL: no project scope -> sibling legitimately selected; own-first -> own selected, no sibling.
const c1 = render([sibling, mine], null);
const c2 = render([mine, sibling], currentProject);
const ctrlOk = c1.includes('Run: run-sibling') && c2.includes('run-mine') && !c2.includes('run-sibling');
console.log(`CONTROL: unscoped shows sibling=${c1.includes('Run: run-sibling')}; own-first scoped has no sibling=${!c2.includes('run-sibling')} -> ${ctrlOk ? 'ok' : 'UNEXPECTED'}`);

// Scenario C (realistic, no project mismatch needed): engine.go:655 rewrites an
// empty/junk model to "unknown-model", which the client isJunkModel hides from
// the list — but activeRuns[0] still feeds the details panel.
const nomodel = run('nomodel', { project_id: 'proj-mine', model_name: 'unknown-model' });
const c = render([nomodel], currentProject);
console.log('C) activeRuns=[run reported without model -> "unknown-model"], scope=api-v2');
console.log('   EXPECTED: list empty -> details empty');
console.log(`   ACTUAL:   emptyListMsg=${c.includes('No active agent sessions currently connected.')}; In-Flight Live=${c.includes('In-Flight Live')}; details run-nomodel=${c.includes('run-nomodel')}`);
if (c.includes('No active agent sessions currently connected.') && c.includes('run-nomodel')) bad++;

if (!ctrlOk) { console.log('FAIL: control'); process.exit(1); }
if (bad > 0) { console.log(`FAIL (${bad} scenario(s))`); process.exit(1); }
console.log('PASS'); process.exit(0);
