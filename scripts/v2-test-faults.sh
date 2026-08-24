#!/usr/bin/env sh
set -eu

PROJECT=${V2_FAULT_PROJECT:-codex-cpa-v2-fault-test}
PUBLIC_PORT=${V2_FAULT_PUBLIC_PORT:-29317}
INTERNAL_PORT=${V2_FAULT_INTERNAL_PORT:-29319}
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEMP_DIR=$(mktemp -d)
GATEWAY_FIXTURES="$TEMP_DIR/gateway"
EDGE_FIXTURES="$TEMP_DIR/edge"
PUBLIC_URL="http://127.0.0.1:${PUBLIC_PORT}"
INTERNAL_URL="http://127.0.0.1:${INTERNAL_PORT}"

case "$PROJECT" in
  *[!A-Za-z0-9_.-]*|'')
    echo "invalid v2 fault Test Compose project name" >&2
    exit 1
    ;;
esac

compose() {
  V2_TEST_PUBLIC_PORT="$PUBLIC_PORT" \
  V2_TEST_INTERNAL_PORT="$INTERNAL_PORT" \
  V2_TEST_GATEWAY_FIXTURE_DIR="$GATEWAY_FIXTURES" \
  V2_TEST_EDGE_FIXTURE_DIR="$EDGE_FIXTURES" \
    docker compose \
      -p "$PROJECT" \
      --project-directory "$ROOT_DIR" \
      -f "$ROOT_DIR/docker-compose.v2-test.yml" \
      "$@"
}

cleanup() {
  compose down --remove-orphans >/dev/null 2>&1 || true
  rm -rf -- "$TEMP_DIR"
}
trap cleanup EXIT HUP INT TERM

cp -R "$ROOT_DIR/testdata/v2/gateway" "$GATEWAY_FIXTURES"
cp -R "$ROOT_DIR/testdata/v2/edge" "$EDGE_FIXTURES"
cp "$GATEWAY_FIXTURES/auth-snapshot.json" "$TEMP_DIR/auth-snapshot.valid.json"

compose up -d --build --wait

models_status() {
  curl --noproxy '*' -sS -o "$TEMP_DIR/models.json" -w '%{http_code}' \
    -H 'Authorization: Bearer fixture-external-key' \
    "$PUBLIC_URL/v1/models"
}

wait_for_status() {
  expected=$1
  attempts=${2:-30}
  index=0
  while [ "$index" -lt "$attempts" ]; do
    status=$(models_status || true)
    if [ "$status" = "$expected" ]; then
      return 0
    fi
    index=$((index + 1))
    sleep 1
  done
  echo "timed out waiting for model status $expected; last status ${status:-none}" >&2
  return 1
}

wait_for_slot() {
  expected=$1
  attempts=${2:-20}
  index=0
  while [ "$index" -lt "$attempts" ]; do
    slot=$(curl --noproxy '*' -fsS "$INTERNAL_URL/__internal/edge/slot" 2>/dev/null || true)
    if [ "$slot" = "$expected" ]; then
      return 0
    fi
    index=$((index + 1))
    sleep 1
  done
  echo "timed out waiting for Edge slot $expected; last slot ${slot:-none}" >&2
  return 1
}

wait_for_status 200

# Upstream loss must be a bounded 502 and must not weaken API-Key rejection.
compose stop cliproxy-alpha >/dev/null
wait_for_status 502 10
python3 - "$TEMP_DIR/models.json" <<'PY'
import json
import sys
from pathlib import Path

payload = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
if payload.get("error", {}).get("code") != "upstream_unavailable":
    raise SystemExit("upstream failure did not preserve the 502 error contract")
PY
invalid_status=$(
  curl --noproxy '*' -sS -o "$TEMP_DIR/invalid.json" -w '%{http_code}' \
    -H 'Authorization: Bearer fixture-invalid-key' \
    "$PUBLIC_URL/v1/models"
)
test "$invalid_status" = "401"
compose start cliproxy-alpha >/dev/null
wait_for_status 200

# A corrupted auth snapshot is retained briefly, then fails closed after the
# five-second freshness contract. Restoring the valid atomic fixture recovers.
printf '%s\n' '{"version":1,"broken":true}' >"$GATEWAY_FIXTURES/auth-snapshot.json"
wait_for_status 503 15
python3 - "$TEMP_DIR/models.json" <<'PY'
import json
import sys
from pathlib import Path

payload = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
if payload.get("error", {}).get("code") != "authentication_snapshot_unavailable":
    raise SystemExit("corrupt auth snapshot did not fail closed")
PY
cp "$TEMP_DIR/auth-snapshot.valid.json" "$GATEWAY_FIXTURES/auth-snapshot.json"
wait_for_status 200

# Invalid Edge selection must keep the last valid slot. A valid atomic switch
# changes only new requests; an already-open SSE stays on its original proxy.
printf '%s\n' 'invalid edge selection' >"$EDGE_FIXTURES/active-gateway.conf"
sleep 1
wait_for_slot blue
printf '%s\n' 'set $active_gateway_backend gateway-blue:8317;' >"$EDGE_FIXTURES/active-gateway.conf"
wait_for_slot blue

curl --noproxy '*' -fsS -N \
  -H 'Authorization: Bearer fixture-external-key' \
  -H 'Content-Type: application/json' \
  --data-binary '{"stream":true,"fixture_delay_ms":750}' \
  "$PUBLIC_URL/v1/responses" >"$TEMP_DIR/drain-stream.txt" &
stream_pid=$!
index=0
while [ "$index" -lt 20 ] && ! grep -F 'event: response.created' "$TEMP_DIR/drain-stream.txt" >/dev/null 2>&1; do
  index=$((index + 1))
  sleep 1
done
grep -F 'event: response.created' "$TEMP_DIR/drain-stream.txt" >/dev/null

printf '%s\n' 'set $active_gateway_backend gateway-green:8317;' >"$EDGE_FIXTURES/active-gateway.conf"
wait_for_slot green
wait "$stream_pid"
grep -F 'event: response.completed' "$TEMP_DIR/drain-stream.txt" >/dev/null
grep -F 'data: [DONE]' "$TEMP_DIR/drain-stream.txt" >/dev/null
wait_for_status 200

printf '%s\n' 'Go v2 isolated fault, fencing, recovery, and stream-drain test passed'
