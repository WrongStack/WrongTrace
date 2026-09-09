// Regression test (round 35): AgentSessionsView "current project" session
// scope must not leak sibling-project runs via the bidirectional slug/name
// containment
//   !r.project_slug.toLowerCase().includes(currentProject.name.toLowerCase()) &&
//   !currentProject.name.toLowerCase().includes(r.project_slug.toLowerCase())
// and must not hide runs whose project_id matches exactly but whose slug
// drifted (the containment guard ran even after an id match).
//
// Contract (mirrors the round-32 ProxyRoutingView fix — a containment test
// between a project NAME and an agent-supplied project SLUG can only match a
// DIFFERENT workspace, because names default to filepath.Base of sibling
// directories and agents send their own slug):
//   - runs with an exact project_id match render (id suffices, slug ignored),
//   - slug-only runs render ONLY on exact (case-insensitive) slug==name
//     equality — never on substring containment,
//   - unattributed runs (neither id nor slug) render as before,
//   - with no current project, everything renders (no over-filtering).
//
// The Overview snapshot's active_runs is NOT project-scoped server-side
// (metrics.go sets ActiveRuns: e.ActiveRuns() unconditionally), so this
// client-side filter is the only per-project scoping of the sessions list.
//
// Run: node web/scripts/agent-sessions-scope-regression.mjs
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

const tsxSource = fs.readFileSync(path.join(root, 'web', 'src', 'components', 'AgentSessionsView.tsx'), 'utf8');
const transformed = await vite.transformWithOxc(tsxSource, 'AgentSessionsView.tsx');
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

function run(id, agent, extra = {}) {
  return {
    run_id: `run-${id}`,
    agent_name: `ag-${agent}`,
    model_name: 'claude-3-7-sonnet',
    task_id: `task-${id}`,
    started_at: '2026-09-06T12:00:00.000Z',
    last_seen: '2026-09-06T12:01:00.000Z',
    ...extra,
  };
}

const runsFixture = [
  // exact project_id match -> must render under the current project
  run('own-id', 'own-id', { project_id: 'proj-v2id' }),
  // slug-only run, slug equals the project name exactly -> must render
  run('own-slug', 'own-slug', { project_slug: 'api-v2' }),
  // id matches but the slug drifted -> must still render (id suffices);
  // pre-fix the containment guard hid this run (over-exclusion facet)
  run('own-drift', 'own-drift', { project_id: 'proj-v2id', project_slug: 'drifted-slug' }),
  // sibling workspace run (slug "api", no id) -> must NOT render under
  // "api-v2"; pre-fix the bidirectional containment leaked it (leak facet)
  run('sibling', 'sibling', { project_slug: 'api' }),
  // unattributed run -> renders under any scope (intended passthrough)
  run('unattr', 'unattr'),
];

const lucideStub = new Proxy({}, { get: () => () => null });
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
  };
}

// Hook call order in AgentSessionsView: useState slot 0 is selectedRunId,
// slot 1 is activeSubTab ('catalog' by default — the run cards render under
// the 'sessions' sub-tab, so the harness seeds it). The array must stay
// SPARSE: only slot 1 is pre-set so every other useState auto-seeds its
// declared initial value.
function seed() {
  const v = [];
  v[1] = 'sessions';
  return { values: v, idx: 0 };
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
  body += '\n__exports.AgentSessionsView = AgentSessionsView;';
  return body;
})();

function render(withProject) {
  const hooksMods = {
    useModelCatalog: () => ({ data: [], refetch: () => {} }),
    useProviderCatalog: () => ({ data: [], refetch: () => {} }),
  };
  const store = seed();
  const mods = {
    react: makeHooks(store),
    'react/jsx-runtime': { jsx: realElement, jsxs: realElement, Fragment: RealReact.Fragment },
    'lucide-react': lucideStub,
    './RichDiffViewer': { RichDiffViewer: () => null },
    '../lib/clipboard': { copyToClipboard: async () => true },
    '../hooks/useMetrics': hooksMods,
    '../types': { isJunkModel: () => false, formatCleanModel: (s) => s },
  };
  const fn = new Function('__mods', '__exports', bodySansImports);
  const exportsRef = {};
  fn(mods, exportsRef, RealReact);
  const tree = exportsRef.AgentSessionsView({
    activeRuns: runsFixture,
    models: [],
    overview: undefined,
    recentEvents: [],
    loading: false,
    currentProject: withProject,
  });
  return RealReactDOMServer.renderToStaticMarkup(tree);
}

globalThis.window = { location: { origin: 'http://localhost:3444' } };

const fails = [];
function fail(msg) {
  fails.push(msg);
  console.log('FAIL: ' + msg);
}

const marker = (markup, id) => markup.includes(`ag-${id}`) || markup.includes(`run-${id}`);

// Scenario A: a current project is selected (the dashboard default whenever a
// project exists).
const scoped = render(currentProject);
if (!marker(scoped, 'own-id')) {
  fail("setup: the id-matched own run did not render (marker 'own-id' absent)");
}
if (!marker(scoped, 'own-slug')) {
  fail("contract: exact slug==name run stopped rendering under scope 'current' (marker 'own-slug' absent)");
}
if (!marker(scoped, 'own-drift')) {
  fail(
    "contract: run with exact project_id match but drifted slug was hidden — the id match must suffice (marker 'own-drift' absent)",
  );
}
if (!marker(scoped, 'unattr')) {
  fail("contract: unattributed runs stopped rendering under scope 'current' (marker 'unattr' absent)");
}
if (marker(scoped, 'sibling')) {
  fail(
    'LEAK: sibling-project run (project_slug "api") renders under current project "api-v2" — a slug/name containment fallback matched a different workspace (marker \'sibling\' present)',
  );
}

// Scenario B (control): no current project — every run renders (the scope
// filter must not over-filter the unscoped view).
const unscoped = render(null);
for (const id of ['own-id', 'own-slug', 'own-drift', 'sibling', 'unattr']) {
  if (!marker(unscoped, id)) {
    fail(`control: unscoped view lost run ${id}`);
  }
}

if (fails.length > 0) {
  console.log(`FAIL: ${fails.length} check(s) failed — agent-sessions project-scope regression`);
  process.exit(1);
}
console.log(
  "PASS: scope 'current' shows id-matched + exact-slug + unattributed runs only (sibling slug 'api' excluded from 'api-v2', drifted-slug id-match kept); unscoped view shows everything",
);
