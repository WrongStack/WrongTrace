// Regression test (round r5, F211): SettingsView must not save before the
// settings query has loaded. The form holds placeholder defaults until then
// and the POST is a full replacement, so a save sent ignore_patterns: [] and
// the '-' clear marker for every webhook, wiping them server-side.
//
// Contract: before load no POST is sent and the save buttons are disabled;
// after load the persisted values round-trip unchanged.
//
// Run: node web/scripts/settings-save-before-load-regression.mjs
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


const body = await loadBody('components/SettingsView.tsx', ['SettingsView']);
globalThis.setTimeout = () => 0; // avoid the 3s "Saved!" timer keeping node alive

function hooks(store) {
  return {
    ...RealReact,
    useState(initial) {
      const i = store.idx++;
      if (!(i in store.values)) store.values[i] = typeof initial === 'function' ? initial() : initial;
      return [store.values[i], (v) => { store.values[i] = typeof v === 'function' ? v(store.values[i]) : v; }];
    },
    useMemo: (fn) => fn(), useCallback: (fn) => fn, useRef: (v) => ({ current: v }),
    useEffect: (fn) => { fn(); }, // run effects synchronously so hydration (when data exists) happens
  };
}
function findForm(node) {
  if (!node || typeof node !== 'object') return null;
  if (Array.isArray(node)) { for (const n of node) { const f = findForm(n); if (f) return f; } return null; }
  if (node.type === 'form' && node.props?.onSubmit) return node;
  return findForm(node.props?.children);
}

async function saveWith(settingsData) {
  const store = { values: ['watcher'], idx: 0 }; // slot 0 = activeSubTab
  const mods = {
    react: hooks(store),
    '@tanstack/react-query': { useQueryClient: () => ({ resetQueries() {} }) },
    'react/jsx-runtime': { jsx: realElement, jsxs: realElement, Fragment: RealReact.Fragment },
    'lucide-react': lucideStub,
    '../hooks/useMetrics': {
      useSettings: () => ({ data: settingsData, refetch() {} }),
      useProjects: () => ({ data: [], refetch() {} }),
    },
  };
  const { SettingsView } = evalBody(body, mods);
  SettingsView(); store.idx = 0; // render 1 (effects hydrate if data present)
  const tree = SettingsView(); store.idx = 0; // render 2 with hydrated state
  let posted = null;
  globalThis.fetch = async (url, init) => { posted = { url, body: JSON.parse(init.body) }; return { ok: true, json: async () => ({}) }; };
  // user moves the debounce slider, then clicks "Save Watcher Config"
  await findForm(tree).props.onSubmit({ preventDefault() {} });
  return { posted, tree };
}

// Faithful port of the merge in internal/core/settings.go UpdateSettings (lines ~143-175).
function goMerge(cur, s) {
  const next = { ...cur };
  if (s.debounce_ms > 0) next.debounce_ms = s.debounce_ms;
  if (s.ignore_patterns != null) next.ignore_patterns = [...s.ignore_patterns];
  for (const k of ['slack_webhook_url', 'discord_webhook_url', 'custom_webhook_url']) {
    const v = s[k];
    if (v === '-' || v === 'none' || v === 'CLEAR') next[k] = '';
    else if (v) next[k] = v;
  }
  return next;
}

const server = {
  debounce_ms: 250, thrashing_threshold: 3, fragility_cutoff: 50, cost_alert_usd: 25, auto_prune_days: 90,
  default_provider: 'Anthropic',
  ignore_patterns: ['.git', 'node_modules', 'generated', 'third_party'],
  slack_webhook_url: 'https://hooks.slack.com/services/T0/B0/xyz',
  discord_webhook_url: 'https://discord.com/api/webhooks/1/abc',
  custom_webhook_url: 'https://alerts.example.internal/hook',
};

function submitButtons(node, out = []) {
  if (!node || typeof node !== 'object') return out;
  if (Array.isArray(node)) { for (const n of node) submitButtons(n, out); return out; }
  if (node.type === 'button' && node.props?.type === 'submit') out.push(node);
  submitButtons(node.props?.children, out);
  return out;
}
let ok = true;
const check = (label, cond, detail) => { console.log(`${cond ? 'ok  ' : 'FAIL'} ${label}${detail ? ': ' + detail : ''}`); if (!cond) ok = false; };

// 1. Not loaded: nothing is posted, so nothing persisted can be cleared.
const r1 = await saveWith(undefined);
check('not loaded: no POST', r1.posted === null, JSON.stringify(r1.posted?.body));
const btns1 = submitButtons(r1.tree);
check('not loaded: save buttons disabled', btns1.length > 0 && btns1.every((b) => b.props.disabled), `${btns1.length} submit buttons`);

// 2. Loaded: values round-trip unchanged.
const r2 = await saveWith(server);
const after2 = goMerge(server, r2.posted.body);
check('loaded: ignore patterns kept', JSON.stringify(after2.ignore_patterns) === JSON.stringify(server.ignore_patterns));
check('loaded: webhooks kept', after2.slack_webhook_url === server.slack_webhook_url && after2.discord_webhook_url === server.discord_webhook_url && after2.custom_webhook_url === server.custom_webhook_url);
check('loaded: buttons enabled', submitButtons(r2.tree).every((b) => !b.props.disabled));

// 3. Loaded with empty webhooks: the clear sentinel round-trips to empty.
const bare = { ...server, slack_webhook_url: '', discord_webhook_url: '', custom_webhook_url: '', ignore_patterns: ['.git'] };
const r3 = await saveWith(bare);
const after3 = goMerge(bare, r3.posted.body);
check('loaded, empty webhooks: stay empty, patterns kept', after3.slack_webhook_url === '' && JSON.stringify(after3.ignore_patterns) === '[".git"]');

if (!ok) { console.log('FAIL'); process.exit(1); }
console.log('PASS');
