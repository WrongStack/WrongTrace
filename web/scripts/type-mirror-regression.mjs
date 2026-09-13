// type-mirror-regression.mjs — pins web/src/types/index.ts to the Go wire contracts.
//
// types/index.ts opens with "Shared TypeScript types mirroring the Go API
// contracts", but nothing enforced the mirroring. Four interfaces had drifted
// from the fields the daemon actually serializes:
//
//   - ThrashingRow: the wire field is node_signature (db.ThrashingRow json
//     tag), the mirror said `signature`, and ThrashingHeatmap.tsx followed the
//     mirror — rendering `undefined` in the heatmap's symbol column (blank
//     cell, "undefined" tooltip). tsc could not catch it because the type and
//     the accessor were wrong together.
//   - WsCodeEvent: was PascalCase while ast.Event (the code_event WS payload)
//     marshals snake_case tags; every sibling WS payload type is snake_case.
//   - AppSettings: carried five fields the daemon never sends
//     (auto_vacuum_enabled, retention_days, enable_webhook_alerts, webhook_url,
//     webhook_type) and omitted socket_path/version that it does send.
//   - EventRecord: omitted the optional `timestamp` field (timestamp,omitempty).
//
// This guard mechanically extracts each Go struct's json tags and the matching
// TS interface's field names and requires the name sets to agree.
//
// Contract: read-only; no deps; runs from the repo root (run-all.mjs cwd);
// fails its own SETUP if a Go struct or TS interface anchor is missing, so a
// renamed anchor breaks this guard loudly instead of passing vacuously. The
// two CONTROL pairs were verified to agree and must pass before and after any
// fix; a control failure means the extraction or a real wire change, not this
// guard's drift list.

import { readFileSync } from 'node:fs';

const PAIRS = [
  // Drifts this guard was written to pin:
  { goFile: 'internal/db/queries.go', go: 'ThrashingRow', ts: 'ThrashingRow' },
  { goFile: 'internal/ast/diff.go', go: 'Event', ts: 'WsCodeEvent' },
  { goFile: 'internal/core/settings.go', go: 'AppSettings', ts: 'AppSettings' },
  { goFile: 'internal/db/queries.go', go: 'EventRecord', ts: 'EventRecord' },
  // Controls (verified agreements):
  { goFile: 'internal/db/queries.go', go: 'ModelRow', ts: 'ModelRow' },
  { goFile: 'internal/core/metrics.go', go: 'MetricsSnapshot', ts: 'MetricsSnapshot' },
];

const goStructTags = (src, name) => {
  const m = src.match(new RegExp(`type ${name} struct \\{([\\s\\S]*?)\\n\\}`));
  if (!m) return null;
  return [...m[1].matchAll(/`json:"([^"]+)"/g)].map((t) => t[1].split(',')[0]);
};

const tsInterfaceFields = (src, name) => {
  const m = src.match(new RegExp(`export interface ${name} \\{([\\s\\S]*?)\\n\\}`));
  if (!m) return null;
  return [...m[1].matchAll(/^\s{2}([A-Za-z_][A-Za-z0-9_]*)\s*[?!]?:/gm)].map((f) => f[1]);
};

const goFiles = new Map();
for (const f of new Set(PAIRS.map((p) => p.goFile))) goFiles.set(f, readFileSync(f, 'utf8'));
const tsSrc = readFileSync('web/src/types/index.ts', 'utf8');

const pairs = [];
const setup = [];
for (const p of PAIRS) {
  const g = goStructTags(goFiles.get(p.goFile), p.go);
  const t = tsInterfaceFields(tsSrc, p.ts);
  if (!g || g.length === 0 || !t || t.length === 0) {
    setup.push(`${p.goFile}#${p.go}(${g ? g.length : 'missing'}) <-> types/index.ts#${p.ts}(${t ? t.length : 'missing'})`);
    continue;
  }
  pairs.push({ ...p, g, t });
}
if (setup.length > 0) {
  console.error(`FAIL: SETUP anchors missing or empty: ${setup.join('; ')}`);
  process.exit(1);
}

const drifts = [];
for (const p of pairs) {
  const gs = new Set(p.g);
  const tsSet = new Set(p.t);
  const wireOnly = p.g.filter((x) => !tsSet.has(x));
  const mirrorOnly = p.t.filter((x) => !gs.has(x));
  if (wireOnly.length > 0 || mirrorOnly.length > 0) {
    drifts.push(`${p.goFile}#${p.go} <-> types/index.ts#${p.ts}  wire-only=[${wireOnly.join(', ')}] mirror-only=[${mirrorOnly.join(', ')}]`);
  }
}

if (drifts.length > 0) {
  console.error(`FAIL: ${drifts.length} mirror drift(s) between the Go wire contracts and web/src/types/index.ts:`);
  for (const d of drifts) console.error(`  - ${d}`);
  process.exit(1);
}
console.log(`PASS: ${pairs.length} Go<->TS interface mirrors agree (ThrashingRow, WsCodeEvent, AppSettings, EventRecord + 2 controls)`);
