// Regression test (round 31): ProxyRoutingView destructive actions must surface
// failures instead of silently no-opping.
//
// Defect: handleDelete (DELETE /api/proxy/routes/:id) and handleClearTraffic
// (DELETE /api/proxy/traffic) never checked res.ok and only console.error'd
// network failures — a rejected delete/clear (e.g. 401 after a daemon restart
// invalidates the per-process session cookie) was a zero-feedback silent
// no-op, and handleClearTraffic even cleared the inspector selection
// (setSelectedTraffic(null)) on the failure path.
//
// Contract pinned here:
//   failed delete/clear  -> server error rendered, no optimistic state
//                           changes, no refetch (failure is not progress)
//   successful delete    -> refetchRoutes called, no error
//   successful clear     -> selection cleared, refetchTraffic called, no error
//
// Run: node web/scripts/proxy-destructive-actions-regression.mjs
// (exit 0 = PASS, exit 1 = FAIL; self-locating, uses the repo's own vite)

import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';

const scriptDir = path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
const webDir = path.resolve(scriptDir, '..');
const root = path.resolve(webDir, '..');

const webRequire = createRequire(pathToFileURL(path.join(webDir, 'package.json')));
const vite = webRequire('vite');
const RealReact = webRequire('react');
const RealReactDOMServer = webRequire('react-dom/server');

const tsxSource = fs.readFileSync(path.join(webDir, 'src', 'components', 'ProxyRoutingView.tsx'), 'utf8');

const transformed = await vite.transformWithOxc(tsxSource, 'ProxyRoutingView.tsx');
if (!transformed || typeof transformed.code !== 'string' || transformed.code.length === 0) {
  console.log('FAIL: transform produced no code');
  process.exit(1);
}
let body = transformed.code.replace(/^import\s+type\s+.*?;?\s*$/gm, '');
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
    id: 'traffic-1',
    timestamp: '2026-09-02T20:00:00.500Z',
    method: 'POST',
    status_code: 200,
    duration_ms: 812,
    model: 'glm-5.3-flash',
    provider: 'zai',
    target_url: 'https://upstream.example/v1/chat/completions',
    prompt_tokens: 100,
    completion_tokens: 50,
    total_tokens: 150,
    cost_usd: 0.0002,
    is_stream: false,
  },
];

const realElement = (type, props, key) => RealReact.createElement(type, key !== undefined ? { ...props, key } : props);
const lucideStub = new Proxy({}, { get: () => () => null });
const chartsStub = new Proxy({}, { get: () => () => null });

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

function collectClickables(nodes, out = []) {
  for (const n of Array.isArray(nodes) ? nodes : nodes != null ? [nodes] : []) {
    if (!n || typeof n !== 'object' || !n.props) continue;
    if (typeof n.props.onClick === 'function' && n.props.onClick.length === 0) out.push(n);
    if (n.props.children) collectClickables(n.props.children, out);
  }
  return out;
}

function textOf(el) {
  let out = '';
  const walk = (n) => {
    for (const c of Array.isArray(n) ? n : n != null ? [n] : []) {
      if (typeof c === 'string' || typeof c === 'number') out += String(c);
      else if (c && typeof c === 'object' && c.props) walk(c.props.children);
    }
  };
  walk(el && el.props ? el.props.children : []);
  return out;
}

async function scenario(fetchBehavior, seed) {
  const calls = { fetch: [], refetchRoutes: 0, refetchTraffic: 0 };
  globalThis.fetch = async (url, opts) => {
    calls.fetch.push({ url: String(url), method: (opts && opts.method) || 'GET' });
    return fetchBehavior(String(url), (opts && opts.method) || 'GET');
  };
  const store = { values: seed, idx: 0 };
  const props = { currentProject: null };
  const render = () => {
    store.idx = 0;
    const mods = {
      react: makeHooks(store),
      'react/jsx-runtime': { jsx: realElement, jsxs: realElement, Fragment: RealReact.Fragment },
      'lucide-react': lucideStub,
      recharts: chartsStub,
      '../hooks/useMetrics': {
        useProxyRoutes: () => ({ data: routesFixture, refetch: () => { calls.refetchRoutes++; } }),
        useModelCatalog: () => ({ data: [] }),
        useProxyTraffic: () => ({ data: trafficFixture, refetch: () => { calls.refetchTraffic++; } }),
        useProxyTrafficDetail: () => ({ data: null }),
      },
    };
    const exportsRef = {};
    new Function('__mods', '__exports', body)(mods, exportsRef);
    const tree = exportsRef.ProxyRoutingView(props);
    return { tree, markup: RealReactDOMServer.renderToStaticMarkup(tree) };
  };
  const before = render();
  return { calls, store, render, before };
}

function fail(msg) {
  console.log('FAIL: ' + msg);
  process.exitCode = 1;
}

const denied = () => ({ ok: false, status: 401, json: async () => ({ error: 'authentication required' }) });
const okResponse = () => ({ ok: true, status: 200, json: async () => ({}) });

globalThis.window = { location: { origin: 'http://localhost:3444' } };

// ---- Failed route deletion -------------------------------------------------
{
  const seed = new Array(24).fill(undefined);
  seed[0] = 'routes';
  seed[11] = false;
  const r = await scenario((url, method) => (method === 'DELETE' && url.includes('/api/proxy/routes/route-1') ? denied() : okResponse()), seed);
  const clickable = collectClickables(r.before.tree).find((el) => el.props.title === 'Delete Route');
  if (!clickable) {
    console.log('FAIL: setup — Delete Route button not found');
    process.exit(1);
  }
  await clickable.props.onClick();
  const after = r.render();
  const del = r.calls.fetch.find((f) => f.method === 'DELETE' && f.url.includes('/api/proxy/routes/route-1'));
  if (!del) fail('the delete button did not issue DELETE /api/proxy/routes/route-1');
  if (!after.markup.includes('authentication required')) fail('failed delete did not surface the server error');
  if (r.calls.refetchRoutes !== 0) fail('the routes list was refetched after the failed delete (' + r.calls.refetchRoutes + 'x)');
}

// ---- Failed Clear Log -------------------------------------------------------
{
  const seed = new Array(24).fill(undefined);
  seed[0] = 'traffic';
  seed[1] = trafficFixture[0];
  const r = await scenario((url, method) => (method === 'DELETE' && url.includes('/api/proxy/traffic') ? denied() : okResponse()), seed);
  const clickable = collectClickables(r.before.tree).find((el) => textOf(el).includes('Clear Log'));
  if (!clickable) {
    console.log('FAIL: setup — Clear Log button not found');
    process.exit(1);
  }
  await clickable.props.onClick();
  const after = r.render();
  const clr = r.calls.fetch.find((f) => f.method === 'DELETE' && f.url.includes('/api/proxy/traffic'));
  if (!clr) fail('the Clear Log button did not issue DELETE /api/proxy/traffic');
  if (!after.markup.includes('authentication required')) fail('failed clear did not surface the server error');
  if (r.calls.refetchTraffic !== 0) fail('the traffic list was refetched after the failed clear (' + r.calls.refetchTraffic + 'x)');
  if (r.store.values[1] === null || r.store.values[1] === undefined) fail('the inspector selection was cleared on the failure path');
}

// ---- Control: successful delete --------------------------------------------
{
  const seed = new Array(24).fill(undefined);
  seed[0] = 'routes';
  const r = await scenario((url, method) => (method === 'DELETE' && url.includes('/api/proxy/routes/route-1') ? okResponse() : denied()), seed);
  const clickable = collectClickables(r.before.tree).find((el) => el.props.title === 'Delete Route');
  if (!clickable) {
    console.log('FAIL: setup — Delete Route button missing (control)');
    process.exit(1);
  }
  await clickable.props.onClick();
  const after = r.render();
  if (r.calls.refetchRoutes !== 1) fail('control: a successful delete did not refetch routes (' + r.calls.refetchRoutes + ')');
  if (after.markup.includes('Failed to delete route')) fail('control: a successful delete surfaced an error');
}

// ---- Control: successful clear ---------------------------------------------
{
  const seed = new Array(24).fill(undefined);
  seed[0] = 'traffic';
  seed[1] = trafficFixture[0];
  const r = await scenario((url, method) => (method === 'DELETE' && url.includes('/api/proxy/traffic') ? okResponse() : denied()), seed);
  const clickable = collectClickables(r.before.tree).find((el) => textOf(el).includes('Clear Log'));
  if (!clickable) {
    console.log('FAIL: setup — Clear Log button missing (control)');
    process.exit(1);
  }
  await clickable.props.onClick();
  const after = r.render();
  if (r.calls.refetchTraffic !== 1) fail('control: a successful clear did not refetch traffic (' + r.calls.refetchTraffic + ')');
  if (r.store.values[1] !== null) fail('control: a successful clear did not reset the inspector selection');
  if (after.markup.includes('Failed to clear')) fail('control: a successful clear surfaced an error');
}

if (process.exitCode === 1) {
  console.log('FAIL: destructive actions are silent failures — reproducible');
  process.exit(1);
}
console.log('PASS: route deletes and log clears surface failures without optimistic changes; successful paths keep their contract');
