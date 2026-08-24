#!/usr/bin/env sh
set -eu

PUBLIC_PORT=${V2_TEST_PUBLIC_PORT:-28317}
INTERNAL_PORT=${V2_TEST_INTERNAL_PORT:-28319}
PROJECT=${V2_TEST_PROJECT:-codex-cpa-v2-test}
PUBLIC_URL="http://127.0.0.1:${PUBLIC_PORT}"
INTERNAL_URL="http://127.0.0.1:${INTERNAL_PORT}"
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEMP_DIR=$(mktemp -d)

cleanup() {
  rm -rf -- "$TEMP_DIR"
}
trap cleanup EXIT HUP INT TERM

case "$PROJECT" in
  *[!A-Za-z0-9_.-]*|'')
    echo "invalid v2 Test Compose project name" >&2
    exit 1
    ;;
esac

edge_id=$(
  docker compose \
    -p "$PROJECT" \
    --project-directory "$ROOT_DIR" \
    -f "$ROOT_DIR/docker-compose.v2-test.yml" \
    ps -q edge
)
test -n "$edge_id"
test "$(docker inspect --format '{{.State.Running}}' "$edge_id")" = "true"
test "$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "$edge_id")" = "$PROJECT"

public_binding=$(docker port "$edge_id" 8317/tcp)
internal_binding=$(docker port "$edge_id" 8319/tcp)
test "$public_binding" = "127.0.0.1:${PUBLIC_PORT}"
test "$internal_binding" = "127.0.0.1:${INTERNAL_PORT}"

health=$(curl --noproxy '*' -fsS "${PUBLIC_URL}/__health")
test "$health" = "ok"

curl --noproxy '*' -fsS -D "$TEMP_DIR/landing.headers" "${PUBLIC_URL}/" >"$TEMP_DIR/landing.html"
grep -F '<title>Codex CPA Cluster</title>' "$TEMP_DIR/landing.html" >/dev/null
grep -i '^Cache-Control: no-cache' "$TEMP_DIR/landing.headers" >/dev/null
python3 - "$TEMP_DIR/landing.html" >"$TEMP_DIR/portal-assets.txt" <<'PY'
import sys
from html.parser import HTMLParser
from pathlib import Path


class Assets(HTMLParser):
    def __init__(self):
        super().__init__()
        self.paths = []

    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        if tag == "script" and values.get("src", "").startswith("/portal/assets/"):
            self.paths.append(values["src"])
        if tag == "link" and values.get("rel") == "stylesheet" and values.get("href", "").startswith("/portal/assets/"):
            self.paths.append(values["href"])


assets = Assets()
assets.feed(Path(sys.argv[1]).read_text(encoding="utf-8"))
if len(assets.paths) < 2:
    raise SystemExit("React Portal entry does not reference built JavaScript and CSS")
print(*assets.paths, sep="\n")
PY
while IFS= read -r asset; do
  curl --noproxy '*' -fsS -D "$TEMP_DIR/asset.headers" "${PUBLIC_URL}${asset}" >/dev/null
  grep -i '^Cache-Control: public, max-age=31536000, immutable' "$TEMP_DIR/asset.headers" >/dev/null
done <"$TEMP_DIR/portal-assets.txt"
curl --noproxy '*' -fsS "${PUBLIC_URL}/native/" >"$TEMP_DIR/native.html"
grep -F '<div id="root"></div>' "$TEMP_DIR/native.html" >/dev/null
curl --noproxy '*' -fsS "${PUBLIC_URL}/admin/" >"$TEMP_DIR/admin.html"
grep -F '<div id="root"></div>' "$TEMP_DIR/admin.html" >/dev/null

unauthorized_status=$(
  curl --noproxy '*' -sS -o "$TEMP_DIR/unauthorized.json" -w '%{http_code}' \
    "${PUBLIC_URL}/v1/models"
)
test "$unauthorized_status" = "401"

curl --noproxy '*' -fsS \
  -H 'Authorization: Bearer fixture-external-key' \
  "${PUBLIC_URL}/v1/models" >"$TEMP_DIR/models.json"
python3 - "$TEMP_DIR/models.json" <<'PY'
import json
import sys
from pathlib import Path

payload = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
models = [item.get("id") for item in payload.get("data", [])]
if models != ["fixture-model"]:
    raise SystemExit("unexpected v2 Test model catalog")
PY

curl --noproxy '*' -fsS -N \
  -H 'Authorization: Bearer fixture-external-key' \
  -H 'Content-Type: application/json' \
  --data-binary "@$ROOT_DIR/testdata/v2/stream-request.json" \
  "${PUBLIC_URL}/v1/responses" >"$TEMP_DIR/stream.txt"
grep -F 'event: response.created' "$TEMP_DIR/stream.txt" >/dev/null
grep -F 'event: response.output_text.delta' "$TEMP_DIR/stream.txt" >/dev/null
grep -F '"delta":"OK"' "$TEMP_DIR/stream.txt" >/dev/null
grep -F 'event: response.completed' "$TEMP_DIR/stream.txt" >/dev/null
grep -F 'data: [DONE]' "$TEMP_DIR/stream.txt" >/dev/null

slot=$(curl --noproxy '*' -fsS "${INTERNAL_URL}/__internal/edge/slot")
test "$slot" = "blue"

printf '%s\n' 'Go v2 isolated data-plane smoke test passed'
