// Regression test (round 96): the CodeAtlas selected-file symbol-count
// expression (web/src/components/CodeAtlas.tsx, the
// selectedItem.file.symbols.length site) must survive the
// include_symbols=false wire shape that GET /api/atlas?include_symbols=false
// actually serves. core.AtlasFile carries `Symbols []AtlasSymbol
// json:"symbols"` with NO omitempty (atlas.go:70) and Handlers.Atlas sets
// pkg.Files[i].Symbols = nil in that mode (handlers.go:422-425), so the
// payload's files have an explicit "symbols": null — while the component used
// to dereference file.symbols unguarded.
//
// Defect (pre-fix): the expression threw
//   TypeError: Cannot read properties of null (reading 'length')
// on a symbols-null payload. Fix: `symbols: AtlasSymbol[] | null` in the
// type mirror (web/src/types/index.ts) + `(file.symbols ?? [])` guards at
// every .symbols dereference in CodeAtlas.tsx.
//
// The expression is SLICED VERBATIM from the production file at runtime, so
// this harness always tests the code that actually ships (house pattern:
// atlas-symbol-sort-regression.mjs). The slice starts at the JSX brace
// enclosing the anchor and ends at "} nodes", which stays valid across
// pre-fix (`{selectedItem.file.symbols.length}`) and post-fix
// (`{(selectedItem.file.symbols ?? []).length}`) spellings.
//
// Run: node web/scripts/atlas-symbols-null-crash-regression.mjs
// Exit 0 = pass, 1 = fail.
import path from 'node:path';
import fs from 'node:fs';

const scriptDir = path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
const root = path.resolve(scriptDir, '..', '..');
const componentPath = path.join(root, 'web', 'src', 'components', 'CodeAtlas.tsx');
const source = fs.readFileSync(componentPath, 'utf8');

// --- Setup: slice the production symbol-count expression verbatim ----------
const anchor = 'selectedItem.file.symbols';
const occurrences = source.split(anchor).length - 1;
if (occurrences !== 1) {
  console.log(`FAIL: setup — expected exactly 1 selectedItem.file.symbols anchor in CodeAtlas.tsx, found ${occurrences}`);
  process.exit(1);
}
const anchorIdx = source.indexOf(anchor);
const openBrace = source.lastIndexOf('{', anchorIdx);
if (openBrace < 0) {
  console.log('FAIL: setup — no opening JSX brace found before the anchor');
  process.exit(1);
}
const endMarker = '} nodes';
const end = source.indexOf(endMarker, anchorIdx);
if (end < 0) {
  console.log('FAIL: setup — expression terminator "} nodes" not found after the anchor');
  process.exit(1);
}
const expr = source.slice(openBrace + 1, end); // between the JSX braces
let fn;
try {
  fn = new Function('selectedItem', `return (${expr});`);
} catch (err) {
  console.log('FAIL: setup — sliced expression does not compile: ' + err.message);
  process.exit(1);
}

// --- Fixtures ---------------------------------------------------------------
// include_symbols=false wire shape — exactly what the server emits (files
// keep every other field but carry "symbols": null; proven by the round-96
// Go wire probe marshaling core.AtlasFile after the handler's
// Symbols = nil transform).
const symbolsNullSelection = {
  type: 'file',
  file: {
    path: 'internal/ast/parser.go',
    name: 'parser.go',
    language: 'go',
    health_score: 90,
    is_fragile: false,
    recent_thrashing_count: 0,
    total_loc: 20,
    symbols: null,
  },
};

// Full-mode control: symbols present — must count 1 both pre- and post-fix
// (proves the guards do not regress the normal path).
const symbolsFullSelection = {
  type: 'file',
  file: {
    ...symbolsNullSelection.file,
    symbols: [
      {
        node_signature: 'function:parser.go::Parse',
        name: 'Parse',
        kind: 'function',
        start_line: 10,
        end_line: 20,
        lines_of_code: 11,
        status: 'ACTIVE',
        edit_count: 0,
        last_action: '',
        last_model: '',
        last_event_time: '0001-01-01T00:00:00Z',
        ast_content_hash: 'h',
      },
    ],
  },
};

const fails = [];
const check = (ok, msg) => {
  if (!ok) {
    fails.push(msg);
    console.log('FAIL: ' + msg);
  }
};

// (a) THE contract: a symbols-null payload must yield a number, not throw.
let nullCount;
try {
  nullCount = fn(symbolsNullSelection);
  check(nullCount === 0, `symbols-null payload must evaluate to 0, got ${String(nullCount)}`);
} catch (err) {
  check(false, `symbols-null payload crashes the production expression: ${err.constructor.name}: ${err.message}`);
}

// (b) Control: a populated-symbols payload counts 1 symbol.
let fullCount;
try {
  fullCount = fn(symbolsFullSelection);
  check(fullCount === 1, `full-mode control must count 1 symbol, got ${String(fullCount)}`);
} catch (err) {
  check(false, `full-mode control crashed: ${err.constructor.name}: ${err.message}`);
}

if (fails.length > 0) {
  console.log(`FAIL: ${fails.length} check(s) failed — atlas include_symbols=null contract`);
  process.exit(1);
}
console.log(
  'PASS: CodeAtlas symbol-count expression survives the include_symbols=false wire shape (symbols null) and still counts populated payloads',
);
