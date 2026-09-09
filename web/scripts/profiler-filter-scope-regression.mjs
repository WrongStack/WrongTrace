// Regression test (round 4): ProfilerTracesView's type-filter chips must
// govern EVERYTHING below them — the events badge, the traces table, AND the
// two charts — not just the table.
//
// Defect (pre-fix): the filter chips drove `filterType` into `filteredTraces`,
// which scoped the badge and the traces table, but `timelineChartData`,
// `serviceChartData` and the charts-panel gate (`traces.length > 0`) all
// consumed the RAW traces array. Under an active filter the charts therefore
// plotted traces the filter had excluded — up to "No matching runtime traces
// found." rendered in the same panel as a chart still plotting those
// non-matching traces.
//
// Contract: everything below the chips describes the filtered dataset; when
// the filter matches nothing, the charts panel hides with the table's empty
// state. Deliberately OUT of scope: the KPI row (Total Traces / P50 / P90-P99
// / Active Services / Runtime Errors) — it is the labeled GLOBAL summary
// (overview-first), so its cards legitimately ignore the filter.
//
// Run: node web/scripts/profiler-filter-scope-regression.mjs
// Exit 0 = pass, 1 = fail. Transpiles the REAL component with the repo's own
// vite; no test framework or extra dependency.
//
// Harness notes for the next editor (each cost a debugging cycle once):
//   - `store.idx` is the slot index of the NEXT useState call and must start
//     at 0; the component's slots are 0 selectedTrace, 1 filterType,
//     2 chartView. Seeding slot 1 drives the chips without a DOM.
//   - oxc emits ALIASED imports (`jsx as _jsx`); the import shim converts
//     them or the evaluated body is a SyntaxError.
//   - Stub components must pass children INSIDE props — the old
//     realElement('div', attrs, props.children) put children in the `key`
//     slot, so the AreaChart never rendered and every chart assertion passed
//     vacuously. The setup checks below (badge counts + markers) would then
//     lie. Keep them.
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

const tsxSource = fs.readFileSync(path.join(root, 'web', 'src', 'components', 'ProfilerTracesView.tsx'), 'utf8');
const transformed = await vite.transformWithOxc(tsxSource, 'ProfilerTracesView.tsx');
if (transformed.errors && transformed.errors.length > 0) {
  console.log('FAIL: transform errors: ' + JSON.stringify(transformed.errors));
  process.exit(1);
}
const code = transformed.code;

// Fixture: two OTLP traces (one ok, one erroring) and one large pprof trace.
// The 'pprof' filter must exclude both OTLP traces; 'test_runner' matches
// nothing.
const tracesFixture = [
  { trace_id: 't-ok',  profiler_type: 'otlp',  duration_ms: 100,  status_code: 200, error_msg: '',    service_name: 'svc-a', node_signature: 'MARKER-OTLP-OK',  file_path: 'a.go', timestamp: '2026-09-09T10:00:01Z' },
  { trace_id: 't-err', profiler_type: 'otlp',  duration_ms: 200,  status_code: 500, error_msg: 'boom', service_name: 'svc-a', node_signature: 'MARKER-OTLP-ERR', file_path: 'b.go', timestamp: '2026-09-09T10:00:02Z' },
  { trace_id: 't-pp',  profiler_type: 'pprof', duration_ms: 9000, status_code: 200, error_msg: '',    service_name: 'svc-b', node_signature: 'MARKER-PPROF',    file_path: 'c.go', timestamp: '2026-09-09T10:00:03Z' },
];

const lucideStub = new Proxy({}, { get: () => () => null });
const realElement = (type, props, key) =>
  RealReact.createElement(type, key !== undefined ? { ...props, key } : props);
// Every recharts component renders its `data` into the markup as JSON so the
// chart's plotted points are assertable; ResponsiveContainer passes children
// through. Children ride INSIDE props — see the harness notes above.
const rechartsStub = new Proxy({}, {
  get: (_, name) => (props = {}) =>
    realElement('div', {
      'data-chart': String(name),
      'data-points': JSON.stringify(props.data ?? null),
      children: props.children ?? null,
    }),
});

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

const bodySansImports = (() => {
  let body = code.replace(/^import\s+type\s+.*?;?\s*$/gm, '');
  const convertNames = (names) =>
    names
      .split(',')
      .map((piece) => {
        const t = piece.trim();
        if (!t) return '';
        const m = t.match(/^(\S+)\s+as\s+(\S+)$/);
        return m ? `${m[1]}: ${m[2]}` : t;
      })
      .filter(Boolean)
      .join(', ');
  body = body.replace(/^import\s+(\w+)(?:\s*,\s*)?\s*\{([^}]*)\}\s*from\s*['"]([^'"]+)['"];?\s*$/gm, (_, def, names, spec) => {
    const specLit = JSON.stringify(spec);
    return `const {${convertNames(names)}} = __mods[${specLit}]; const ${def} = __mods[${specLit}].default;`;
  });
  body = body.replace(/^import\s*\{([^}]*)\}\s*from\s*['"]([^'"]+)['"];?\s*$/gm, (_, names, spec) => {
    return `const {${convertNames(names)}} = __mods[${JSON.stringify(spec)}];`;
  });
  body = body.replace(/^import\s+(\w+)\s+from\s*['"]([^'"]+)['"];?\s*$/gm, (_, def, spec) => {
    return `const ${def} = __mods[${JSON.stringify(spec)}].default;`;
  });
  body = body.replace(/^export\s+/gm, '');
  body += '\n__exports.ProfilerTracesView = ProfilerTracesView;';
  if (/^\s*import\s/m.test(body)) {
    console.log('FAIL: setup — unresolved import lines remain after shimming');
    process.exit(1);
  }
  return body;
})();

function render(filterType) {
  const store = { values: { 0: null, 1: filterType, 2: 'latency' }, idx: 0 };
  const useMetricsStub = {
    useProfilerTraces: () => ({ data: tracesFixture, refetch() {}, isLoading: false }),
    useProfilerHotspots: () => ({ data: [], refetch() {}, isLoading: false }),
    useProfilerOverview: () => ({ data: undefined, refetch() {}, isLoading: false }),
  };
  const mods = {
    react: makeHooks(store),
    'react/jsx-runtime': { jsx: realElement, jsxs: realElement, Fragment: RealReact.Fragment },
    'lucide-react': lucideStub,
    recharts: rechartsStub,
    '../hooks/useMetrics': useMetricsStub,
  };
  const fn = new Function('__mods', '__exports', bodySansImports);
  const exportsRef = {};
  fn(mods, exportsRef, RealReact);
  const tree = exportsRef.ProfilerTracesView({});
  return RealReactDOMServer.renderToStaticMarkup(tree);
}

const fails = [];
function fail(msg) {
  fails.push(msg);
  console.log('FAIL: ' + msg);
}

// Scenario A (control): filterType 'all' — charts and table show everything.
const allView = render('all');
for (const marker of ['MARKER-OTLP-OK', 'MARKER-OTLP-ERR', 'MARKER-PPROF']) {
  if (!allView.includes(marker)) {
    fail(`control: ${marker} missing from the unfiltered panel`);
  }
}
if (!allView.includes('3 events')) fail('control: unfiltered badge should read "3 events"');

// Scenario B: filterType 'pprof' — the two OTLP traces are excluded by the
// chips, so neither the table NOR the charts may plot them.
const pprofView = render('pprof');
if (!pprofView.includes('1 events')) {
  fail(`setup: the pprof-filtered badge should read "1 events" (got "${pprofView.match(/(\d+) events/)?.[1]} events")`);
}
if (!pprofView.includes('MARKER-PPROF')) {
  fail("setup: the pprof trace's own row/chart point went missing under the pprof filter");
}
for (const marker of ['MARKER-OTLP-OK', 'MARKER-OTLP-ERR']) {
  if (pprofView.includes(marker)) {
    fail(
      `LEAK: trace "${marker}" (profiler_type otlp) is rendered while the pprof filter is active — ` +
        'the latency chart plots traces the filter excluded',
    );
  }
}

// Scenario C: filterType 'test_runner' matches NOTHING. The table says so;
// the charts must not plot the excluded traces.
const noneView = render('test_runner');
if (!noneView.includes('No matching runtime traces found.')) {
  fail('setup: the empty-filter table state ("No matching runtime traces found.") did not render');
}
if (!noneView.includes('0 events')) {
  fail(`setup: the empty-filter badge should read "0 events" (got "${noneView.match(/(\d+) events/)?.[1]} events")`);
}
for (const marker of ['MARKER-OTLP-OK', 'MARKER-OTLP-ERR', 'MARKER-PPROF']) {
  if (noneView.includes(marker)) {
    fail(
      `CONTRADICTION: "No matching runtime traces found." is rendered while trace "${marker}" ` +
        'is still plotted by the panel chart — the charts ignore the type filter',
    );
  }
}

if (fails.length > 0) {
  console.log(`FAIL: ${fails.length} check(s) failed — profiler filter scope`);
  process.exit(1);
}
console.log(
  'PASS: under every type filter the charts plot exactly the filtered dataset; the empty-filter state plots nothing',
);
