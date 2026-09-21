// Regression test: useProxyTraffic must not send a project_id parameter.
//
// Defect (round-99 observation): the hook accepted a projectId argument and
// built `?project_id=...`, but the only caller (ProxyRoutingView) invoked it
// with no argument AND the ListProxyTraffic handler reads only limit/detail —
// so the parameter was dead capability: silently ignored by the server and
// never exercised by any call site.
//
// Contract: the hook takes NO parameters, requests
// /api/proxy/traffic?detail=false&limit=100 under the bare ['proxy_traffic']
// query key (which Dashboard's invalidate(['proxy_traffic']) calls match —
// React Query invalidation is prefix-based), and ProxyRoutingView keeps
// invoking it with no argument. Project scoping of traffic stays client-side
// via ProxyRoutingView's projectScope state.
//
// Run: node web/scripts/proxy-traffic-no-project-param-regression.mjs
// Exit 0 = pass, 1 = fail.
import fs from 'node:fs';
import path from 'node:path';

const scriptDir = path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
const root = path.resolve(scriptDir, '..', '..');

const read = (p) => fs.readFileSync(path.join(root, 'web', 'src', ...p.split('/')), 'utf8');
const hooks = read('hooks/useMetrics.ts');
const dashboard = read('pages/Dashboard.tsx');
const routingView = read('components/ProxyRoutingView.tsx');

// Extract just the useProxyTraffic hook (from its export to the next export —
// useProxyTrafficDetail follows it and shares the name prefix).
const hookStart = hooks.indexOf('export function useProxyTraffic');
const hookEnd = hooks.indexOf('\nexport function ', hookStart + 1);
const hook = hooks.slice(hookStart, hookEnd === -1 ? undefined : hookEnd);

const failures = [];
const check = (cond, msg) => { if (!cond) failures.push(msg); };

check(hook.includes('export function useProxyTraffic()'),
  'useProxyTraffic must take NO parameters (the projectId parameter was dead capability)');
check(!hook.includes('project_id'),
  'useProxyTraffic must not reference project_id — ListProxyTraffic ignores it');
check(hook.includes('/proxy/traffic?detail=false&limit=100'),
  'useProxyTraffic must still request /proxy/traffic with detail=false&limit=100');
check(hook.includes("queryKey: ['proxy_traffic']"),
  "useProxyTraffic must use the bare ['proxy_traffic'] query key");

// The invalidation contract must survive the key simplification.
check(dashboard.includes("invalidate(['proxy_traffic'])"),
  "Dashboard must keep invalidating ['proxy_traffic'] (prefix-matches the hook's key)");

// The only caller must use the parameter-less form.
check(routingView.includes('useProxyTraffic()'),
  'ProxyRoutingView must call useProxyTraffic() with no argument');
check(!/useProxyTraffic\([^)]/.test(routingView),
  'ProxyRoutingView must not pass any argument to useProxyTraffic');

if (failures.length > 0) {
  for (const f of failures) console.log('  - ' + f);
  console.log(`FAIL: proxy-traffic-no-project-param (${failures.length} assertion(s))`);
  process.exit(1);
}
console.log('PASS: useProxyTraffic requests /proxy/traffic without a project_id parameter and keeps the Dashboard invalidation contract');
