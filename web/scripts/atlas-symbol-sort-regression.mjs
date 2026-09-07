// Regression test (round 36): the CodeAtlas canvas symbol comparator
// (web/src/components/CodeAtlas.tsx, the [...file.symbols].sort site) must be
// a valid weak ordering.
//
// Defect (pre-fix): the comparator returned 1 for BOTH directions of any pair
// of distinct non-MODIFIED statuses (ADDED/DELETED, ADDED/ACTIVE, ...):
//   if (a.status !== b.status) return a.status === 'MODIFIED' ? -1 : 1;
// which breaks antisymmetry (cmp(a,b) === cmp(b,a) === 1), makes the
// edit_count / lines_of_code tiebreaks unreachable for those pairs, and lets
// the MAX_CANVAS_SYMBOLS=14 selection depend on backend insertion order.
//
// Contract:
//   - antisymmetry: cmp(a,b) and cmp(b,a) must never both order a after b,
//   - edit_count desc decides across statuses (not just within one status),
//   - the full sort is input-permutation independent (distinct keys),
//   - control: MODIFIED still outranks every other status.
//
// The comparator body is SLICED VERBATIM from the production file at runtime,
// so this harness always tests the code that actually ships.
//
// Run: node web/scripts/atlas-symbol-sort-regression.mjs
// Exit 0 = pass, 1 = fail.
import { pathToFileURL } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';

const scriptDir = path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
const root = path.resolve(scriptDir, '..', '..');
const componentPath = path.join(root, 'web', 'src', 'components', 'CodeAtlas.tsx');
const source = fs.readFileSync(componentPath, 'utf8');

// --- Setup: slice the production comparator body verbatim -------------------
const anchor = 'const sortedSymbols = [...file.symbols].sort((a, b) => {';
const occurrences = source.split(anchor).length - 1;
if (occurrences !== 1) {
  console.log(`FAIL: setup — expected exactly 1 symbol-sort anchor in CodeAtlas.tsx, found ${occurrences}`);
  process.exit(1);
}
const bodyStart = source.indexOf(anchor) + anchor.length;
const bodyEnd = source.indexOf('});', bodyStart);
if (bodyEnd < 0) {
  console.log('FAIL: setup — comparator closing "});" not found after the anchor');
  process.exit(1);
}
const body = source.slice(bodyStart, bodyEnd);
let cmp;
try {
  cmp = new Function('a', 'b', body);
} catch (err) {
  console.log('FAIL: setup — sliced comparator body does not compile: ' + err.message);
  process.exit(1);
}

function sym(marker, status, editCount, loc) {
  return {
    node_signature: `function:atlas.ts::${marker}`,
    name: marker,
    kind: 'function',
    status,
    edit_count: editCount,
    lines_of_code: loc,
  };
}

const fails = [];
const check = (ok, msg) => {
  if (!ok) {
    fails.push(msg);
    console.log('FAIL: ' + msg);
  }
};

// (a) Antisymmetry probe: for a distinct-status non-MODIFIED pair, the
// comparator must not order the second argument first in BOTH directions.
const A = sym('added9', 'ADDED', 9, 40);
const D = sym('deleted1', 'DELETED', 1, 10);
check(
  !(cmp(A, D) > 0 && cmp(D, A) > 0),
  `antisymmetry violated: cmp(ADDED,DELETED)=${cmp(A, D)} and cmp(DELETED,ADDED)=${cmp(D, A)} both order the second argument first`,
);

// (b) Tiebreak reachability: with distinct edit_counts, edit_count desc must
// decide across statuses — ADDED(e=9) belongs before DELETED(e=1).
check(
  cmp(A, D) < 0,
  `edit_count desc must order ADDED(e=9) before DELETED(e=1) across statuses; got ${cmp(A, D)}`,
);

// (c) End-to-end determinism: an equal-edit_count ADDED/DELETED pair must
// sort to the same order from either input permutation (the lines_of_code
// desc tiebreak becomes reachable once antisymmetry holds).
const A5 = sym('added5', 'ADDED', 5, 50);
const D5 = sym('deleted5', 'DELETED', 5, 10);
const pairFromFirst = [...[A5, D5]].sort(cmp);
const pairFromSecond = [...[D5, A5]].sort(cmp);
check(
  pairFromFirst[0] === A5 && pairFromSecond[0] === A5,
  'equal-count ADDED/DELETED pair is input-order dependent: the lines_of_code tiebreak is never applied',
);

// (d) Control (must hold pre-fix too): MODIFIED outranks every other status
// regardless of edit_count.
const M = sym('modified0', 'MODIFIED', 0, 1);
check(cmp(D, M) > 0, 'control: MODIFIED must outrank a non-MODIFIED symbol regardless of edit_count');

// (e) Full-sort determinism: 16 distinct-key symbols across all statuses,
// sorted from two opposite input permutations, must produce identical output
// sequences (this is what selects the MAX_CANVAS_SYMBOLS=14 canvas set).
const sixteen = [];
const statuses = ['MODIFIED', 'ADDED', 'DELETED'];
let ec = 16;
for (let i = 0; i < 16; i++) {
  sixteen.push(sym(`s${i}`, statuses[i % 3], ec, 10 + i));
  ec--;
}
const perm1 = [...sixteen].sort(cmp).map((s) => s.name);
const perm2 = [...sixteen].reverse().sort(cmp).map((s) => s.name);
check(
  JSON.stringify(perm1) === JSON.stringify(perm2),
  'full-sort output differs between input permutations (the comparator is not a valid weak ordering)',
);

if (fails.length > 0) {
  console.log(`FAIL: ${fails.length} check(s) failed — atlas symbol sort comparator`);
  process.exit(1);
}
console.log(
  'PASS: CodeAtlas symbol comparator is a valid weak ordering (MODIFIED first, edit_count desc, lines_of_code desc; input-order independent)',
);
