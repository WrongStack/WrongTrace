// Run the real incremental query with deterministic server pages. Same-second
// IDs must use the server's descending tie-breaker before the page is capped.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const webRequire = createRequire(path.join(root, 'web/package.json'));
const { transformWithOxc } = webRequire('vite');
const source = fs.readFileSync(path.join(root, 'web/src/hooks/useMetrics.ts'), 'utf8');
const { code } = await transformWithOxc(source, 'useMetrics.ts');
const body = code.replace(/^import .*?;\s*$/gm, '').replace(/^export\s+/gm, '');
let previous;
let fresh;
let requestURL;
const hooks = new Function('useQuery', 'useQueryClient', 'fetch', `${body}\nreturn { useRecentEvents };`)(
  (options) => options,
  () => ({ getQueryData: () => previous }),
  async (url) => { requestURL = url; return { ok: true, json: async () => fresh }; },
);
const row = (id, time = '2026-10-29T12:00:00Z') => ({ event_id: id, event_time: time });
async function run(oldRows, newRows, limit = 2) {
  previous = oldRows;
  fresh = newRows;
  return (await hooks.useRecentEvents('project-a', limit).queryFn({ signal: undefined })).map((r) => r.event_id);
}

assert.deepEqual(await run([row('b', '2026-10-29T11:59:59Z')], [row('c')]), ['c', 'b']);
console.log('CONTROL: different-second rows remain newest first');
const actual = await run([row('b'), row('a')], [row('c'), row('b')]);
console.log(`EXPECTED: [c,b] | ACTUAL: ${JSON.stringify(actual)}`);
assert.deepEqual(actual, ['c', 'b'], 'new same-second event must survive the page limit');
assert.match(requestURL, /project_id=project-a.*since=/);
assert.deepEqual(await run([row('b'), row('a')], [row('c'), row('b')]), ['c', 'b']);
assert.deepEqual(await run([row('c'), row('b')], [row('c'), row('b')]), ['c', 'b']);
assert.deepEqual(await run([], [row('c'), row('b')]), ['c', 'b']);
assert.deepEqual(await run([row('b')], []), ['b']);
assert.deepEqual(await run([row('b'), row('a')], [row('c')], 1), ['c']);
console.log('PASS: same-second incremental events retain the newest IDs without duplicates');
