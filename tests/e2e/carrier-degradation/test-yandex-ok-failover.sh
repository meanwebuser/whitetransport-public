#!/usr/bin/env bash
# Concrete Yandex primary -> OK Docs backup failover with an exit-only target.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
RUN_DIR="$(mktemp -d /tmp/wt-yandex-ok-failover.XXXXXX)"
BIN="$RUN_DIR/whitetransportd" NODE_CFG="$RUN_DIR/node.json" CLIENT_CFG="$RUN_DIR/client.json"
NODE_LOG="$RUN_DIR/node.log" CLIENT_LOG="$RUN_DIR/client.log"
NODE_PID="" CLIENT_PID="" YANDEX_PID="" OK_PID="" HOLD_PID="" SUCCESS=false

reserve_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }
cleanup() {
    local status=$?
    for pid in "$HOLD_PID" "$CLIENT_PID" "$NODE_PID" "$OK_PID" "$YANDEX_PID"; do [[ -z "$pid" ]] || kill "$pid" 2>/dev/null || true; done
    for pid in "$HOLD_PID" "$CLIENT_PID" "$NODE_PID" "$OK_PID" "$YANDEX_PID"; do [[ -z "$pid" ]] || wait "$pid" 2>/dev/null || true; done
    if [[ "$SUCCESS" != true ]]; then
        printf 'Yandex-OK failover artifacts: %s\n' "$RUN_DIR" >&2
        [[ ! -f "$CLIENT_LOG" ]] || tail -n 100 "$CLIENT_LOG" >&2
        [[ ! -f "$NODE_LOG" ]] || tail -n 80 "$NODE_LOG" >&2
    fi
    rm -rf "$RUN_DIR"
    exit "$status"
}
trap cleanup EXIT

wait_http() { local url="$1"; for _ in $(seq 1 120); do curl --noproxy '*' --max-time 1 -sf "$url" >/dev/null 2>&1 && return 0; sleep .1; done; return 1; }
for command_name in unshare ip; do command -v "$command_name" >/dev/null; done
unshare -Un --map-root-user true >/dev/null 2>&1

NODE_API="$(reserve_port)" CLIENT_API="$(reserve_port)" NODE_SOCKS="$(reserve_port)" CLIENT_SOCKS="$(reserve_port)"
YANDEX_PORT="$(reserve_port)" OK_PORT="$(reserve_port)" HTTP_PORT="$(reserve_port)"
PRIMARY_NONCE="yandex-primary-$RANDOM-$RANDOM" BACKUP_NONCE="ok-backup-$RANDOM-$RANDOM"
YANDEX_STATE="$RUN_DIR/yandex-state" OK_STATE="$RUN_DIR/ok-state" YANDEX_FAIL="$RUN_DIR/yandex.failed"
HOLD_MARKER="$RUN_DIR/hold.started"
mkdir -p "$RUN_DIR/control" "$YANDEX_STATE" "$OK_STATE"

python3 "$SCRIPT_DIR/yandex_fixture.py" --port "$YANDEX_PORT" --state-dir "$YANDEX_STATE" --failure-flag "$YANDEX_FAIL" >"$RUN_DIR/yandex-host.log" 2>&1 & YANDEX_PID=$!
python3 "$SCRIPT_DIR/ok_docs_fixture.py" --port "$OK_PORT" --state-dir "$OK_STATE" --initial-delay-ms 750 >"$RUN_DIR/ok-host.log" 2>&1 & OK_PID=$!
wait_http "http://127.0.0.1:${YANDEX_PORT}/health"
wait_http "http://127.0.0.1:${OK_PORT}/health"

python3 - "$NODE_CFG" "$CLIENT_CFG" "$RUN_DIR" "$NODE_API" "$CLIENT_API" "$NODE_SOCKS" "$CLIENT_SOCKS" "$YANDEX_PORT" "$OK_PORT" "$YANDEX_FAIL" <<'PY'
import json,os,sys
node_path,client_path,root,node_api,client_api,node_socks,client_socks,yandex_port,ok_port,_failure=sys.argv[1:]
store={"tokens":[{"id":"local-test-bootstrap","platform":"local","kind":"api_key","lifecycle":"embedded","status":"active","value":"deterministic-local-session-key-not-a-secret"}],"bindings":[{"token_id":"local-test-bootstrap","platform":"local","connection_type":"messages","channel_id":"control","role":"discovery","priority":10,"enabled":True}]}
def carriers(): return [
 {"id":"local.control","carrier_type":"file.mailbox","file_mailbox":{"dir":os.path.join(root,"control")},"endpoint":{"id":"local.control","address":"control"}},
 {"id":"yandex.primary","carrier_type":"yandex.disk.files","endpoint":{"id":"yandex.primary","address":"egress-yandex"},"yandex_disk":{"oauth_token":"deterministic-yandex-fixture","base_url":f"http://127.0.0.1:{yandex_port}/v1/disk","base_path":"/yandex-ok-failover","cleanup_after_read":False,"min_send_interval_ms":0}},
 {"id":"ok.backup","carrier_type":"ok.docs.256","endpoint":{"id":"ok.backup","address":"chat:C123"},"ok_docs":{"access_token":"deterministic-ok-token","application_key":"deterministic-ok-app","session_secret_key":"deterministic-ok-secret","base_url":f"http://127.0.0.1:{ok_port}/fb.do"}},
]
common={"enabled_carriers":["local.control","yandex.primary","ok.backup"],"token_store":store,"upstream_proxy":{"url":"","client_egress_only":True,"apply_to_carriers":False}}
node={**common,"role":"node","node_id":"yandex-ok-node","listen_api":f"127.0.0.1:{node_api}","socks_listen":f"127.0.0.1:{node_socks}","state_file":os.path.join(root,"node-state.json"),"carrier_configs":carriers()}
client={**common,"role":"client","client_id":"yandex-ok-client","listen_api":f"127.0.0.1:{client_api}","socks_listen":f"127.0.0.1:{client_socks}","state_file":os.path.join(root,"client-state.json"),"carrier_configs":carriers()}
for path,cfg in ((node_path,node),(client_path,client)): json.dump(cfg,open(path,"w",encoding="utf-8"),separators=(",",":"))
PY

(cd "$REPO_ROOT/core/go" && GOPROXY=off /usr/local/go/bin/go build -o "$BIN" ./cmd/whitetransportd)
env WT_NODE_BIN="$BIN" WT_NODE_CFG="$NODE_CFG" WT_YANDEX_FIXTURE="$SCRIPT_DIR/yandex_fixture.py" WT_OK_FIXTURE="$SCRIPT_DIR/ok_docs_fixture.py" WT_TARGET_FIXTURE="$SCRIPT_DIR/nonce_http_fixture.py" WT_YANDEX_PORT="$YANDEX_PORT" WT_OK_PORT="$OK_PORT" WT_HTTP_PORT="$HTTP_PORT" WT_PRIMARY_NONCE="$PRIMARY_NONCE" WT_BACKUP_NONCE="$BACKUP_NONCE" WT_HOLD_MARKER="$HOLD_MARKER" WT_YANDEX_STATE="$YANDEX_STATE" WT_OK_STATE="$OK_STATE" WT_YANDEX_FAIL="$YANDEX_FAIL" unshare -Un --map-root-user bash -c '
set -Eeuo pipefail; ip link set lo up
python3 "$WT_YANDEX_FIXTURE" --port "$WT_YANDEX_PORT" --state-dir "$WT_YANDEX_STATE" --failure-flag "$WT_YANDEX_FAIL" & yp=$!
python3 "$WT_OK_FIXTURE" --port "$WT_OK_PORT" --state-dir "$WT_OK_STATE" --initial-delay-ms 750 & op=$!
python3 "$WT_TARGET_FIXTURE" --port "$WT_HTTP_PORT" --nonce "$WT_PRIMARY_NONCE" --backup-nonce "$WT_BACKUP_NONCE" --hold-marker "$WT_HOLD_MARKER" & hp=$!
WT_DEBUG=1 "$WT_NODE_BIN" -config "$WT_NODE_CFG" -serve & dp=$!
cleanup_node(){ kill "$dp" "$hp" "$op" "$yp" 2>/dev/null || true; wait "$dp" "$hp" "$op" "$yp" 2>/dev/null || true; }
trap cleanup_node EXIT TERM INT; wait "$dp"
' >"$NODE_LOG" 2>&1 & NODE_PID=$!
sleep .4; kill -0 "$NODE_PID" 2>/dev/null
WT_DEBUG=1 "$BIN" -config "$CLIENT_CFG" -serve >"$CLIENT_LOG" 2>&1 & CLIENT_PID=$!
wait_http "http://127.0.0.1:${CLIENT_API}/health"

for _ in $(seq 1 180); do curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API}/v1/nodes" | python3 -c 'import json,sys; raise SystemExit(0 if any(n.get("node_id")=="yandex-ok-node" and n.get("available") for n in json.load(sys.stdin)) else 1)' 2>/dev/null && break; sleep .1; done
curl --noproxy '*' -sf -X POST "http://127.0.0.1:${CLIENT_API}/v1/session/connect" -H 'Content-Type: application/json' -d '{"node_id":"yandex-ok-node"}' >/dev/null
for _ in $(seq 1 180); do curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API}/v1/status" | python3 -c 'import json,sys; raise SystemExit(0 if json.load(sys.stdin).get("state")=="connected" else 1)' 2>/dev/null && break; sleep .1; done

if curl --noproxy '*' --max-time 1 -sf "http://127.0.0.1:${HTTP_PORT}/nonce" >/dev/null 2>&1; then printf 'exit target reachable directly\n' >&2; exit 1; fi
curl --noproxy '*' -sf -X POST "http://127.0.0.1:${CLIENT_API}/v1/session/egress/select" -H 'Content-Type: application/json' -d '{"egress_endpoint_id":"yandex.primary"}' >/dev/null
FIRST="$(curl --noproxy '' --max-time 50 -sf -x "socks5h://127.0.0.1:${CLIENT_SOCKS}" "http://127.0.0.1:${HTTP_PORT}/nonce")"
[[ "$FIRST" == "$PRIMARY_NONCE" ]]; grep -q 'socks5 connected .*route=yandex.primary' "$CLIENT_LOG"

curl --noproxy '' --max-time 30 -sf -x "socks5h://127.0.0.1:${CLIENT_SOCKS}" "http://127.0.0.1:${HTTP_PORT}/hold" >"$RUN_DIR/hold.out" 2>"$RUN_DIR/hold.err" & HOLD_PID=$!
for _ in $(seq 1 100); do [[ -f "$HOLD_MARKER" ]] && [[ "$(grep -c 'socks5 connected .*route=yandex.primary' "$CLIENT_LOG" || true)" -ge 2 ]] && break; sleep .1; done
[[ -f "$HOLD_MARKER" ]] || { printf 'exit target never entered hold handler\n' >&2; exit 1; }
[[ "$(grep -c 'socks5 connected .*route=yandex.primary' "$CLIENT_LOG" || true)" -ge 2 ]] || { printf 'hold stream was not established over Yandex\n' >&2; exit 1; }
curl --noproxy '*' -sf -X POST "http://127.0.0.1:${CLIENT_API}/v1/session/egress/select" -H 'Content-Type: application/json' -d '{"egress_endpoint_id":"auto"}' | python3 -c 'import json,sys; s=json.load(sys.stdin); assert not s.get("selected_egress_endpoint_id") and s.get("automatic_egress_endpoint_id")=="yandex.primary",s'
curl --noproxy '*' -sf -X POST "http://127.0.0.1:${OK_PORT}/control/delay?ms=0" >/dev/null
FAILURE_STARTED_MS="$(date +%s%3N)"
curl --noproxy '*' -sf -X POST "http://127.0.0.1:${YANDEX_PORT}/control/fail" >/dev/null
for _ in $(seq 1 140); do kill -0 "$HOLD_PID" 2>/dev/null || break; sleep .1; done
if kill -0 "$HOLD_PID" 2>/dev/null; then printf 'open Yandex stream exceeded interruption bound\n' >&2; exit 1; fi
set +e; wait "$HOLD_PID" 2>/dev/null; HOLD_STATUS=$?; set -e; HOLD_PID=""
[[ "$HOLD_STATUS" -ne 0 ]] || { printf 'hold stream completed successfully instead of being interrupted\n' >&2; exit 1; }
INTERRUPTION_MS="$(( $(date +%s%3N) - FAILURE_STARTED_MS ))"
[[ "$INTERRUPTION_MS" -le 15000 ]] || { printf 'hold interruption took %sms\n' "$INTERRUPTION_MS" >&2; exit 1; }

SECOND="$(curl --noproxy '' --max-time 60 -sf -x "socks5h://127.0.0.1:${CLIENT_SOCKS}" "http://127.0.0.1:${HTTP_PORT}/backup")"
[[ "$SECOND" == "$BACKUP_NONCE" ]]; grep -q 'socks5 connected .*route=ok.backup' "$CLIENT_LOG"
curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API}/v1/status" | python3 -c 'import json,sys; s=json.load(sys.stdin); assert s.get("state")=="connected",s'
curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API}/v1/carriers" | python3 -c 'import json,sys; c=json.load(sys.stdin); y=c["yandex.primary"]; assert y.get("lifecycle_state")=="degraded" and y.get("failure_stage")=="runtime" and y.get("error_code")=="io_failure",y'
YANDEX_FRAMES="$(find "$YANDEX_STATE" -maxdepth 1 -type f | wc -l)"; OK_MESSAGES="$(find "$OK_STATE" -maxdepth 1 -type f -name 'message-*.json' | wc -l)"
[[ "$YANDEX_FRAMES" -gt 0 && "$OK_MESSAGES" -gt 0 ]]
SUCCESS=true
printf '{"test":"carrier-yandex-ok-failover","exit":0,"primaryRoute":"yandex.primary","backupRoute":"ok.backup","primaryNonceValid":true,"backupNonceValid":true,"directTargetReachable":false,"openStreamInterrupted":true,"primaryLifecycle":"degraded","primaryFailureStage":"runtime","daemonRestarted":false,"yandexFrames":%s,"okMessages":%s}\n' "$YANDEX_FRAMES" "$OK_MESSAGES"
