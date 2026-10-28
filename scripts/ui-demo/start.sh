#!/bin/sh
# One-command UI demo: PostgreSQL, Sumi server, Vite, dev-seed and the
# sample set, all isolated from the main dev environment (3001/5174,
# sumi_ui_demo database, scripts/ui-demo/.runtime state). Run via `mise run demo`.
set -eu

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

DEMO_DB="${SUMI_UI_DEMO_DB:-sumi_ui_demo}"
DEMO_SERVER_PORT="${SUMI_UI_DEMO_SERVER_PORT:-3001}"
DEMO_WEB_PORT="${SUMI_UI_DEMO_WEB_PORT:-5174}"
RUNTIME="scripts/ui-demo/.runtime"
# Computer state needs a short path: the macOS Unix socket is limited to 103
# bytes, which the deep worktree path exceeds.
COMPUTER_ROOT="${SUMI_UI_DEMO_COMPUTER_ROOT:-$HOME/.sumi-ui-demo/computer}"
LOG_DIR="$RUNTIME/logs"
mkdir -p "$LOG_DIR"

if ! command -v brew >/dev/null 2>&1; then
  echo "demo needs Homebrew PostgreSQL; start PostgreSQL using your system service manager" >&2
  exit 1
fi
brew services start postgresql@17 >/dev/null 2>&1 || true
pg_bin="$(brew --prefix postgresql@17)/bin"
"$pg_bin/psql" --dbname postgres --tuples-only --no-align \
  --command "SELECT 1 FROM pg_database WHERE datname = '$DEMO_DB'" |
  grep -qx 1 || "$pg_bin/createdb" "$DEMO_DB"

if curl -sf "http://127.0.0.1:$DEMO_SERVER_PORT/api/v1/health" >/dev/null 2>&1; then
  echo "UI demo server is already running on :$DEMO_SERVER_PORT (stop it first for a clean restart)" >&2
  exit 1
fi
if curl -sf "http://127.0.0.1:$DEMO_WEB_PORT/" >/dev/null 2>&1; then
  echo "UI demo web is already running on :$DEMO_WEB_PORT (stop it first for a clean restart)" >&2
  exit 1
fi

export SUMI_SERVER__BIND="127.0.0.1:$DEMO_SERVER_PORT"
export SUMI_SERVER__DATABASE_URL="postgres://localhost/$DEMO_DB"
echo "building Sumi server (first run compiles)..."
cargo build >"$LOG_DIR/build.log" 2>&1 || {
  echo "build failed; see $LOG_DIR/build.log" >&2
  exit 1
}
target/debug/sumi server >"$LOG_DIR/server.log" 2>&1 &
SERVER_PID=$!

SEED_PID=""
WEB_PID=""
trap 'kill $SERVER_PID $SEED_PID $WEB_PID 2>/dev/null || true' EXIT INT TERM

echo "waiting for server on :$DEMO_SERVER_PORT ..."
for _ in $(seq 1 120); do
  if curl -sf "http://127.0.0.1:$DEMO_SERVER_PORT/api/v1/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done
curl -sf "http://127.0.0.1:$DEMO_SERVER_PORT/api/v1/health" >/dev/null ||
  { echo "server did not become healthy; see $LOG_DIR/server.log" >&2; exit 1; }

export SUMI_SEED_SERVER="http://127.0.0.1:$DEMO_SERVER_PORT"
export SUMI_SEED_COMPUTER_ROOT="$COMPUTER_ROOT"
node scripts/dev-seed.mjs >"$LOG_DIR/dev-seed.log" 2>&1 &
SEED_PID=$!

SUMI_VITE_PORT="$DEMO_WEB_PORT" \
SUMI_VITE_PROXY_TARGET="http://127.0.0.1:$DEMO_SERVER_PORT" \
pnpm --dir web dev --port "$DEMO_WEB_PORT" --strictPort >"$LOG_DIR/web.log" 2>&1 &
WEB_PID=$!

echo "waiting for dev-seed (Computer pairing + agents) ..."
for _ in $(seq 1 300); do
  if grep -q "READY" "$LOG_DIR/dev-seed.log" 2>/dev/null; then
    break
  fi
  if ! kill -0 "$SEED_PID" 2>/dev/null; then
    echo "dev-seed exited early; see $LOG_DIR/dev-seed.log" >&2
    exit 1
  fi
  sleep 1
done
if ! grep -q "READY" "$LOG_DIR/dev-seed.log" 2>/dev/null; then
  echo "dev-seed did not finish; see $LOG_DIR/dev-seed.log" >&2
  exit 1
fi

node scripts/ui-demo/seed-samples.mjs

echo "waiting for web on :$DEMO_WEB_PORT ..."
for _ in $(seq 1 60); do
  if curl -sf "http://127.0.0.1:$DEMO_WEB_PORT/" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done

echo ""
echo "UI demo ready:"
echo "  web      http://127.0.0.1:$DEMO_WEB_PORT"
echo "  server   http://127.0.0.1:$DEMO_SERVER_PORT"
echo "  login    dev@example.test / correct horse battery staple"
echo "  samples  #general, #review, #empty-lab; Tasks, Inbox, Members"
echo "  shots    mise run demo:shots"
echo ""
echo "press Ctrl-C to stop (server + web + seed daemon)"

wait
