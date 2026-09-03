// Regression test (round 32): ProxyRoutingView "current project" traffic scope
// must not leak sibling-project traffic via the substring fallback
//   currentProject.name.toLowerCase().includes(t.project_slug.toLowerCase())
//
// Contract (mirrors the round-17/21 Atlas sibling-prefix family — a containment
// test between a project NAME and an agent-supplied project SLUG can only match
// a DIFFERENT workspace, because names default to filepath.Base of sibling
// directories and agents send their own slug via X-Project-Slug):
//   - scope 'current' renders records with an exact project_id match,
//   - renders unattributed records (neither id nor slug) as before,
//   - renders slug-matching records ONLY on exact (case-insensitive) equality
//     with the current project name — never on substring containment,
//   - scope 'all' still renders every record (no over-filtering).
//
// Run: node web/scripts/project-scope-sibling-leak-regression.mjs
// Exit 0 = pass, 1 = fail.
// Uses the repo's own vite (web/node_modules) to transpile the real component;
// no test framework or extra dependency is required.
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';

const scriptDir = path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
const root = path.resolve(scriptDir, '..', '..');

const webRequire = createRequire(pathToFileURL(path.join(root, 'web', 'package.json')));
const vite = webRequire('vite');
const RealReact = webRequire('react');
const RealReactDOMServer = webRequire('react-dom/server');

const tsxSource = fs.readFileSync(path.join(root, 'web', 'src', 'components', 'ProxyRoutingView.tsx'), 'utf8');
const transformed = await vite.transformWithOxc(tsxSource, 'ProxyRoutingView.tsx');
if (transformed.errors && transformed.errors.length > 0) {
  console.log('FAIL: transform errors: ' + JSON.stringify(transformed.errors));
  process.exit(1);
}
const code = transformed.code;

// currentProject "api-v2" — a sibling workspace of "api" (names are
// filepath.Base of sibling directories; both are real registered workspaces).
const currentProject = {
  id: 'proj-v2id',
  name: 'api-v2',
  path: 'D:/workspaces/api-v2',
  db_path: 'D:/home/.wrongtrace/projects/api-v2/wrongtrace.db',
};

function rec(id, model, extra = {}) {
  return {
    id,
    timestamp: '2026-09-03T10:00:00.000Z',
    method: 'POST',
    incoming_path: `/proxy/${id}/v1/chat/completions`,
    target_url: `https://upstream.example/v1/chat/completions?rec=${id}`,
    provider: 'Upstream',
    model,
    status_code: 200,
    is_stream: false,
    request_headers: {},
    request_body: '{}',
    response_headers: {},
    response_body: '{}',
    prompt_tokens: 10,
    completion_tokens: 5,
    cached_tokens: 0,
    reasoning_tokens: 0,
    cost_usd: 0.01,
    cache_savings_usd: 0,
    tool_calls: [],
    tool_count: 0,
    duration_ms: 42,
    ...extra,
  };
}

const trafficFixture = [
  rec('px-own', 'model-own', { project_id: 'proj-v2id' }), // exact id match -> must render under 'current'
  rec('px-sibling', 'model-sibling', { project_slug: 'api' }), // sibling slug, no id -> must NOT render under 'current'
  rec('px-unattr', 'model-unattr'), // unattributed -> renders under 'current' (intended passthrough)
];

const lucideStub = new Proxy({}, { get: () => () => null });
const chartsStub = new Proxy({}, { get: () => () => null });
const realElement = (type, props, key) =>
  RealReact.createElement(type, key !== undefined ? { ...props, key } : props);

function makeHooks(store) {
  return {
    ...RealReact,
    useState(initial) {
      const i = store.idx++;
      if (!(i in store.values)) store.values[i] = initial;
      const set = (v) => {
        store.values[i] = typeof v === 'function' ? v(store.values[i]) : v;
      };
      return [store.values[i], set];
    },
    useMemo(fn) {
      return fn();
    },
    useEffect() {},
    useCallback(fn) {
      return fn;
    },
    useDeferredValue(v) {
      return v;
    },
  };
}

// Hook call order in ProxyRoutingView: 0 activeSubTab, 1 selectedTraffic,
// 2 trafficFilter, 3 statusFilter, 4 projectScope, ... (round-30 harness notes).
function seed(scope) {
  const v = new Array(24).fill(undefined);
  v[0] = 'traffic';
  v[4] = scope;
  return v;
}

const bodySansImports = (() => {
  let body = code.replace(/^import\s+type\s+.*?;?\s*$/gm, '');
  body = body.replace(/^import\s*\{([^}]*)\}\s*from\s*['"]([^'"]+)['"];?\s*$/gm, (_, names, spec) => {
    const converted = names
      .split(',')
      .map((piece) => {
        const t = piece.trim();
        if (!t) return '';
        const m = t.match(/^(\S+)\s+as\s+(\S+)$/);
        return m ? `${m[1]}: ${m[2]}` : t;
      })
      .filter(Boolean)
      .join(', ');
    return `const {${converted}} = __mods[${JSON.stringify(spec)}];`;
  });
  body = body.replace(/^export\s+/gm, '');
  body += '\n__exports.ProxyRoutingView = ProxyRoutingView;';
  return body;
})();

function render(scope) {
  const hooksMods = {
    useProxyRoutes: () => ({ data: [], refetch: () => {} }),
    useModelCatalog: () => ({ data: [] }),
    useProxyTraffic: () => ({ data: trafficFixture, refetch: () => {} }),
    useProxyTrafficDetail: () => ({ data: null }),
  };
  const store = { values: seed(scope), idx: 0 };
  const mods = {
    react: makeHooks(store),
    'react/jsx-runtime': { jsx: realElement, jsxs: realElement, Fragment: RealReact.Fragment },
    'lucide-react': lucideStub,
    recharts: chartsStub,
    '../hooks/useMetrics': hooksMods,
  };
  const fn = new Function('__mods', '__exports', bodySansImports);
  const exportsRef = {};
  fn(mods, exportsRef, RealReact);
  const tree = exportsRef.ProxyRoutingView({ currentProject });
  return RealReactDOMServer.renderToStaticMarkup(tree);
}

globalThis.window = { location: { origin: 'http://localhost:3444' } };

const fails = [];
function fail(msg) {
  fails.push(msg);
  console.log('FAIL: ' + msg);
}

// Scenario A: scope 'current' (the default, and the UI toggle the dashboard
// shows whenever a project is selected).
const current = render('current');
if (!current.includes('model-own')) {
  fail("setup: the id-matched own record did not render under scope 'current' (marker 'model-own' absent)");
}
if (!current.includes('model-unattr')) {
  fail("contract: unattributed traffic stopped rendering under scope 'current' (marker 'model-unattr' absent)");
}
if (current.includes('model-sibling')) {
  fail(
    'LEAK: sibling-project traffic (project_slug "api") renders under current project "api-v2" scope — a slug/name containment fallback matched a different workspace (marker \'model-sibling\' present)',
  );
}

// Scenario B (control): scope 'all' shows every record — proves the scope
// filter did not over-filter the unscoped view.
const all = render('all');
for (const marker of ['model-own', 'model-sibling', 'model-unattr']) {
  if (!all.includes(marker)) {
    fail(`control: scope 'all' lost traffic record ${marker}`);
  }
}

if (fails.length > 0) {
  console.log(`FAIL: ${fails.length} check(s) failed — project-scope sibling leak regression`);
  process.exit(1);
}
console.log(
  "PASS: scope 'current' shows id-matched + unattributed traffic only (sibling slug 'api' excluded from 'api-v2'); scope 'all' still shows everything",
);
