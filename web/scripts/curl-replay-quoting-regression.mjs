// Regression test (round 33): ProxyRoutingView.generateCurlCommand must produce
// a REPLAYABLE cURL command (the button's own contract: "Copy replayable cURL
// terminal command").
//
// Contract: every captured value (method, origin+incoming_path, each request
// header "name: value", the request body) is POSIX single-quoted with '\''
// doubling, so a `"` or $(...)/$VAR/backtick in captured traffic can neither
// corrupt the command nor execute when the copied line is pasted. Request
// headers are arbitrary agent traffic (maskHeaders only masks secret-looking
// values; everything else is stored verbatim), so hostile header content is
// realistic. The body path already used this convention before the fix; the
// header/method/URL paths now share it.
//
// Run: node web/scripts/curl-replay-quoting-regression.mjs
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

// One record whose captured headers exercise every shell-sensitive character.
// `X-Note` carries a double quote (breaks double-quoting) plus $(id) (command
// substitution); `X-Quote` carries a single quote (needs POSIX doubling).
const curlRecord = {
  id: 'px-curl',
  timestamp: '2026-09-03T10:00:00.000Z',
  method: 'POST',
  incoming_path: '/proxy/px-curl/v1/chat/completions',
  target_url: 'https://upstream.example/v1/chat/completions',
  provider: 'Upstream',
  model: 'model-curl',
  status_code: 200,
  is_stream: false,
  request_headers: {
    Accept: 'application/json',
    'X-Note': 'v1" & $(id)',
    'X-Quote': "it's fine",
  },
  request_body: `{"note":"it's fine"}`,
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
};

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
// 2 trafficFilter, 3 statusFilter, 4 projectScope, 5 trafficInspectorTab, ...
// Scope 'all' decouples this proof from the round-32 project-scope filter.
const store = {
  values: (() => {
    const v = new Array(24).fill(undefined);
    v[0] = 'traffic';
    v[1] = curlRecord;
    v[4] = 'all';
    return v;
  })(),
  idx: 0,
};

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

function collectByTitle(node, out = []) {
  if (node == null || typeof node !== 'object' || !node.props) return out;
  if (typeof node.props.onClick === 'function' && typeof node.props.title === 'string') {
    out.push(node);
  }
  const kids = node.props.children;
  if (Array.isArray(kids)) kids.forEach((k) => collectByTitle(k, out));
  else if (kids != null) collectByTitle(kids, out);
  return out;
}

let capturedCommand = null;
try {
  Object.defineProperty(globalThis, 'navigator', {
    configurable: true,
    value: { clipboard: { writeText: (s) => { capturedCommand = String(s); } } },
  });
} catch {
  console.log('FAIL: setup: could not stub navigator.clipboard');
  process.exit(1);
}
globalThis.window = { location: { origin: 'http://localhost:3444' } };

const mods = {
  react: makeHooks(store),
  'react/jsx-runtime': { jsx: realElement, jsxs: realElement, Fragment: RealReact.Fragment },
  'lucide-react': lucideStub,
  recharts: chartsStub,
  '../hooks/useMetrics': {
    useProxyRoutes: () => ({ data: [], refetch: () => {} }),
    useModelCatalog: () => ({ data: [] }),
    useProxyTraffic: () => ({ data: [curlRecord], refetch: () => {} }),
    useProxyTrafficDetail: () => ({ data: null }),
  },
};
const fn = new Function('__mods', '__exports', bodySansImports);
const exportsRef = {};
fn(mods, exportsRef, RealReact);
const tree = exportsRef.ProxyRoutingView({ currentProject: null });

const titled = collectByTitle(tree);
const curlButton = titled.find((n) => n.props.title === 'Copy replayable cURL terminal command');
if (!curlButton) {
  console.log('FAIL: setup: the "Copy replayable cURL terminal command" button did not render for the selected record');
  process.exit(1);
}
curlButton.props.onClick();
if (capturedCommand == null) {
  console.log('FAIL: setup: clicking the cURL button did not put a command on the clipboard');
  process.exit(1);
}

const cmd = capturedCommand;
const fails = [];
function fail(msg) {
  fails.push(msg);
  console.log('FAIL: ' + msg);
}

// Control: the body path already used POSIX single-quote doubling — must keep
// working (regression guard for the fix).
if (!cmd.endsWith(`-d '{"note":"it'\\''s fine"}'`)) {
  fail(`control: the request body is not POSIX-escaped as before (want suffix -d '{"note":"it'\\''s fine"}')`);
}
// Contract 1: no header argument may live in a double-quoted context — a `"`
// in a value terminates it, and $/backticks are shell-interpreted there.
if (cmd.includes('-H "')) {
  fail('contract: header arguments are double-quoted (a `"` in a captured value corrupts the command; $(...) and $VAR inside double quotes are EXECUTED by the shell on paste)');
}
// Contract 2: the hostile header must appear fully literal inside single quotes.
if (!cmd.includes(`-H 'X-Note: v1" & $(id)'`)) {
  fail('contract: the hostile header value v1" & $(id) is not carried literally inside a single-quoted argument');
}
// Contract 3: embedded single quotes must use the POSIX doubling the body uses.
if (!cmd.includes(`-H 'X-Quote: it'\\''s fine'`)) {
  fail('contract: a single quote inside a header value is not POSIX-doubled (command would terminate the argument early)');
}
// Contract 4: method and URL are captured data too — same escaping.
if (!cmd.startsWith(`curl -X 'POST' 'http://localhost:3444/proxy/px-curl/v1/chat/completions'`)) {
  fail('contract: the captured method/URL are interpolated unquoted (only safe while they happen to contain no shell metacharacters)');
}
// Contract 5: the clean header stays intact.
if (!cmd.includes(`-H 'Accept: application/json'`)) {
  fail('contract: the clean header lost its argument shape');
}

if (fails.length > 0) {
  console.log('--- generated command ---\n' + cmd + '\n-------------------------');
  console.log(`FAIL: ${fails.length} check(s) failed — generateCurlCommand produces a non-replayable command`);
  process.exit(1);
}
console.log('PASS: the copied cURL command is POSIX-safe and replayable (method/URL/headers single-quoted with doubling; body escaping preserved)');
