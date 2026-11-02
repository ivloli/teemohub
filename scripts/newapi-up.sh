#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ ! -f .env ]]; then
  echo "missing .env (copy .env.example first)" >&2
  exit 1
fi
set -a
# shellcheck disable=SC1091
. ./.env
set +a

PORT="${NEWAPI_PORT:-3000}"
DB="${NEWAPI_SQLITE_PATH:-./runtime/newapi/newapi.db}"
BIN="$ROOT/runtime/bin/new-api"
LOG="$ROOT/runtime/logs/newapi.log"
PID_FILE="$ROOT/runtime/newapi.pid"

mkdir -p "$(dirname "$DB")" "$(dirname "$LOG")"

if [[ ! -x "$BIN" ]]; then
  echo "New API binary missing: $BIN" >&2
  echo "build it with: make newapi-build" >&2
  exit 1
fi

if curl -fsS -m 2 "http://127.0.0.1:$PORT/api/status" >/dev/null 2>&1; then
  echo "New API already listening on :$PORT"
else
  echo "starting New API on 127.0.0.1:$PORT"
  PORT="$PORT" SQLITE_PATH="$DB" \
    SESSION_SECRET="${TEEMOHUB_SESSION_SECRET:-$(openssl rand -hex 32)}" \
    GIN_MODE=release \
    nohup "$BIN" >>"$LOG" 2>&1 &
  echo $! >"$PID_FILE"
  for _ in $(seq 1 60); do
    if curl -fsS -m 2 "http://127.0.0.1:$PORT/api/status" >/dev/null 2>&1; then
      break
    fi
    sleep 0.5
  done
  curl -fsS -m 2 "http://127.0.0.1:$PORT/api/status" >/dev/null || {
    echo "New API did not become ready; see $LOG" >&2
    exit 1
  }
fi

SETUP="$(curl -fsS -m 5 "http://127.0.0.1:$PORT/api/setup")"
INITIALIZED="$(python3 -c 'import json,sys;print(str(json.load(sys.stdin).get("data",{}).get("status",False)).lower())' <<<"$SETUP")"
if [[ "$INITIALIZED" != "true" ]]; then
  echo "initializing New API root user"
  curl -fsS -m 10 -X POST "http://127.0.0.1:$PORT/api/setup" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"${NEWAPI_ROOT_USER:-root}\",\"password\":\"${NEWAPI_ROOT_PASSWORD}\",\"confirmPassword\":\"${NEWAPI_ROOT_PASSWORD}\",\"SelfUseModeEnabled\":false,\"DemoSiteEnabled\":false}" >/dev/null
fi

echo "New API ready: http://127.0.0.1:$PORT"
