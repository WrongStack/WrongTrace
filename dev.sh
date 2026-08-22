#!/usr/bin/env sh
# WrongTrace dev runner (POSIX): daemon + Vite HMR in one command.
#
# Usage:  ./dev.sh [port] [watch-dir]
#         port defaults to 4318, watch-dir to the repo root.
#
# What it does:
#   1. builds the daemon (fast incremental when nothing changed) — avoids
#      `go run` leaving orphaned children on interrupt
#   2. installs web/node_modules on first use
#   3. starts the daemon (watching the repo itself) and the Vite dev server
#      (HMR at :5173, proxying /api + /api/ws to the daemon)
#   4. on Ctrl+C tears BOTH processes down and cleans up
#
# Windows: use dev.ps1 (PowerShell 7). This script is for WSL / macOS / Linux.

set -eu

PORT="${1:-4318}"
WATCH_DIR="${2:-"$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"}"
ROOT="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
BIN="$ROOT/bin/wrongtrace"
VITE_URL="http://localhost:5173"

command -v go >/dev/null 2>&1 || { echo "dev.sh: go not found in PATH" >&2; exit 1; }
command -v npm >/dev/null 2>&1 || { echo "dev.sh: npm not found in PATH" >&2; exit 1; }

echo "==> building daemon"
mkdir -p "$ROOT/bin"
(cd "$ROOT" && go build -o "$BIN" ./cmd/wrongtrace)

if [ ! -d "$ROOT/web/node_modules" ]; then
  echo "==> installing dashboard dependencies (first run)"
  (cd "$ROOT/web" && npm install)
fi

cleanup() {
  # Kill the whole process group so vite's children die too.
  if [ -n "${VITE_PID:-}" ] || [ -n "${DAEMON_PID:-}" ]; then
    echo ""
    echo "==> stopping dev processes"
    [ -n "${VITE_PID:-}" ] && kill "$VITE_PID" 2>/dev/null || true
    [ -n "${DAEMON_PID:-}" ] && kill "$DAEMON_PID" 2>/dev/null || true
    wait 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

echo "==> starting daemon on :$PORT (multi-project workspace hub)"
"$BIN" start \
  --port "$PORT" \
  --watch "$WATCH_DIR" \
  --repo "$(basename "$WATCH_DIR")" &
DAEMON_PID=$!

# Wait for the daemon's health endpoint before starting vite so the proxy
# target is guaranteed live (avoids a confusing first-load proxy error).
i=0
until curl -fsS "http://localhost:$PORT/api/health" >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -ge 50 ]; then
    echo "dev.sh: daemon did not become healthy within 10s" >&2
    exit 1
  fi
  sleep 0.2
done
echo "    daemon healthy: http://localhost:$PORT/api/health"

echo "==> starting vite dev server"
(cd "$ROOT/web" && WRONGTRACE_PORT="$PORT" npm run dev) &
VITE_PID=$!

echo ""
echo "WrongTrace dev is up:"
echo "  dashboard (HMR): $VITE_URL"
echo "  daemon API:      http://localhost:$PORT"
echo "  press Ctrl+C to stop both"
echo ""

wait
