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

BASE="http://127.0.0.1:${NEWAPI_PORT:-3000}"
ACCESS="$(curl -fsS -m 10 -X POST "$BASE/api/user/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"${NEWAPI_ROOT_USER:-root}\",\"password\":\"${NEWAPI_ROOT_PASSWORD}\"}" \
  | python3 -c 'import json,sys;print(json.load(sys.stdin)["data"]["access_token"])')"

auth=(-H "Authorization: Bearer $ACCESS")

existing_names="$(curl -fsS -m 10 "${auth[@]}" "$BASE/api/channel/?p=0&size=100" \
  | python3 -c 'import json,sys;d=json.load(sys.stdin).get("data") or {};items=d.get("items") or (d if isinstance(d,list) else []);print("\n".join(str(i.get("name","")) for i in items))')"

create_channel() {
  local name="$1" key="$2" base_url="$3" models="$4"
  if [[ -z "$key" ]]; then
    echo "skip channel $name: provider key is empty"
    return
  fi
  if grep -Fxq "$name" <<<"$existing_names"; then
    echo "channel $name already exists"
    return
  fi
  local payload
  payload="$(python3 - "$name" "$key" "$base_url" "$models" <<'PY'
import json,sys
name,key,base_url,models=sys.argv[1:5]
print(json.dumps({"mode":"single","channel":{
  "type":1,"name":name,"key":key,"base_url":base_url,
  "models":models,"group":"default","status":1,
  "test_model":models.split(",")[0],
}}))
PY
)"
  curl -fsS -m 15 "${auth[@]}" -X POST "$BASE/api/channel/" \
    -H 'Content-Type: application/json' -d "$payload" >/dev/null
  echo "created channel $name ($models)"
}

DEEPSEEK_MODELS="${DEEPSEEK_MODEL:-deepseek-chat}"
if [[ -n "${DEEPSEEK_REASONER_MODEL:-}" && "$DEEPSEEK_REASONER_MODEL" != "$DEEPSEEK_MODEL" ]]; then
  DEEPSEEK_MODELS="$DEEPSEEK_MODELS,$DEEPSEEK_REASONER_MODEL"
fi
create_channel "deepseek" "${DEEPSEEK_API_KEY:-}" "${DEEPSEEK_BASE_URL:-https://api.deepseek.com}" "$DEEPSEEK_MODELS"
create_channel "kimi" "${KIMI_API_KEY:-}" "${KIMI_BASE_URL:-https://api.moonshot.cn}" "${KIMI_MODEL:-kimi-k2-0905-preview}"

TOKENS="$(curl -fsS -m 10 "${auth[@]}" "$BASE/api/token/?p=0&size=100")"
TOKEN_ID="$(python3 -c '
import json,sys
d=json.load(sys.stdin).get("data") or {}
items=d.get("items") or []
print(next((str(i["id"]) for i in items if i.get("name")=="teemohub-gateway"), ""))
' <<<"$TOKENS")"

if [[ -z "$TOKEN_ID" ]]; then
  curl -fsS -m 10 "${auth[@]}" -X POST "$BASE/api/token/" -H 'Content-Type: application/json' \
    -d '{"name":"teemohub-gateway","remain_quota":0,"unlimited_quota":true,"expired_time":-1}' >/dev/null
  TOKENS="$(curl -fsS -m 10 "${auth[@]}" "$BASE/api/token/?p=0&size=100")"
  TOKEN_ID="$(python3 -c '
import json,sys
d=json.load(sys.stdin).get("data") or {}
items=d.get("items") or []
print(next((str(i["id"]) for i in items if i.get("name")=="teemohub-gateway"), ""))
' <<<"$TOKENS")"
fi

if [[ -z "$TOKEN_ID" ]]; then
  echo "failed to create teemohub-gateway token" >&2
  exit 1
fi

FULL_KEY="$(curl -fsS -m 10 "${auth[@]}" -X POST "$BASE/api/token/$TOKEN_ID/key" \
  | python3 -c 'import json,sys;print(json.load(sys.stdin)["data"]["key"])')"

python3 - "$FULL_KEY" <<'PY'
import os,sys,re
key=sys.argv[1]
path=".env"
lines=open(path).read().splitlines()
found=False
for i,l in enumerate(lines):
    if l.startswith("NEWAPI_TOKEN="):
        lines[i]="NEWAPI_TOKEN="+key
        found=True
if not found:
    lines.append("NEWAPI_TOKEN="+key)
fd=os.open(path, os.O_WRONLY|os.O_TRUNC, 0o600)
with os.fdopen(fd,"w") as f:
    f.write("\n".join(lines)+"\n")
PY
chmod 600 .env
echo "teemoHub gateway token ready (stored in .env)"
echo "channels: $(curl -fsS -m 10 "${auth[@]}" "$BASE/api/channel/?p=0&size=100" | python3 -c 'import json,sys;d=json.load(sys.stdin).get("data") or {};items=d.get("items") or [];print(", ".join(i.get("name","?") for i in items) or "none")')"
