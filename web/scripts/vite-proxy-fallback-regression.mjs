// Regression test (round 35): web/vite.config.ts must proxy the daemon API to
// the port the daemon ACTUALLY listens on by default.
//
// Contract: the standalone `npm run dev` flow (daemon started with default
// flags, no dev.sh/dev.ps1 launcher, no env vars) proxies /api, /proxy and
// /v1 to the daemon's default port 3444 (cmd/wrongtrace/main.go:109
// `IntP("port", "p", 3444, ...)`); an explicit WRONGTRACE_DAEMON_PORT (exported
// by dev.sh/dev.ps1) still wins, keeping the launcher flows coherent.
// The fallback was '3445' — only correct while a launcher shifted the daemon —
// which pointed the whole standalone dev proxy at a dead port.
//
// Run: node web/scripts/vite-proxy-fallback-regression.mjs
// Exit 0 = pass, 1 = fail.
// Uses the repo's own vite (web/node_modules) to transpile the REAL config;
// defineConfig is identity, so the evaluated default export IS the config.
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';

const scriptDir = path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
const root = path.resolve(scriptDir, '..', '..');

const webRequire = createRequire(pathToFileURL(path.join(root, 'web', 'package.json')));
const vite = webRequire('vite');

const configSource = fs.readFileSync(path.join(root, 'web', 'vite.config.ts'), 'utf8');
const transformed = await vite.transformWithOxc(configSource, 'vite.config.ts');
if (transformed.errors && transformed.errors.length > 0) {
  console.log('FAIL: transform errors: ' + JSON.stringify(transformed.errors));
  process.exit(1);
}

const bodySansImports = (() => {
  let body = transformed.code
    // named import: import { defineConfig } from 'vite';
    .replace(/^import\s*\{([^}]*)\}\s*from\s*['"]([^'"]+)['"];?\s*$/gm,
      (_, names, spec) => `const {${names.trim()}} = __mods[${JSON.stringify(spec)}];`)
    // default import: import react from '@vitejs/plugin-react';
    .replace(/^import\s+([A-Za-z_$][\w$]*)\s+from\s*['"]([^'"]+)['"];?\s*$/gm,
      (_, name, spec) => `const ${name} = __mods[${JSON.stringify(spec)}];`)
    // capture the default export
    .replace(/^export\s+default\s+/m, '__exports.config = ');
  return body;
})();

const mods = {
  vite: { defineConfig: (x) => x },
  '@vitejs/plugin-react': () => ({}),
};

function evaluate() {
  const fn = new Function('__mods', '__exports', 'process', bodySansImports);
  const exportsRef = {};
  fn(mods, exportsRef, process);
  return exportsRef.config;
}

const DAEMON_DEFAULT = 'http://127.0.0.1:3444'; // cmd/wrongtrace/main.go:109 flag default
const fails = [];
function fail(msg) {
  fails.push(msg);
  console.log('FAIL: ' + msg);
}

// --- Scenario A: standalone `npm run dev` (no launcher env vars) ----------
delete process.env.WRONGTRACE_DAEMON_PORT;
delete process.env.WRONGTRACE_PORT;
delete process.env.VITE_PORT;
const standalone = evaluate();

for (const entry of ['/api', '/proxy', '/v1']) {
  const target = standalone.server?.proxy?.[entry]?.target;
  if (target !== DAEMON_DEFAULT) {
    fail(
      `standalone dev proxy '${entry}' targets ${JSON.stringify(target ?? null)}, but the daemon's default port is 3444 (cmd/wrongtrace/main.go:109) — the dashboard API/WS/proxy calls hit a dead port`,
    );
  }
}
if (standalone.server?.port !== 3444) {
  fail(`control: standalone vite port is ${JSON.stringify(standalone.server?.port ?? null)}, want 3444`);
}

// --- Scenario B (control): launcher flows keep their explicit env ----------
process.env.WRONGTRACE_DAEMON_PORT = '3999';
const launcher = evaluate();
delete process.env.WRONGTRACE_DAEMON_PORT;
const launcherTarget = launcher.server?.proxy?.['/api']?.target;
if (launcherTarget !== 'http://127.0.0.1:3999') {
  fail(`control: launcher flow (WRONGTRACE_DAEMON_PORT=3999) proxy target is ${JSON.stringify(launcherTarget ?? null)}, want http://127.0.0.1:3999 — dev.sh/dev.ps1 coherence broken`);
}

if (fails.length > 0) {
  console.log(`FAIL: ${fails.length} check(s) failed — standalone vite proxy fallback does not match the daemon default`);
  process.exit(1);
}
console.log('PASS: standalone `npm run dev` proxies /api, /proxy and /v1 to the daemon default 127.0.0.1:3444; explicit WRONGTRACE_DAEMON_PORT still wins for dev.sh/dev.ps1');
