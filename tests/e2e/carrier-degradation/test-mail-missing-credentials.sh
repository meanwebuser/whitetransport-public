#!/usr/bin/env bash
# Actual-process matrix: each missing Mail credential is isolated while file control+egress remains usable.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
RUN_ROOT="$(mktemp -d /tmp/wt-mail-missing-credentials.XXXXXX)"
BIN="$RUN_ROOT/whitetransportd"
RESULTS="$RUN_ROOT/results.jsonl"
CLIENT_PID="" NODE_WRAPPER_PID="" NODE_DAEMON_PID="" SUCCESS=false

reserve_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }
wait_http() { local url="$1"; for _ in $(seq 1 240); do curl --noproxy '*' --max-time 1 -sf "$url" >/dev/null 2>&1 && return 0; sleep .1; done; return 1; }
wait_pidfile() { local path="$1"; for _ in $(seq 1 160); do [[ -s "$path" ]] && kill -0 "$(<"$path")" 2>/dev/null && return 0; sleep .1; done; return 1; }

stop_case() {
    local pid
    for pid in "$CLIENT_PID" "$NODE_WRAPPER_PID"; do [[ -z "$pid" ]] || kill "$pid" 2>/dev/null || true; done
    for pid in "$CLIENT_PID" "$NODE_WRAPPER_PID"; do [[ -z "$pid" ]] || wait "$pid" 2>/dev/null || true; done
    CLIENT_PID="" NODE_WRAPPER_PID="" NODE_DAEMON_PID=""
}

cleanup() {
    local status=$? destination
    set +e
    stop_case
    if [[ "$SUCCESS" == true && "$status" -eq 0 ]]; then
        rm -rf "$RUN_ROOT"
    else
        mkdir -p "$REPO_ROOT/trash/logs"
        destination="$REPO_ROOT/trash/logs/mail-missing-credentials-failed-$(date +%s)"
        mv "$RUN_ROOT" "$destination" 2>/dev/null || true
        printf 'Mail missing-credential artifacts: %s\n' "$destination" >&2
        status=1
    fi
    exit "$status"
}
trap cleanup EXIT

unshare -Un --map-root-user true >/dev/null 2>&1
(cd "$REPO_ROOT/core/go" && GOPROXY=off /usr/local/go/bin/go build -o "$BIN" ./cmd/whitetransportd)

for missing in smtp_username smtp_password imap_username imap_password; do
    CASE_DIR="$RUN_ROOT/$missing"
    NODE_CFG="$CASE_DIR/node.json" CLIENT_CFG="$CASE_DIR/client.json"
    NODE_LOG="$CASE_DIR/node.log" CLIENT_LOG="$CASE_DIR/client.log"
    CONTROL_DIR="$CASE_DIR/file-control" EGRESS_DIR="$CASE_DIR/file-egress"
    NODE_API="$(reserve_port)" CLIENT_API="$(reserve_port)"
    NODE_SOCKS="$(reserve_port)" CLIENT_SOCKS="$(reserve_port)" HTTP_PORT="$(reserve_port)"
    NONCE="file-egress-${missing}-${RANDOM}-${RANDOM}"
    mkdir -p "$CONTROL_DIR" "$EGRESS_DIR"

    python3 - "$NODE_CFG" "$CLIENT_CFG" "$CASE_DIR" "$NODE_API" "$CLIENT_API" "$NODE_SOCKS" "$CLIENT_SOCKS" "$missing" <<'PY'
import json, os, sys

node_path, client_path, root, node_api, client_api, node_socks, client_socks, missing = sys.argv[1:]
parts = {
    "smtp_username": "fixture-smtp-user",
    "smtp_password": "fixture-smtp-pass",
    "imap_username": "fixture-imap-user",
    "imap_password": "fixture-imap-pass",
}
del parts[missing]
store = {
    "tokens": [
        {"id": "local-session", "platform": "local", "kind": "api_key", "lifecycle": "embedded", "status": "active", "value": "deterministic-local-session-key-not-a-secret"},
        {"id": "mail-account-a", "platform": "mail", "kind": "composite", "lifecycle": "embedded", "status": "active", "parts": parts},
    ],
    "bindings": [
        {"token_id": "local-session", "platform": "local", "connection_type": "messages", "channel_id": "control", "role": "discovery", "priority": 10, "enabled": True},
        {"token_id": "mail-account-a", "platform": "mail", "connection_type": "imap_smtp", "channel_id": "account-a", "role": "egress", "priority": 10, "enabled": True},
    ],
}
mail = {
    "smtp_address": "127.0.0.1:1", "imap_address": "127.0.0.1:1", "account_id": "account-a",
    "mailbox": "INBOX", "from_address": "sender@example.test", "to_address": "receiver@example.test",
    "tls_server_name": "mail.fixture.test", "ca_file": "/nonexistent/mail-ca.pem", "timeout_seconds": 1,
}
carriers = [
    {"id": "file.control", "carrier_type": "file.mailbox", "role": "discovery", "endpoint": {"id": "file.control", "address": "control"}, "file_mailbox": {"dir": os.path.join(root, "file-control")}},
    {"id": "mail.primary", "carrier_type": "mail.imap_smtp", "role": "egress", "endpoint": {"id": "mail.primary", "address": "mail-egress"}, "mail_imap_smtp": mail},
    {"id": "file.egress", "carrier_type": "file.mailbox", "role": "egress", "endpoint": {"id": "file.egress", "address": "file-egress"}, "file_mailbox": {"dir": os.path.join(root, "file-egress"), "allow_egress": True}},
]
common = {
    "enabled_carriers": ["file.control", "mail.primary", "file.egress"],
    "carrier_configs": carriers, "token_store": store,
    "upstream_proxy": {"url": "", "client_egress_only": True, "apply_to_carriers": False},
}
node = {**common, "role": "node", "node_id": f"mail-missing-{missing}-node", "listen_api": f"127.0.0.1:{node_api}", "socks_listen": f"127.0.0.1:{node_socks}", "state_file": os.path.join(root, "node-state.json")}
client = {**common, "role": "client", "client_id": f"mail-missing-{missing}-client", "listen_api": f"127.0.0.1:{client_api}", "socks_listen": f"127.0.0.1:{client_socks}", "state_file": os.path.join(root, "client-state.json")}
for path, config in ((node_path, node), (client_path, client)):
    with open(path, "w", encoding="utf-8") as output:
        json.dump(config, output, separators=(",", ":"))
PY

    CLIENT_CFG_DIGEST="$(sha256sum "$CLIENT_CFG" | awk '{print $1}')"
    NODE_CFG_DIGEST="$(sha256sum "$NODE_CFG" | awk '{print $1}')"
    env WT_BIN="$BIN" WT_CFG="$NODE_CFG" WT_LOG="$NODE_LOG" WT_PIDFILE="$CASE_DIR/node-daemon.pid" \
        WT_FIXTURE="$SCRIPT_DIR/nonce_http_fixture.py" WT_HTTP_PORT="$HTTP_PORT" WT_NONCE="$NONCE" \
        unshare -Un --map-root-user bash -c '
set -Eeuo pipefail
ip link set lo up
python3 "$WT_FIXTURE" --port "$WT_HTTP_PORT" --nonce "$WT_NONCE" & hp=$!
WT_DEBUG=1 "$WT_BIN" -config "$WT_CFG" -serve >"$WT_LOG" 2>&1 & dp=$!
printf "%s\n" "$dp" >"$WT_PIDFILE"
cleanup_inner(){ kill "$dp" "$hp" 2>/dev/null || true; wait "$dp" "$hp" 2>/dev/null || true; }
trap cleanup_inner EXIT TERM INT
while kill -0 "$dp" 2>/dev/null; do wait "$dp" 2>/dev/null || true; done
' >"$CASE_DIR/node-wrapper.log" 2>&1 &
    NODE_WRAPPER_PID=$!
    wait_pidfile "$CASE_DIR/node-daemon.pid"
    NODE_DAEMON_PID="$(<"$CASE_DIR/node-daemon.pid")"

    WT_DEBUG=1 "$BIN" -config "$CLIENT_CFG" -serve >"$CLIENT_LOG" 2>&1 &
    CLIENT_PID=$!
    wait_http "http://127.0.0.1:${CLIENT_API}/health"
    CLIENT_START_TICKS="$(awk '{print $22}' "/proc/$CLIENT_PID/stat")"
    NODE_START_TICKS="$(awk '{print $22}' "/proc/$NODE_DAEMON_PID/stat")"

    curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API}/v1/carriers" | python3 -c '
import json,sys
c=json.load(sys.stdin)
for carrier in ("file.control","file.egress"):
    row=c[carrier]
    assert row.get("healthy") is True and row.get("lifecycle_state")=="constructed",(carrier,row)
mail=c["mail.primary"]
assert mail.get("healthy") is False and mail.get("lifecycle_state")=="degraded",mail
assert mail.get("failure_stage")=="construction" and mail.get("error_code")=="credential_missing" and mail.get("retryable") is True,mail
assert [key for key,row in c.items() if row.get("error_code")=="credential_missing"]==["mail.primary"],c
'

    NODE_ID="mail-missing-${missing}-node"
    for _ in $(seq 1 160); do
        curl --noproxy '*' --max-time 2 -sf "http://127.0.0.1:${CLIENT_API}/v1/nodes" \
          | python3 -c 'import json,sys; target=sys.argv[1]; raise SystemExit(0 if any(n.get("node_id")==target and n.get("available") for n in json.load(sys.stdin)) else 1)' "$NODE_ID" 2>/dev/null && break
        sleep .1
    done
    curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API}/v1/nodes" \
      | python3 -c 'import json,sys; target=sys.argv[1]; assert any(n.get("node_id")==target and n.get("available") for n in json.load(sys.stdin))' "$NODE_ID"
    curl --noproxy '*' -sf -X POST "http://127.0.0.1:${CLIENT_API}/v1/session/connect" \
      -H 'Content-Type: application/json' -d "{\"node_id\":\"${NODE_ID}\"}" >/dev/null
    for _ in $(seq 1 300); do
        curl --noproxy '*' --max-time 2 -sf "http://127.0.0.1:${CLIENT_API}/v1/status" \
          | python3 -c 'import json,sys; s=json.load(sys.stdin); raise SystemExit(0 if s.get("state")=="connected" and s.get("automatic_egress_endpoint_id")=="file.egress" else 1)' 2>/dev/null && break
        sleep .1
    done
    curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API}/v1/status" \
      | python3 -c 'import json,sys; s=json.load(sys.stdin); assert s.get("state")=="connected" and s.get("automatic_egress_endpoint_id")=="file.egress" and not s.get("selected_egress_endpoint_id"),s'

    if curl --noproxy '*' --max-time 1 -sf "http://127.0.0.1:${HTTP_PORT}/nonce" >/dev/null 2>&1; then
        printf 'isolated target reachable directly for %s\n' "$missing" >&2
        exit 1
    fi
    BODY="$(curl --noproxy '' --max-time 45 -sf -x "socks5h://127.0.0.1:${CLIENT_SOCKS}" "http://127.0.0.1:${HTTP_PORT}/nonce")"
    [[ "$BODY" == "$NONCE" ]]
    grep -q 'socks5 connected .*route=file.egress' "$CLIENT_LOG"
    [[ "$(awk '{print $22}' "/proc/$CLIENT_PID/stat")" == "$CLIENT_START_TICKS" ]]
    [[ "$(awk '{print $22}' "/proc/$NODE_DAEMON_PID/stat")" == "$NODE_START_TICKS" ]]
    [[ "$(sha256sum "$CLIENT_CFG" | awk '{print $1}')" == "$CLIENT_CFG_DIGEST" ]]
    [[ "$(sha256sum "$NODE_CFG" | awk '{print $1}')" == "$NODE_CFG_DIGEST" ]]
    wait_http "http://127.0.0.1:${CLIENT_API}/health"

    python3 - "$RESULTS" "$missing" <<'PY'
import json, sys
with open(sys.argv[1], "a", encoding="utf-8") as output:
    output.write(json.dumps({
        "missingCredential": sys.argv[2], "daemonAlive": True, "processIdentityUnchanged": True,
        "configUnchanged": True, "controlRoute": "file.control", "egressRoute": "file.egress",
        "mailBinding": "mail.primary", "mailFailureCode": "credential_missing", "payloadVerified": True,
        "directTargetReachable": False, "manualSelectionUsed": False,
    }, separators=(",", ":")) + "\n")
PY

    OLD_CLIENT_PID="$CLIENT_PID" OLD_NODE_PID="$NODE_DAEMON_PID"
    stop_case
    ! kill -0 "$OLD_CLIENT_PID" 2>/dev/null
    ! kill -0 "$OLD_NODE_PID" 2>/dev/null
    if ss -ltnH 2>/dev/null | grep -Eq ":(${CLIENT_API}|${CLIENT_SOCKS})[[:space:]]"; then
        printf 'listener leaked after %s\n' "$missing" >&2
        exit 1
    fi
done

python3 - "$RESULTS" <<'PY'
import json, sys
cases=[json.loads(line) for line in open(sys.argv[1], encoding="utf-8") if line.strip()]
assert [case["missingCredential"] for case in cases] == ["smtp_username", "smtp_password", "imap_username", "imap_password"]
print(json.dumps({"test":"carrier-mail-missing-credentials","exit":0,"cases":cases,"cleanupVerified":True},separators=(",",":")))
PY
SUCCESS=true
