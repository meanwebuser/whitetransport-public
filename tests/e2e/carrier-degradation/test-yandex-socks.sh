#!/usr/bin/env bash
# Two real daemon processes move a SOCKS HTTP payload through the Yandex adapter.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
GO_ROOT="$REPO_ROOT/core/go"
TEST_TMP_ROOT="${WT_TEST_TMP_ROOT:-$REPO_ROOT/.tmp}"
mkdir -p "$TEST_TMP_ROOT"
RUN_DIR="$(mktemp -d "$TEST_TMP_ROOT/wt-carrier-yandex-socks.XXXXXX")"
BIN="$RUN_DIR/whitetransportd"
NODE_CFG="$RUN_DIR/node.json"
CLIENT_CFG="$RUN_DIR/client.json"
NODE_LOG="$RUN_DIR/node.log"
CLIENT_LOG="$RUN_DIR/client.log"
FIXTURE_STATE="$RUN_DIR/yandex-state"
NODE_PID="" CLIENT_PID="" FIXTURE_PID="" SUCCESS=false

reserve_port() {
    python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}

cleanup() {
    local status=$?
    for pid in "$CLIENT_PID" "$NODE_PID" "$FIXTURE_PID"; do
        [[ -z "$pid" ]] || kill "$pid" 2>/dev/null || true
    done
    for pid in "$CLIENT_PID" "$NODE_PID" "$FIXTURE_PID"; do
        [[ -z "$pid" ]] || wait "$pid" 2>/dev/null || true
    done
    if [[ "$SUCCESS" != true ]]; then
        printf 'yandex SOCKS artifacts: %s\n' "$RUN_DIR" >&2
        [[ ! -f "$CLIENT_LOG" ]] || tail -n 60 "$CLIENT_LOG" >&2
        [[ ! -f "$NODE_LOG" ]] || tail -n 60 "$NODE_LOG" >&2
    fi
    rm -rf "$RUN_DIR"
    exit "$status"
}
trap cleanup EXIT

wait_http() {
    local url="$1"
    for _ in $(seq 1 120); do
        curl --noproxy '*' --max-time 1 -sf "$url" >/dev/null 2>&1 && return 0
        sleep .1
    done
    return 1
}

NODE_API_PORT="$(reserve_port)"
CLIENT_API_PORT="$(reserve_port)"
NODE_SOCKS_PORT="$(reserve_port)"
CLIENT_SOCKS_PORT="$(reserve_port)"
FIXTURE_PORT="$(reserve_port)"
HTTP_PORT="$(reserve_port)"
NONCE="yandex-socks-$RANDOM-$RANDOM"
mkdir -p "$RUN_DIR/control" "$FIXTURE_STATE"
for command_name in unshare ip; do command -v "$command_name" >/dev/null; done
unshare -Un --map-root-user true >/dev/null 2>&1

python3 "$SCRIPT_DIR/yandex_fixture.py" --port "$FIXTURE_PORT" --state-dir "$FIXTURE_STATE" >"$RUN_DIR/yandex.log" 2>&1 &
FIXTURE_PID=$!
wait_http "http://127.0.0.1:${FIXTURE_PORT}/health"

python3 - "$NODE_CFG" "$CLIENT_CFG" "$RUN_DIR" "$NODE_API_PORT" "$CLIENT_API_PORT" "$NODE_SOCKS_PORT" "$CLIENT_SOCKS_PORT" "$FIXTURE_PORT" <<'PY'
import json, os, sys
node_path, client_path, root, node_api, client_api, node_socks, client_socks, fixture_port = sys.argv[1:]

token_store={
    "tokens":[{"id":"local-test-bootstrap","platform":"local","kind":"api_key","lifecycle":"embedded","status":"active","value":"deterministic-local-session-key-not-a-secret"}],
    "bindings":[{"token_id":"local-test-bootstrap","platform":"local","connection_type":"messages","channel_id":"control","role":"discovery","priority":10,"enabled":True}],
}
def carriers():
    return [
        {"id":"local.control","carrier_type":"file.mailbox","file_mailbox":{"dir":os.path.join(root,"control")},"endpoint":{"id":"local.control","address":"control"}},
        {"id":"yandex.primary","carrier_type":"yandex.disk.files","endpoint":{"id":"yandex.primary","address":"egress"},"yandex_disk":{"oauth_token":"deterministic-local-yandex-fixture","base_url":f"http://127.0.0.1:{fixture_port}/v1/disk","base_path":"/carrier-yandex-socks","cleanup_after_read":False,"min_send_interval_ms":0}},
        {"id":"broken.vk","carrier_type":"vk.messages","endpoint":{"id":"broken.vk","address":"2000000001"},"vk_messages":{}},
    ]
common={
    "enabled_carriers":["local.control","yandex.primary","broken.vk"],
    "token_store":token_store,
    "upstream_proxy":{"url":"","client_egress_only":True,"apply_to_carriers":False},
}
node={**common,"role":"node","node_id":"yandex-local-node","display_name":"Yandex Local Node","listen_api":f"127.0.0.1:{node_api}","socks_listen":f"127.0.0.1:{node_socks}","state_file":os.path.join(root,"node-state.json"),"carrier_configs":carriers()}
client={**common,"role":"client","client_id":"yandex-local-client","listen_api":f"127.0.0.1:{client_api}","socks_listen":f"127.0.0.1:{client_socks}","state_file":os.path.join(root,"client-state.json"),"carrier_configs":carriers()}
for path, cfg in ((node_path,node),(client_path,client)):
    with open(path,"w",encoding="utf-8") as handle: json.dump(cfg,handle,separators=(",",":"))
PY

(cd "$GO_ROOT" && GOPROXY=off /usr/local/go/bin/go build -o "$BIN" ./cmd/whitetransportd)
env \
    WT_NODE_BIN="$BIN" \
    WT_NODE_CFG="$NODE_CFG" \
    WT_NODE_FIXTURE="$SCRIPT_DIR/yandex_fixture.py" \
    WT_NODE_NONCE_FIXTURE="$SCRIPT_DIR/nonce_http_fixture.py" \
    WT_NODE_FIXTURE_PORT="$FIXTURE_PORT" \
    WT_NODE_HTTP_PORT="$HTTP_PORT" \
    WT_NODE_NONCE="$NONCE" \
    WT_NODE_FIXTURE_STATE="$FIXTURE_STATE" \
    WT_NODE_API_PORT="$NODE_API_PORT" \
    WT_NODE_DEGRADED_MARKER="$RUN_DIR/node-vk-degraded.ok" \
    unshare -Un --map-root-user bash -c '
set -Eeuo pipefail
ip link set lo up
python3 "$WT_NODE_FIXTURE" --port "$WT_NODE_FIXTURE_PORT" --state-dir "$WT_NODE_FIXTURE_STATE" &
fixture_pid=$!
python3 "$WT_NODE_NONCE_FIXTURE" --port "$WT_NODE_HTTP_PORT" --nonce "$WT_NODE_NONCE" &
target_pid=$!
WT_DEBUG=1 "$WT_NODE_BIN" -config "$WT_NODE_CFG" -serve &
daemon_pid=$!
cleanup_node() {
    kill "$daemon_pid" "$target_pid" "$fixture_pid" 2>/dev/null || true
    wait "$daemon_pid" "$target_pid" "$fixture_pid" 2>/dev/null || true
}
trap cleanup_node EXIT TERM INT
node_degraded=false
for _ in $(seq 1 100); do
    if curl --noproxy "*" -sf "http://127.0.0.1:${WT_NODE_API_PORT}/v1/carriers" | python3 -c '\''
import json,sys
rows=json.load(sys.stdin)
assert rows["broken.vk"]["lifecycle_state"] == "degraded", rows
assert rows["broken.vk"]["error_code"] == "credential_missing", rows
assert rows["yandex.primary"]["lifecycle_state"] == "constructed", rows
'\''; then
        touch "$WT_NODE_DEGRADED_MARKER"
        node_degraded=true
        break
    fi
    sleep .1
done
[[ "$node_degraded" == true ]]
wait "$daemon_pid"
' >"$NODE_LOG" 2>&1 &
NODE_PID=$!
for _ in $(seq 1 100); do
    kill -0 "$NODE_PID" 2>/dev/null
    [[ ! -f "$RUN_DIR/node-vk-degraded.ok" ]] || break
    sleep .1
done
[[ -f "$RUN_DIR/node-vk-degraded.ok" ]]
WT_DEBUG=1 "$BIN" -config "$CLIENT_CFG" -serve >"$CLIENT_LOG" 2>&1 &
CLIENT_PID=$!
wait_http "http://127.0.0.1:${CLIENT_API_PORT}/health"

for _ in $(seq 1 160); do
    if curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API_PORT}/v1/nodes" | python3 -c 'import json,sys; raise SystemExit(0 if any(n.get("node_id")=="yandex-local-node" and n.get("available") for n in json.load(sys.stdin)) else 1)' 2>/dev/null; then
        break
    fi
    sleep .1
done
curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API_PORT}/v1/nodes" | python3 -c 'import json,sys; assert any(n.get("node_id")=="yandex-local-node" and n.get("available") for n in json.load(sys.stdin))'

curl --noproxy '*' --max-time 20 -sf -X POST "http://127.0.0.1:${CLIENT_API_PORT}/v1/session/connect" -H 'Content-Type: application/json' -d '{"node_id":"yandex-local-node"}' >/dev/null
for _ in $(seq 1 160); do
    if curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API_PORT}/v1/status" | python3 -c 'import json,sys; s=json.load(sys.stdin); raise SystemExit(0 if s.get("state")=="connected" and any(e.get("id")=="yandex.primary" for e in s.get("egress_endpoints",[])) else 1)' 2>/dev/null; then
        break
    fi
    sleep .1
done
curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API_PORT}/v1/status" | python3 -c 'import json,sys; s=json.load(sys.stdin); assert s.get("state")=="connected" and any(e.get("id")=="yandex.primary" for e in s.get("egress_endpoints",[])),s'

if curl --noproxy '*' --max-time 1 -sf "http://127.0.0.1:${HTTP_PORT}/nonce" >/dev/null 2>&1; then
    printf 'client namespace reached exit-only target directly\n' >&2
    exit 1
fi
FRAME_COUNT_BEFORE="$(find "$FIXTURE_STATE" -maxdepth 1 -type f | wc -l)"
BODY="$(curl --noproxy '' --max-time 45 -sf -x "socks5h://127.0.0.1:${CLIENT_SOCKS_PORT}" "http://127.0.0.1:${HTTP_PORT}/nonce")"
[[ "$BODY" == "$NONCE" ]]
grep -q 'socks5 connected .*route=yandex.primary' "$CLIENT_LOG"
FRAME_COUNT="$(find "$FIXTURE_STATE" -maxdepth 1 -type f | wc -l)"
[[ "$FRAME_COUNT" -gt "$FRAME_COUNT_BEFORE" ]]
if grep -R -a -F "$NONCE" "$FIXTURE_STATE" >/dev/null 2>&1; then
    printf 'plaintext SOCKS nonce leaked into Yandex frames\n' >&2
    exit 1
fi
curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API_PORT}/v1/carriers" | python3 -c '
import json,sys
rows=json.load(sys.stdin)
assert rows["broken.vk"]["lifecycle_state"] == "degraded", rows
assert rows["yandex.primary"]["lifecycle_state"] == "constructed", rows
'

SUCCESS=true
printf '{"test":"carrier-yandex-socks","exit":0,"daemonCount":2,"payloadVerified":true,"directTargetReachable":false,"route":"yandex.primary","degradedCarrier":"broken.vk","clientDegraded":true,"nodeDegraded":true,"fixtureFramesBefore":%s,"fixtureFramesAfter":%s,"plaintextNonceFrames":0}\n' "$FRAME_COUNT_BEFORE" "$FRAME_COUNT"
