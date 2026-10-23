#!/usr/bin/env bash
# Runs the API and the webapp dev server side by side with prefixed logs.
# Used by `make dev`; the docker services and the seed must already be up.
# Ctrl+C (or either process exiting) stops both.
#
# Works on Linux, macOS (bash 3.2) and Windows Git Bash.
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MAKE_CMD="${MAKE:-make}"
API_BASE_URL="${API_BASE_URL:-http://localhost:8080}"

# Job control puts each background job in its own process group, so the
# whole tree (make, go run, the api binary; make, npm, nuxt) can be stopped.
set -m

prefix() {
  awk -v p="$1" '{ print p " " $0; fflush() }'
}

"$MAKE_CMD" --no-print-directory -C "$ROOT/API" run > >(prefix "[api]") 2>&1 &
API_PID=$!

API_BASE_URL="$API_BASE_URL" "$MAKE_CMD" --no-print-directory -C "$ROOT/Webapp" dev > >(prefix "[web]") 2>&1 &
WEB_PID=$!

stop_tree() {
  local pid=$1
  kill -0 "$pid" 2>/dev/null || return 0
  if [ -r "/proc/$pid/winpid" ] && command -v taskkill >/dev/null 2>&1; then
    # Git Bash: native Windows children (api.exe, node.exe) ignore MSYS
    # signals, so kill the Windows process tree.
    taskkill //F //T //PID "$(cat "/proc/$pid/winpid")" >/dev/null 2>&1
  fi
  kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null
}

STOPPING=0
cleanup() {
  [ "$STOPPING" = 1 ] && return
  STOPPING=1
  echo "[dev] stopping api and webapp"
  stop_tree "$API_PID"
  stop_tree "$WEB_PID"
  wait 2>/dev/null
  echo "[dev] stopped (docker services keep running; use 'make down')"
}
# Ctrl+C is the normal way to stop, so it exits 0.
trap 'cleanup; exit 0' INT
trap 'cleanup; exit 143' TERM

echo "[dev] api    -> http://localhost:8080 (healthz, readyz)"
echo "[dev] webapp -> http://localhost:3000 (API_BASE_URL=$API_BASE_URL)"
echo "[dev] press Ctrl+C to stop both"

# bash 3.2 has no `wait -n`: poll until one of the two exits.
while kill -0 "$API_PID" 2>/dev/null && kill -0 "$WEB_PID" 2>/dev/null; do
  sleep 1
done

kill -0 "$API_PID" 2>/dev/null || echo "[dev] api exited"
kill -0 "$WEB_PID" 2>/dev/null || echo "[dev] webapp exited"
cleanup
exit 1
