#!/bin/sh
# Stop the UI demo and remove its isolated database and runtime state.
# Never touches the main dev environment (3000/5173, sumi_dev).
set -eu

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

DEMO_DB="${SUMI_UI_DEMO_DB:-sumi_ui_demo}"
DEMO_SERVER_PORT="${SUMI_UI_DEMO_SERVER_PORT:-3001}"
DEMO_WEB_PORT="${SUMI_UI_DEMO_WEB_PORT:-5174}"
RUNTIME="scripts/ui-demo/.runtime"
COMPUTER_ROOT="${SUMI_UI_DEMO_COMPUTER_ROOT:-$HOME/.sumi-ui-demo/computer}"

# Stop only processes bound to the UI demo ports.
for port in "$DEMO_SERVER_PORT" "$DEMO_WEB_PORT"; do
  pids="$(lsof -ti "tcp:$port" 2>/dev/null || true)"
  if [ -n "$pids" ]; then
    for pid in $pids; do
      kill "$pid" 2>/dev/null || true
    done
  fi
done

if command -v brew >/dev/null 2>&1; then
  pg_bin="$(brew --prefix postgresql@17)/bin"
  "$pg_bin/dropdb" --if-exists --force "$DEMO_DB" || true
fi

if [ -d "$RUNTIME" ]; then
  target="$(cd "$RUNTIME" && pwd)"
  expected="$REPO_ROOT/scripts/ui-demo/.runtime"
  case "$target" in
    "$expected"|"$expected"/*) ;;
    *)
      echo "refusing to remove unexpected runtime path: $target" >&2
      exit 1
      ;;
  esac
rm -rf "$target"
fi

if [ -d "$COMPUTER_ROOT" ]; then
  target="$(cd "$COMPUTER_ROOT" && pwd)"
  expected="$HOME/.sumi-ui-demo/computer"
  case "$target" in
    "$expected"|"$expected"/*) ;;
    *)
      echo "refusing to remove unexpected computer root: $target" >&2
      exit 1
      ;;
  esac
  rm -rf "$target"
fi

echo "UI demo cleaned (db: $DEMO_DB, runtime: $RUNTIME, computer: $COMPUTER_ROOT)"
