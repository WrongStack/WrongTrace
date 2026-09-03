// Regression test (round 30): ProxyRoutingView.handleSaveRoute must surface
// failed route saves instead of closing the modal as if they succeeded.
//
// Failed save contract (mirrors the round-27 silent-failure fixes in
// ProjectIdentityModal.handleSave / Navbar.handleSwitchProject):
//   - the server's error message is rendered in the modal,
//   - the modal stays open and the form draft is preserved,
//   - the routes list is not refetched as if the route existed.
// Successful-save control: modal closes, form clears, list refetches.
//
// Run: node web/scripts/route-save-regression.mjs   (exit 0 = pass, 1 = fail)
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

const routesFixture = [
  {
    id: 'route-1',
    name: 'Existing',
    path_prefix: '/proxy/existing',
    target_upstream: 'https://upstream.example/v1',
    protocol_type: 'openai',
    default_model: '',
    enabled: true,
  },
];
const trafficFixture = [
  {
    id: 'px-1',
    timestamp: '2026-09-02T20:00:00.5Z',
    method: 'POST',
    incoming_path: '/proxy/existing/v1/chat/completions',
    target_url: 'https://upstream.example/v1/chat/completions',
    provider: 'Upstream',
    model: 'm1',
    status_code: 200,
    prompt_tokens: 10,
    completion_tokens: 5,
  },
];

const SERVER_ERROR = 'authentication required';
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
// 2 trafficFilter, 3 statusFilter, 4 projectScope, 5 trafficInspectorTab,
// 6 trafficChartMode, 7 toolCategoryFilter, 8 toolChartMetric,
// 9 selectedToolName, 10 copiedId, 11 isModalOpen, 12 formName, 13 formPath,
// 14 formUpstream, 15 formProtocol, 16 formModel, 17 isSubmitting,
// 18 routeError.
function seed(modalOpen) {
  const v = new Array(24).fill(undefined);
  v[0] = 'routes';
  v[11] = modalOpen;
  v[12] = 'my-route';
  v[13] = '/proxy/my-route';
  v[14] = 'https://upstream.example/v1';
  v[15] = 'openai';
  v[16] = 'm1';
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

function collectOnSubmit(node, out = []) {
  if (node == null || typeof node !== 'object' || !node.props) return out;
  if (typeof node.props.onSubmit === 'function') out.push(node);
  const kids = node.props.children;
  if (Array.isArray(kids)) kids.forEach((k) => collectOnSubmit(k, out));
  else if (kids != null) collectOnSubmit(kids, out);
  return out;
}

const calls = { fetch: [], refetchRoutes: 0, refetchTraffic: 0 };

async function scenario(fetchResponse, modalOpen) {
  calls.fetch = [];
  calls.refetchRoutes = 0;
  calls.refetchTraffic = 0;
  globalThis.fetch = async (url, opts) => {
    calls.fetch.push({ url: String(url), method: opts?.method, body: opts?.body ? String(opts.body) : '' });
    return fetchResponse();
  };
  const hooksMods = {
    useProxyRoutes: () => ({ data: routesFixture, refetch: () => { calls.refetchRoutes++; } }),
    useModelCatalog: () => ({ data: [] }),
    useProxyTraffic: () => ({ data: trafficFixture, refetch: () => { calls.refetchTraffic++; } }),
    useProxyTrafficDetail: () => ({ data: null }),
  };
  const store = { values: seed(modalOpen), idx: 0 };
  const props = { currentProject: null };
  const render = () => {
    store.idx = 0;
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
    const tree = exportsRef.ProxyRoutingView(props);
    const markup = RealReactDOMServer.renderToStaticMarkup(tree);
    return { tree, markup };
  };
  const before = render();
  const forms = collectOnSubmit(before.tree);
  if (forms.length === 0) return { setupFail: 'no <form onSubmit> found in the rendered modal' };
  await forms[0].props.onSubmit({ preventDefault() {} });
  const after = render();
  return { before, after, store, calls };
}

const fails = [];
function fail(msg) {
  fails.push(msg);
  console.log('FAIL: ' + msg);
}

globalThis.window = { location: { origin: 'http://localhost:3444' } };

// Scenario 1: the server rejects the save.
{
  const r = await scenario(
    () => ({
      ok: false,
      status: 401,
      json: async () => ({ error: SERVER_ERROR, message: SERVER_ERROR }),
    }),
    true,
  );
  if (r.setupFail) {
    console.log('FAIL: setup: ' + r.setupFail);
    process.exit(1);
  }
  if (!r.before.markup.includes('Add AI Gateway Route')) {
    console.log('FAIL: setup: modal not rendered despite isModalOpen=true');
    process.exit(1);
  }
  if (!r.after.markup.includes(SERVER_ERROR)) {
    fail('the server error was not surfaced to the user (no "' + SERVER_ERROR + '" anywhere in the rendered output)');
  }
  if (r.store.values[11] !== true) {
    fail('the Add Route modal was closed after the failed save — the failure looked like success');
  }
  if (r.store.values[12] !== 'my-route') {
    fail('the form draft was wiped after the failed save (formName=' + JSON.stringify(r.store.values[12]) + ', want "my-route")');
  }
  if (r.calls.refetchRoutes !== 0) {
    fail('the routes list was refetched after the failed save (' + r.calls.refetchRoutes + 'x)');
  }
  const saveCall = r.calls.fetch.find((c) => c.url.includes('/api/proxy/routes'));
  if (!saveCall || saveCall.method !== 'POST') {
    fail('the route-save POST was not issued');
  }
}

// Scenario 2 (control): a successful save.
{
  const r = await scenario(
    () => ({ ok: true, status: 200, json: async () => ({}) }),
    true,
  );
  if (r.setupFail) {
    console.log('FAIL: setup(control): ' + r.setupFail);
    process.exit(1);
  }
  if (r.store.values[11] !== false) {
    fail('control: a successful save left the modal open');
  }
  if (r.store.values[12] !== '') {
    fail('control: a successful save left the form draft in place');
  }
  if (r.calls.refetchRoutes !== 1) {
    fail('control: a successful save did not refetch the routes (' + r.calls.refetchRoutes + ')');
  }
}

if (fails.length > 0) {
  console.log('FAIL: ' + fails.length + ' check(s) failed — route-save silent failure regression');
  process.exit(1);
}
console.log('PASS: route-save failures surface the server error, keep the modal open, and preserve the form; successful saves close + clear + refetch');
