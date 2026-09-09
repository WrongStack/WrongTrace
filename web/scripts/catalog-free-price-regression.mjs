// Regression test (round 37): AgentSessionsView groupedCatalog lowest-price
// aggregation must treat price 0 as a REAL price (free / self-hosted models),
// not as an "unset" sentinel.
//
// Defect (pre-fix): the aggregation initializes lowestInputPrice from the
// first provider entry (accepting 0), but the update path requires
//   m.input_price_per_m > 0 && (g.lowestInputPrice === 0 || m < lowest)
// so any subsequent PAID entry overwrites a correct 0 (free-first), and a
// free model arriving last never applies at all (0 > 0 is false) — in both
// orders a 0-priced provider loses "Best: $X/1M" and bestProvider to a paid
// one. The row highlight compounded it: isCheapest required p.inputPrice > 0,
// so a free provider could never be marked "Lowest".
//
// Data reality: 0-priced models are a supported shape — the registry's own
// tests model "custom-ollama-qwen" with InputPricePerM/OutputPricePerM 0.0
// under "Local / Self-Hosted" (internal/models/registry_test.go), Ollama is a
// first-class upstream, and the shipped custom-model form accepts 0.
//
// Contract (catalog compare view, "Provider Pricing Comparison"):
//   - a group with a free (input 0) + a paid provider renders
//     "Best: $0.00/1M" and highlights the FREE provider's row with the
//     "Lowest" badge,
//   - the paid provider's row is NOT highlighted in that group,
//   - control: an all-positive group still renders its true minimum and
//     highlights the cheaper provider (must hold pre-fix too).
//
// Run: node web/scripts/catalog-free-price-regression.mjs
// Exit 0 = pass, 1 = fail. Transpiles the REAL component with the repo's own
// vite; no test framework or extra dependency.
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

// qwen3: one FREE provider (input 0) listed FIRST and one PAID provider —
// pre-fix the paid entry overwrites the correct $0 minimum (free-first case).
// ctrl: two positive-price providers — the control group must behave
// identically before and after the fix.
const catalogFixture = [
  {
    id: 'free-local/qwen3', model_id: 'qwen3', name: 'Qwen3 (Free)', provider: 'free-local',
    input_price_per_m: 0, output_price_per_m: 0, cache_read_price_per_m: 0,
    context_window: 32000, is_canonical: false, is_custom: false, description: 'Self-hosted free variant',
  },
  {
    id: 'paid-cloud/qwen3', model_id: 'qwen3', name: 'Qwen3 (Paid)', provider: 'paid-cloud',
    input_price_per_m: 5, output_price_per_m: 15, cache_read_price_per_m: 0,
    context_window: 128000, is_canonical: true, is_custom: false, description: '',
  },
  {
    id: 'paid-a/ctrl', model_id: 'ctrl', name: 'Control A', provider: 'paid-a',
    input_price_per_m: 3, output_price_per_m: 9, cache_read_price_per_m: 0,
    context_window: 8000, is_canonical: false, is_custom: false,
  },
  {
    id: 'paid-b/ctrl', model_id: 'ctrl', name: 'Control B', provider: 'paid-b',
    input_price_per_m: 7, output_price_per_m: 11, cache_read_price_per_m: 0,
    context_window: 8000, is_canonical: false, is_custom: false,
  },
];

const lucideStub = new Proxy({}, { get: () => () => null });

const allElements = [];
const recordingElement = (type, props, key) => {
  const el = RealReact.createElement(type, key !== undefined ? { ...props, key } : props);
  if (el && typeof el === 'object') allElements.push(el);
  return el;
};

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

// useState slot map (AgentSessionsView): 0 selectedRunId, 1 activeSubTab,
// 2 search, 3 catalogViewMode, ... The array must stay SPARSE — only the
// slots we seed are present so every other useState auto-seeds its declared
// initial value.
function seed() {
  const v = [];
  v[1] = 'catalog'; // activeSubTab — the catalog surface hosts the compare view
  v[3] = 'compare'; // catalogViewMode — grouped "Provider Pricing Comparison"
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

function render() {
  const hooksMods = {
    useModelCatalog: () => ({ data: catalogFixture, refetch: () => {} }),
    useProviderCatalog: () => ({ data: [], refetch: () => {} }),
  };
  const store = seed();
  const mods = {
    react: makeHooks(store),
    'react/jsx-runtime': { jsx: recordingElement, jsxs: recordingElement, Fragment: RealReact.Fragment },
    'lucide-react': lucideStub,
    './RichDiffViewer': { RichDiffViewer: () => null },
    '../hooks/useMetrics': hooksMods,
    '../lib/clipboard': { copyToClipboard: async () => true },
    '../types': { isJunkModel: () => false, formatCleanModel: (s) => s },
  };
  const fn = new Function('__mods', '__exports', bodySansImports);
  const exportsRef = {};
  fn(mods, exportsRef, RealReact);
  const tree = exportsRef.AgentSessionsView({
    activeRuns: [],
    models: [],
    overview: undefined,
    recentEvents: [],
    loading: false,
    currentProject: null,
  });
  return RealReactDOMServer.renderToStaticMarkup(tree);
}

function subtreeText(el) {
  let out = '';
  const walk = (node) => {
    if (node == null || typeof node === 'boolean' || typeof node === 'function') return;
    if (typeof node === 'string' || typeof node === 'number') {
      out += String(node);
      return;
    }
    if (Array.isArray(node)) {
      node.forEach(walk);
      return;
    }
    if (node.props && node.props.children !== undefined) walk(node.props.children);
  };
  walk(el.props ? el.props.children : el);
  return out;
}

// The pricing-comparison row: the only element class carrying the conditional
// cheapest/slate pair whose subtree mentions the given provider.
function rowFor(markupUnused, provider) {
  return allElements.filter((el) => {
    const cn = el.props && typeof el.props.className === 'string' ? el.props.className : '';
    return cn.includes('p-2 rounded-lg border text-xs font-mono') && subtreeText(el).includes(provider);
  });
}

globalThis.window = { location: { origin: 'http://localhost:3444' } };

allElements.length = 0;
const markup = render();

const fails = [];
const check = (ok, msg) => {
  if (!ok) {
    fails.push(msg);
    console.log('FAIL: ' + msg);
  }
};

// Setup guards: both groups and all providers must render.
check(markup.includes('Provider Pricing Comparison'), 'setup: comparison block did not render');
check(markup.includes('free-local'), 'setup: free provider row missing from markup');
check(markup.includes('paid-cloud'), 'setup: paid provider row missing from markup');
check(markup.includes('paid-a'), 'setup: control provider row missing from markup');

// (A) Header badge: the free-containing group must advertise $0.00/1M.
check(
  markup.includes('Best: $0.00/1M'),
  `LEAK-by-sentinel: group with a FREE provider advertises "${(markup.match(/Best: \$[0-9.]+\/1M/) || ['<none>'])[0]}" instead of "Best: $0.00/1M" — price 0 was treated as an unset sentinel`,
);
// Control: the all-positive group still renders its true minimum.
check(markup.includes('Best: $3.00/1M'), 'control: all-positive group must still render "Best: $3.00/1M"');

// (B) Row highlight + Lowest badge: the FREE provider's row carries them, the
// paid provider's row in the same group does not.
const freeRows = rowFor(markup, 'free-local');
const paidRows = rowFor(markup, 'paid-cloud');
check(freeRows.length === 1, `setup: expected exactly 1 free-local pricing row, found ${freeRows.length}`);
check(paidRows.length === 1, `setup: expected exactly 1 paid-cloud pricing row, found ${paidRows.length}`);

if (freeRows.length === 1) {
  const cn = freeRows[0].props.className;
  check(
    cn.includes('bg-emerald-950/20'),
    `LEAK-by-sentinel: the FREE provider's row is not highlighted as cheapest (class "${cn}")`,
  );
  check(
    subtreeText(freeRows[0]).includes('Lowest'),
    'LEAK-by-sentinel: the FREE provider\'s row lacks the "Lowest" badge',
  );
}
if (paidRows.length === 1) {
  const cn = paidRows[0].props.className;
  check(
    cn.includes('bg-slate-950/60'),
    `contract: the PAID provider's row is highlighted although a free provider exists in the group (class "${cn}")`,
  );
  check(
    !subtreeText(paidRows[0]).includes('Lowest'),
    'contract: the PAID provider\'s row wrongly carries the "Lowest" badge while a free provider exists',
  );
}

// (C) Control rows (all-positive group): cheaper provider highlighted with
// the Lowest badge — this holds before AND after the fix.
const ctrlACheapest = allElements.filter((el) => {
  const cn = el.props && typeof el.props.className === 'string' ? el.props.className : '';
  return (
    cn.includes('p-2 rounded-lg border text-xs font-mono') &&
    cn.includes('bg-emerald-950/20') &&
    subtreeText(el).includes('paid-a')
  );
});
check(
  ctrlACheapest.length === 1 && subtreeText(ctrlACheapest[0]).includes('Lowest'),
  'control: the cheaper all-positive provider must keep its highlight + Lowest badge',
);

if (fails.length > 0) {
  console.log(`FAIL: ${fails.length} check(s) failed — catalog lowest-price free-model regression`);
  process.exit(1);
}
console.log(
  'PASS: free (0-priced) providers keep "Best: $0.00/1M" and the Lowest highlight; paid-only groups and unattributed passthroughs unchanged',
);
