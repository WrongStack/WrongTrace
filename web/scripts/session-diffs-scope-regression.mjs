// Regression test (round 38): AgentSessionsView "Code Changes Produced in
// this Session" must not claim OTHER sessions' diffs.
//
// Contract (mirrors the repo-wide identity-filter doctrine — exact match plus
// unattributed passthrough, cf. the server-side `repo_name = ? OR '' OR NULL`):
//   - a session shows diffs whose run_id equals the session's run_id,
//   - the daemon session (agent_name 'WrongStack') ADDITIONALLY shows
//     unattributed events (run_id '') — watcher activity with no active run
//     (engine.go: tool_path 0.95 -> single_active_run 0.60 -> unattributed),
//   - a session NEVER shows diffs attributed to a DIFFERENT run: the previous
//     `|| selectedRun.agent_name === 'WrongStack'` bypass pulled every recent
//     diff into the WrongStack panel, double-attributing other agents' work.
//
// Run: node web/scripts/session-diffs-scope-regression.mjs
// Exit 0 = pass, 1 = fail. Uses the repo's own vite (web/node_modules) to
// transpile the real component; no test framework or extra dependency.
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import path from 'node:path';

const scriptDir = path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
const root = path.resolve(scriptDir, '..', '..');

const webRequire = createRequire(pathToFileURL(path.join(root, 'web', 'package.json')));
const vite = webRequire('vite');
const RealReact = webRequire('react');
const RealReactDOMServer = webRequire('react-dom/server');

const tsxSource = await webRequire('fs').promises.readFile(
  path.join(root, 'web', 'src', 'components', 'AgentSessionsView.tsx'),
  'utf8',
);
const transformed = await vite.transformWithOxc(tsxSource, 'AgentSessionsView.tsx');
if (transformed.errors && transformed.errors.length > 0) {
  console.log('FAIL: transform errors: ' + JSON.stringify(transformed.errors));
  process.exit(1);
}
const code = transformed.code;

const runDaemon = {
  run_id: 'run-daemon',
  agent_name: 'WrongStack',
  model_name: 'wrongtrace-daemon',
  task_id: 'watch',
  project_id: 'p1',
  started_at: '2026-09-06T10:00:00Z',
  last_seen: '2026-09-06T10:05:00Z',
};
const runClaude = {
  run_id: 'run-claude',
  agent_name: 'claude-code',
  model_name: 'claude-x',
  task_id: 't2',
  project_id: 'p1',
  started_at: '2026-09-06T10:01:00Z',
  last_seen: '2026-09-06T10:06:00Z',
};

const eventsFixture = [
  { event_id: 'evt-own', run_id: 'run-daemon', agent_name: 'WrongStack', file_path: 'own.go', node_signature: 'MARKER-OWN-SIG', action: 'MODIFIED', diff_snippet: 'x', event_time: '2026-09-06T10:02:00Z' },
  { event_id: 'evt-foreign', run_id: 'run-claude', agent_name: 'claude-code', file_path: 'foreign.go', node_signature: 'MARKER-FOREIGN-SIG', action: 'MODIFIED', diff_snippet: 'x', event_time: '2026-09-06T10:03:00Z' },
  { event_id: 'evt-unattr', run_id: '', agent_name: '', file_path: 'watched.go', node_signature: 'MARKER-UNATTR-SIG', action: 'MODIFIED', diff_snippet: 'x', event_time: '2026-09-06T10:04:00Z' },
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

function seed(selectedRunId) {
  const v = [];
  v[0] = selectedRunId; // slot 0: selectedRunId
  v[1] = 'sessions'; // slot 1: activeSubTab
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
  body += '\n__exports.AgentSessionsView = AgentSessionsView;';
  return body;
})();

function render(selectedRunId) {
  const hooksMods = {
    useModelCatalog: () => ({ data: [], refetch: () => {}, isLoading: false }),
    useProviderCatalog: () => ({ data: [], refetch: () => {}, isLoading: false }),
  };
  const store = { values: seed(selectedRunId), idx: 0 };
  const mods = {
    react: makeHooks(store),
    'react/jsx-runtime': { jsx: realElement, jsxs: realElement, Fragment: RealReact.Fragment },
    'lucide-react': lucideStub,
    '../hooks/useMetrics': hooksMods,
    './RichDiffViewer': { RichDiffViewer: () => null },
    '../lib/clipboard': { copyToClipboard: async () => true },
    '../types': {
      isJunkModel: () => false,
      formatCleanModel: (m, a) => m || a || '',
    },
  };
  const fn = new Function('__mods', '__exports', bodySansImports);
  const exportsRef = {};
  fn(mods, exportsRef, RealReact);
  const tree = exportsRef.AgentSessionsView({
    activeRuns: [runDaemon, runClaude],
    models: [],
    overview: undefined,
    recentEvents: eventsFixture,
    loading: false,
    currentProject: null,
  });
  return RealReactDOMServer.renderToStaticMarkup(tree);
}

const fails = [];
function fail(msg) {
  fails.push(msg);
  console.log('FAIL: ' + msg);
}

// Scenario A: the daemon session (agent 'WrongStack') is selected.
const daemonView = render('run-daemon');
if (!daemonView.includes('MARKER-OWN-SIG')) {
  fail("setup: the daemon run's own diff did not render (marker 'MARKER-OWN-SIG' absent)");
}
if (!daemonView.includes('MARKER-UNATTR-SIG')) {
  fail("contract: unattributed watcher diffs stopped rendering under the WrongStack session (marker 'MARKER-UNATTR-SIG' absent)");
}
if (daemonView.includes('MARKER-FOREIGN-SIG')) {
  fail(
    'LEAK: a diff attributed to run "run-claude" (agent claude-code) renders under the WrongStack session — the agent-name bypass claims another session\'s work as "produced in this session" (marker \'MARKER-FOREIGN-SIG\' present)',
  );
}

// Scenario B (control): the claude session is selected — exact run matching.
const claudeView = render('run-claude');
if (!claudeView.includes('MARKER-FOREIGN-SIG')) {
  fail("control: the claude run's own diff did not render (marker 'MARKER-FOREIGN-SIG' absent)");
}
if (claudeView.includes('MARKER-OWN-SIG')) {
  fail("control: the WrongStack run's diff leaked into the claude session (marker 'MARKER-OWN-SIG' present)");
}
if (claudeView.includes('MARKER-UNATTR-SIG')) {
  fail("control: unattributed events leaked into the claude session (marker 'MARKER-UNATTR-SIG' present)");
}

if (fails.length > 0) {
  console.log(`FAIL: ${fails.length} check(s) failed — session-diffs scope regression`);
  process.exit(1);
}
console.log(
  "PASS: the WrongStack session shows its own + unattributed diffs only; foreign-run diffs stay in their own session; the claude session shows exactly its own run's diff",
);
