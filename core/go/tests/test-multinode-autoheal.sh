#!/usr/bin/env bash
# Deterministic production-path proof: SOCKS payload via node A, node A fails,
# automatic session reselection to node B, total outage and background recovery,
# then explicit disconnect during an outage must suppress automatic recovery.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
GO_ROOT="$REPO_ROOT/core/go"
TEST_TMP_ROOT="${WT_TEST_TMP_ROOT:-$REPO_ROOT/.tmp}"
mkdir -p "$TEST_TMP_ROOT"
RUN_DIR="$(mktemp -d "$TEST_TMP_ROOT/wt-multinode-autoheal.XXXXXX")"
NODE_A_PID="" NODE_B_PID="" CLIENT_PID="" HTTP_PID=""
NODE_A_LOG="$RUN_DIR/node-a.log" NODE_B_LOG="$RUN_DIR/node-b.log" CLIENT_LOG="$RUN_DIR/client.log"
PHASE=setup ERRORS=() SUCCEEDED=false
PRIMARY_PAYLOAD=false BACKUP_PAYLOAD=false NODE_A_STOPPED=false AUTOHEAL_OBSERVED=false DIRECT_FALLBACK_BLOCKED=false NODE_A_MAILBOX_INTACT=false
OUTAGE_OBSERVED=false BACKGROUND_RECOVERED=false RESTORED_PAYLOAD=false DISCONNECT_RESPECTED=false
RECOVERY_LATENCY_MS=0
SELECTED_NODE_ID="" START_MS="$(date +%s%3N)"

port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'; }
NODE_A_API="$(port)" NODE_B_API="$(port)" CLIENT_API="$(port)" CLIENT_SOCKS="$(port)" HTTP_PORT="$(port)"
CONTROL_DIR="$RUN_DIR/control" NODE_A_DIR="$RUN_DIR/node-a-egress" NODE_B_DIR="$RUN_DIR/node-b-egress"
NODE_A_CFG="$RUN_DIR/node-a.json" NODE_B_CFG="$RUN_DIR/node-b.json" CLIENT_CFG="$RUN_DIR/client.json" BINARY="$RUN_DIR/whitetransportd"

record_error() { ERRORS+=("${PHASE}:${1//$'\n'/ }"); }
fail() { record_error "$1"; exit 1; }
client_ns() { nsenter -t "$CLIENT_PID" -U -n --preserve-credentials -- "$@"; }

emit_result() {
    local status="$1"
    STATUS="$status" ELAPSED="$(($(date +%s%3N)-START_MS))" ERRORS_TEXT="$(printf '%s\n' "${ERRORS[@]:-}")" \
    PRIMARY_PAYLOAD="$PRIMARY_PAYLOAD" BACKUP_PAYLOAD="$BACKUP_PAYLOAD" NODE_A_STOPPED="$NODE_A_STOPPED" \
    AUTOHEAL_OBSERVED="$AUTOHEAL_OBSERVED" DIRECT_FALLBACK_BLOCKED="$DIRECT_FALLBACK_BLOCKED" NODE_A_MAILBOX_INTACT="$NODE_A_MAILBOX_INTACT" SELECTED_NODE_ID="$SELECTED_NODE_ID" \
    OUTAGE_OBSERVED="$OUTAGE_OBSERVED" BACKGROUND_RECOVERED="$BACKGROUND_RECOVERED" RESTORED_PAYLOAD="$RESTORED_PAYLOAD" DISCONNECT_RESPECTED="$DISCONNECT_RESPECTED" RECOVERY_LATENCY_MS="$RECOVERY_LATENCY_MS" \
    BINARY_SHA256="${BINARY_SHA256:-}" python3 - <<'PY'
import json, os
print(json.dumps({
    "test":"multinode-autoheal-local",
    "binarySha256":os.environ["BINARY_SHA256"],
    "proofLevel":"local-integration",
    "transport":"file.mailbox",
    "daemonCount":3,
    "productionCredentialsUsed":False,
    "tokenStorePresent":False,
    "socksStrict":True,
    "primaryPayload":os.environ["PRIMARY_PAYLOAD"]=="true",
    "nodeAStopped":os.environ["NODE_A_STOPPED"]=="true",
    "nodeAMailboxIntact":os.environ["NODE_A_MAILBOX_INTACT"]=="true",
    "autoHealObserved":os.environ["AUTOHEAL_OBSERVED"]=="true",
    "backupPayload":os.environ["BACKUP_PAYLOAD"]=="true",
    "directFallbackBlocked":os.environ["DIRECT_FALLBACK_BLOCKED"]=="true",
    "totalOutageObserved":os.environ["OUTAGE_OBSERVED"]=="true",
    "backgroundRecoveredBeforePayload":os.environ["BACKGROUND_RECOVERED"]=="true",
    "restoredPayload":os.environ["RESTORED_PAYLOAD"]=="true",
    "disconnectRespectedAfterNodeReturn":os.environ["DISCONNECT_RESPECTED"]=="true",
    "recoveryLatencyMs":int(os.environ["RECOVERY_LATENCY_MS"]),
    "selectedNodeId":os.environ["SELECTED_NODE_ID"] or None,
    "latencyMs":int(os.environ["ELAPSED"]),
    "exit":int(os.environ["STATUS"]),
    "phaseErrors":[x for x in os.environ["ERRORS_TEXT"].splitlines() if x],
}, separators=(",",":")))
PY
}

cleanup() {
    local status=$?
    trap - EXIT ERR
    PHASE=cleanup
    if [[ -n "$CLIENT_PID" ]] && kill -0 "$CLIENT_PID" 2>/dev/null; then
        client_ns curl --noproxy '*' --max-time 3 -sf -X POST "http://127.0.0.1:$CLIENT_API/v1/session/disconnect" --data '' >/dev/null 2>&1 || true
    fi
    for pid in "$NODE_A_PID" "$NODE_B_PID" "$CLIENT_PID" "$HTTP_PID"; do
        [[ -z "$pid" ]] || kill "$pid" 2>/dev/null || true
    done
    for pid in "$NODE_A_PID" "$NODE_B_PID" "$CLIENT_PID" "$HTTP_PID"; do
        [[ -z "$pid" ]] || wait "$pid" 2>/dev/null || true
    done
    [[ "$SUCCEEDED" == true ]] || {
        [[ -f "$NODE_A_LOG" ]] && tail -n 40 "$NODE_A_LOG" >&2
        [[ -f "$NODE_B_LOG" ]] && tail -n 40 "$NODE_B_LOG" >&2
        [[ -f "$CLIENT_LOG" ]] && tail -n 80 "$CLIENT_LOG" >&2
    }
    chmod -R u+rwX "$RUN_DIR" 2>/dev/null || true
    rm -rf "$RUN_DIR"
    emit_result "$status"
    exit "$status"
}
trap cleanup EXIT
trap 'record_error "unexpected command failure at line $LINENO"' ERR

PHASE=preflight
for command in unshare nsenter ip curl; do command -v "$command" >/dev/null || fail "$command is required"; done
unshare -Un --map-root-user true >/dev/null 2>&1 || fail 'unprivileged user and network namespaces are unavailable'

PHASE=config
mkdir -p "$CONTROL_DIR" "$NODE_A_DIR" "$NODE_B_DIR"
cat >"$NODE_A_CFG" <<EOF
{"role":"node","node_id":"node-a","display_name":"A","listen_api":"127.0.0.1:$NODE_A_API","socks_listen":"127.0.0.1:0","enabled_carriers":["local.control","local.egress.node-a"],"carrier_configs":[{"id":"local.control","carrier_type":"file.mailbox","file_mailbox":{"dir":"$CONTROL_DIR"},"endpoint":{"id":"local.control","address":"control"}},{"id":"local.egress.node-a","carrier_type":"file.mailbox","file_mailbox":{"dir":"$NODE_A_DIR","allow_egress":true},"endpoint":{"id":"local.egress.node-a","address":"node-a"}}],"bootstrap_secret":"deterministic-local-bootstrap-fixture"}
EOF
cat >"$NODE_B_CFG" <<EOF
{"role":"node","node_id":"node-b","display_name":"B","listen_api":"127.0.0.1:$NODE_B_API","socks_listen":"127.0.0.1:0","enabled_carriers":["local.control","local.egress.node-b"],"carrier_configs":[{"id":"local.control","carrier_type":"file.mailbox","file_mailbox":{"dir":"$CONTROL_DIR"},"endpoint":{"id":"local.control","address":"control"}},{"id":"local.egress.node-b","carrier_type":"file.mailbox","file_mailbox":{"dir":"$NODE_B_DIR","allow_egress":true},"endpoint":{"id":"local.egress.node-b","address":"node-b"}}],"bootstrap_secret":"deterministic-local-bootstrap-fixture"}
EOF
cat >"$CLIENT_CFG" <<EOF
{"role":"client","client_id":"multinode-autoheal-client","listen_api":"127.0.0.1:$CLIENT_API","socks_listen":"127.0.0.1:$CLIENT_SOCKS","enabled_carriers":["local.control","local.egress.node-a","local.egress.node-b"],"carrier_configs":[{"id":"local.control","carrier_type":"file.mailbox","file_mailbox":{"dir":"$CONTROL_DIR"},"endpoint":{"id":"local.control","address":"control"}},{"id":"local.egress.node-a","carrier_type":"file.mailbox","file_mailbox":{"dir":"$NODE_A_DIR","allow_egress":true},"endpoint":{"id":"local.egress.node-a","address":"node-a"}},{"id":"local.egress.node-b","carrier_type":"file.mailbox","file_mailbox":{"dir":"$NODE_B_DIR","allow_egress":true},"endpoint":{"id":"local.egress.node-b","address":"node-b"}}],"bootstrap_secret":"deterministic-local-bootstrap-fixture"}
EOF
python3 - "$NODE_A_CFG" "$NODE_B_CFG" "$CLIENT_CFG" "$RUN_DIR" <<'PY' || fail 'configs are not isolated local file-mailbox fixtures'
import json, os, sys
root=os.path.realpath(sys.argv[4])
for path in sys.argv[1:4]:
    cfg=json.load(open(path))
    assert "token_store" not in cfg
    assert cfg["bootstrap_secret"] == "deterministic-local-bootstrap-fixture"
    assert all(c["carrier_type"] == "file.mailbox" for c in cfg["carrier_configs"])
    assert all(os.path.commonpath((root, os.path.realpath(c["file_mailbox"]["dir"]))) == root for c in cfg["carrier_configs"])
PY

PHASE=build
if [[ -n "${WT_TEST_BINARY:-}" ]]; then
    [[ -x "$WT_TEST_BINARY" ]] || fail 'WT_TEST_BINARY is not executable'
    cp "$WT_TEST_BINARY" "$BINARY"
else
    (cd "$GO_ROOT" && GOPROXY=off /usr/local/go/bin/go build -o "$BINARY" ./cmd/whitetransportd/)
fi

BINARY_SHA256="$(sha256sum "$BINARY" | cut -d ' ' -f 1)"
PHASE=start
"$BINARY" -config "$NODE_A_CFG" -serve >"$NODE_A_LOG" 2>&1 & NODE_A_PID=$!
"$BINARY" -config "$NODE_B_CFG" -serve >"$NODE_B_LOG" 2>&1 & NODE_B_PID=$!
unshare -Un --map-root-user sh -c 'ip link set lo up; exec "$@"' sh "$BINARY" -config "$CLIENT_CFG" -serve >"$CLIENT_LOG" 2>&1 & CLIENT_PID=$!
for _ in $(seq 1 80); do
    curl --noproxy '*' --max-time .2 -sf "http://127.0.0.1:$NODE_A_API/health" >/dev/null &&
    curl --noproxy '*' --max-time .2 -sf "http://127.0.0.1:$NODE_B_API/health" >/dev/null &&
    client_ns curl --noproxy '*' --max-time .2 -sf "http://127.0.0.1:$CLIENT_API/health" >/dev/null && break
    sleep .1
done
kill -0 "$NODE_A_PID" && kill -0 "$NODE_B_PID" && kill -0 "$CLIENT_PID" || fail 'daemon failed before health'

PHASE=target
python3 - "$HTTP_PORT" <<'PY' >"$RUN_DIR/http.log" 2>&1 &
import http.server, sys
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body=("payload-"+self.path).encode()
        self.send_response(200); self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)
    def log_message(self,*_): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
PY
HTTP_PID=$!
sleep .1
client_ns curl --noproxy '*' --max-time 1 -sf "http://127.0.0.1:$HTTP_PORT/blocked" >/dev/null 2>&1 && fail 'client namespace reached target directly'
DIRECT_FALLBACK_BLOCKED=true

PHASE=connect_a
for _ in $(seq 1 80); do
    if client_ns curl --noproxy '*' -sf "http://127.0.0.1:$CLIENT_API/v1/nodes" | python3 -c 'import json,sys; n=json.load(sys.stdin); raise SystemExit(0 if {x["node_id"] for x in n if x.get("available")} >= {"node-a","node-b"} else 1)'; then break; fi
    sleep .1
done
client_ns curl --noproxy '*' -sf -X POST "http://127.0.0.1:$CLIENT_API/v1/session/connect" -H 'Content-Type: application/json' -d '{"node_id":"node-a"}' >/dev/null || fail 'connect node A failed'
client_ns curl --noproxy '' --max-time 10 -sf -x "socks5h://127.0.0.1:$CLIENT_SOCKS" "http://127.0.0.1:$HTTP_PORT/primary" | grep -qx 'payload-/primary' || fail 'primary SOCKS payload failed'
PRIMARY_PAYLOAD=true
NODE_A_DIR_MODE="$(stat -c '%a:%u:%g' "$NODE_A_DIR")"

PHASE=fail_a
kill "$NODE_A_PID"; wait "$NODE_A_PID" 2>/dev/null || true; NODE_A_STOPPED=true
client_ns curl --noproxy '' --max-time 20 -sf -x "socks5h://127.0.0.1:$CLIENT_SOCKS" "http://127.0.0.1:$HTTP_PORT/failed-node-a" | grep -qx 'payload-/failed-node-a' || fail 'first SOCKS request was not recovered through node B'
[[ -d "$NODE_A_DIR" && "$(stat -c '%a:%u:%g' "$NODE_A_DIR")" == "$NODE_A_DIR_MODE" ]] || fail 'node A mailbox state was manipulated during liveness recovery'
NODE_A_MAILBOX_INTACT=true
for _ in $(seq 1 120); do
    status="$(client_ns curl --noproxy '*' -sf "http://127.0.0.1:$CLIENT_API/v1/status" || true)"
    if [[ -n "$status" ]] && printf '%s' "$status" | python3 -c 'import json,sys; s=json.load(sys.stdin); raise SystemExit(0 if s.get("state")=="connected" and s.get("active_node_id")=="node-b" else 1)'; then
        AUTOHEAL_OBSERVED=true
        break
    fi
    sleep .1
done
[[ "$AUTOHEAL_OBSERVED" == true ]] || fail 'client did not automatically reselect node B'

PHASE=payload_b
client_ns curl --noproxy '' --max-time 10 -sf -x "socks5h://127.0.0.1:$CLIENT_SOCKS" "http://127.0.0.1:$HTTP_PORT/backup" | grep -qx 'payload-/backup' || fail 'backup SOCKS payload failed'
BACKUP_PAYLOAD=true
SELECTED_NODE_ID=node-b

PHASE=total_outage
kill "$NODE_B_PID"; wait "$NODE_B_PID" 2>/dev/null || true
NODE_B_PID=""
if client_ns curl --noproxy '' --max-time 18 -sf -x "socks5h://127.0.0.1:$CLIENT_SOCKS" "http://127.0.0.1:$HTTP_PORT/all-offline" >"$RUN_DIR/outage-payload" 2>/dev/null; then
    fail 'SOCKS payload succeeded while every node was stopped'
fi
OUTAGE_OBSERVED=true

PHASE=background_recovery
RECOVERY_STARTED_MS="$(date +%s%3N)"
"$BINARY" -config "$NODE_A_CFG" -serve >>"$NODE_A_LOG" 2>&1 & NODE_A_PID=$!
# Status reads cannot establish a session. There is deliberately no Connect or
# SOCKS request between restoring the node and observing the replacement.
for _ in $(seq 1 450); do
    status="$(client_ns curl --noproxy '*' --max-time 1 -sf "http://127.0.0.1:$CLIENT_API/v1/status" || true)"
    if [[ -n "$status" ]] && printf '%s' "$status" | python3 -c 'import json,sys; s=json.load(sys.stdin); raise SystemExit(0 if s.get("state")=="connected" and s.get("active_node_id")=="node-a" else 1)'; then
        BACKGROUND_RECOVERED=true
        RECOVERY_LATENCY_MS="$(($(date +%s%3N)-RECOVERY_STARTED_MS))"
        break
    fi
    sleep .1
done
[[ "$BACKGROUND_RECOVERED" == true ]] || fail 'session did not recover in background after total outage'

PHASE=restored_payload
client_ns curl --noproxy '' --max-time 10 -sf -x "socks5h://127.0.0.1:$CLIENT_SOCKS" "http://127.0.0.1:$HTTP_PORT/restored" | grep -qx 'payload-/restored' || fail 'background session did not transfer restored payload'
RESTORED_PAYLOAD=true
SELECTED_NODE_ID=node-a

PHASE=disconnect_during_outage
kill "$NODE_A_PID"; wait "$NODE_A_PID" 2>/dev/null || true
NODE_A_PID=""
if client_ns curl --noproxy '' --max-time 18 -sf -x "socks5h://127.0.0.1:$CLIENT_SOCKS" "http://127.0.0.1:$HTTP_PORT/offline-again" >/dev/null 2>&1; then
    fail 'SOCKS unexpectedly succeeded in second total outage'
fi
client_ns curl --noproxy '*' --max-time 3 -sf -X POST "http://127.0.0.1:$CLIENT_API/v1/session/disconnect" --data '' >/dev/null || fail 'disconnect during recovery did not complete promptly'
"$BINARY" -config "$NODE_B_CFG" -serve >>"$NODE_B_LOG" 2>&1 & NODE_B_PID=$!
# Observe more than two normal recovery ticks after the node returns.
for _ in $(seq 1 310); do
    client_ns curl --noproxy '*' --max-time 1 -sf "http://127.0.0.1:$CLIENT_API/v1/status" | python3 -c 'import json,sys; s=json.load(sys.stdin); raise SystemExit(0 if s.get("state")=="disconnected" and not s.get("session_active") else 1)' || fail 'explicitly disconnected client resumed recovery'
    sleep .1
done
if client_ns curl --noproxy '' --max-time 3 -sf -x "socks5h://127.0.0.1:$CLIENT_SOCKS" "http://127.0.0.1:$HTTP_PORT/after-disconnect" >/dev/null 2>&1; then
    fail 'explicitly disconnected client resumed traffic'
fi
DISCONNECT_RESPECTED=true
SUCCEEDED=true
echo '[*] Multi-node auto-heal local integration passed'
