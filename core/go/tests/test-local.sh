#!/usr/bin/env bash
# Deterministic two-daemon SOCKS5 primary-to-backup integration proof.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
GO_ROOT="$REPO_ROOT/core/go"
RUN_DIR="$(mktemp -d /tmp/wt-local-failover.XXXXXX)"
BINARY="$RUN_DIR/whitetransportd"
NODE_LOG="$RUN_DIR/node.log"
CLIENT_LOG="$RUN_DIR/client.log"
NODE_PID="" CLIENT_PID="" HTTP_PID="" UDP_PID=""
SUCCEEDED=false PHASE=setup ERRORS=()
PRIMARY_ROUTE="" BACKUP_ROUTE="" PRIMARY_NONCE_VALID=false BACKUP_NONCE_VALID=false
PRIMARY_FAILURE_OBSERVED=false PRIMARY_FRAME_ARTIFACTS=0 BACKUP_FRAME_ARTIFACTS=0
BINARY_SHA256="" BINARY_SOURCE="local-build"
PRIMARY_FAILURE_EVIDENCE="" CONFIG_ISOLATION_VERIFIED=false
UDP_NONCE_VALID=false UDP_EXIT_RESOLVER_INVOCATIONS=0 UDP_CIPHERTEXT_CONFIRMED=false UDP_FRAME_ARTIFACTS=0 UDP_ENCRYPTED_FRAMES=0 UDP_PLAINTEXT_FRAMES=0
GIT_COMMIT="$(git -C "$REPO_ROOT" rev-parse HEAD)"
GIT_DIRTY=false
[[ -z "$(git -C "$REPO_ROOT" status --porcelain --untracked-files=normal)" ]] || GIT_DIRTY=true
START_MS="$(date +%s%3N)"

reserve_port() {
    python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}
record_error() { local text="${1//$'\n'/ }"; ERRORS+=("${PHASE}:${text:0:4000}"); }
fail() { record_error "$1"; exit 1; }

NODE_API_PORT="$(reserve_port)"
CLIENT_API_PORT="$(reserve_port)"
NODE_SOCKS_PORT="$(reserve_port)"
CLIENT_SOCKS_PORT="$(reserve_port)"
HTTP_PORT="$(reserve_port)"
UDP_PORT="$(reserve_port)"
CONTROL_DIR="$RUN_DIR/control"
PRIMARY_DIR="$RUN_DIR/primary"
BACKUP_DIR="$RUN_DIR/backup"
NODE_CFG="$RUN_DIR/node.json"
CLIENT_CFG="$RUN_DIR/client.json"
PRIMARY_NONCE="primary-${RANDOM}-${RANDOM}"
BACKUP_NONCE="backup-${RANDOM}-${RANDOM}"

emit_json() {
    local status="$1" elapsed error_lines
    elapsed=$(($(date +%s%3N)-START_MS))
    error_lines="$(printf '%s\n' "${ERRORS[@]:-}")"
    STATUS="$status" ELAPSED="$elapsed" ERROR_LINES="$error_lines" PRIMARY_ROUTE="$PRIMARY_ROUTE" BACKUP_ROUTE="$BACKUP_ROUTE" \
    PRIMARY_NONCE_VALID="$PRIMARY_NONCE_VALID" BACKUP_NONCE_VALID="$BACKUP_NONCE_VALID" PRIMARY_FAILURE_OBSERVED="$PRIMARY_FAILURE_OBSERVED" \
    PRIMARY_FRAME_ARTIFACTS="$PRIMARY_FRAME_ARTIFACTS" BACKUP_FRAME_ARTIFACTS="$BACKUP_FRAME_ARTIFACTS" \
    PRIMARY_FAILURE_EVIDENCE="$PRIMARY_FAILURE_EVIDENCE" CONFIG_ISOLATION_VERIFIED="$CONFIG_ISOLATION_VERIFIED" \
    UDP_NONCE_VALID="$UDP_NONCE_VALID" UDP_EXIT_RESOLVER_INVOCATIONS="$UDP_EXIT_RESOLVER_INVOCATIONS" UDP_CIPHERTEXT_CONFIRMED="$UDP_CIPHERTEXT_CONFIRMED" \
    UDP_FRAME_ARTIFACTS="$UDP_FRAME_ARTIFACTS" UDP_ENCRYPTED_FRAMES="$UDP_ENCRYPTED_FRAMES" UDP_PLAINTEXT_FRAMES="$UDP_PLAINTEXT_FRAMES" \
    GIT_COMMIT="$GIT_COMMIT" GIT_DIRTY="$GIT_DIRTY" BINARY_SHA256="$BINARY_SHA256" BINARY_SOURCE="$BINARY_SOURCE" python3 - <<'PY'
import json, os
print(json.dumps({
    "test":"desktop-local-fast", "proofLevel":"local-integration", "transport":"file.mailbox",
    "daemonCount":2, "productionCredentialsUsed":False, "providerEndpointsConfigured":False,
    "configIsolationVerified":os.environ["CONFIG_ISOLATION_VERIFIED"]=="true", "socksStrict":True,
    "binarySha256":os.environ["BINARY_SHA256"] or None, "binarySource":os.environ["BINARY_SOURCE"],
    "gitCommit":os.environ["GIT_COMMIT"], "gitDirty":os.environ["GIT_DIRTY"]=="true",
    "primaryRoute":os.environ["PRIMARY_ROUTE"] or None, "backupRoute":os.environ["BACKUP_ROUTE"] or None,
    "primaryFailureObserved":os.environ["PRIMARY_FAILURE_OBSERVED"]=="true",
    "primaryFailureEvidence":os.environ["PRIMARY_FAILURE_EVIDENCE"] or None,
    "primaryNonceValid":os.environ["PRIMARY_NONCE_VALID"]=="true",
    "backupNonceValid":os.environ["BACKUP_NONCE_VALID"]=="true",
    "primaryFrameArtifacts":int(os.environ["PRIMARY_FRAME_ARTIFACTS"]),
    "backupFrameArtifacts":int(os.environ["BACKUP_FRAME_ARTIFACTS"]),
    "udpNonceValid":os.environ["UDP_NONCE_VALID"]=="true",
    "udpExitResolverInvocations":int(os.environ["UDP_EXIT_RESOLVER_INVOCATIONS"]),
    "udpCiphertextConfirmed":os.environ["UDP_CIPHERTEXT_CONFIRMED"]=="true",
    "udpFrameArtifacts":int(os.environ["UDP_FRAME_ARTIFACTS"]),
    "udpEncryptedFrames":int(os.environ["UDP_ENCRYPTED_FRAMES"]),
    "udpPlaintextFrames":int(os.environ["UDP_PLAINTEXT_FRAMES"]),
    "latencyMs":int(os.environ["ELAPSED"]), "exit":int(os.environ["STATUS"]),
    "phaseErrors":[line for line in os.environ["ERROR_LINES"].splitlines() if line],
},separators=(",",":")))
PY
}

cleanup() {
    local status=$?
    trap - EXIT ERR
    PHASE=cleanup
    if [[ "$SUCCEEDED" != true ]]; then
        [[ -f "$NODE_LOG" ]] && { echo "[*] Node log after failure:"; tail -n 80 "$NODE_LOG"; }
        [[ -f "$CLIENT_LOG" ]] && { echo "[*] Client log after failure:"; tail -n 80 "$CLIENT_LOG"; }
    fi
    curl -sf -X POST "http://127.0.0.1:${CLIENT_API_PORT}/v1/session/disconnect" >/dev/null 2>&1 || true
    [[ -z "$NODE_PID" ]] || kill "$NODE_PID" 2>/dev/null || true
    [[ -z "$CLIENT_PID" ]] || kill "$CLIENT_PID" 2>/dev/null || true
    [[ -z "$HTTP_PID" ]] || kill "$HTTP_PID" 2>/dev/null || true
    [[ -z "$UDP_PID" ]] || kill "$UDP_PID" 2>/dev/null || true
    chmod -R u+rwX "$RUN_DIR" 2>/dev/null || true
    rm -rf "$RUN_DIR"
    emit_json "$status"
    exit "$status"
}
trap cleanup EXIT
trap 'record_error "unexpected command failure at line $LINENO"' ERR

render_config() {
    local input="$1" output="$2"
    sed \
        -e "s|__NODE_API_PORT__|$NODE_API_PORT|g" \
        -e "s|__CLIENT_API_PORT__|$CLIENT_API_PORT|g" \
        -e "s|__NODE_SOCKS_PORT__|$NODE_SOCKS_PORT|g" \
        -e "s|__CLIENT_SOCKS_PORT__|$CLIENT_SOCKS_PORT|g" \
        -e "s|__CONTROL_DIR__|$CONTROL_DIR|g" \
        -e "s|__PRIMARY_DIR__|$PRIMARY_DIR|g" \
        -e "s|__BACKUP_DIR__|$BACKUP_DIR|g" \
        "$input" > "$output"
}

mailbox_line_count() {
    local directory="$1"
    [[ -d "$directory" ]] || { echo 0; return; }
    find "$directory" -maxdepth 1 -type f -name '*.jsonl' -exec cat {} + 2>/dev/null | wc -l | tr -d ' '
}

packet_frame_counts() {
    local directory="$1"
    python3 - "$directory" <<'PY'
import glob, json, os, sys
total = encrypted = plaintext = 0
for path in glob.glob(os.path.join(sys.argv[1], "*.jsonl")):
    with open(path, encoding="utf-8") as stream:
        for line in stream:
            try:
                envelope = json.loads(line)
            except json.JSONDecodeError:
                continue
            if not str(envelope.get("id", "")).startswith("udp-"):
                continue
            total += 1
            payload_type = envelope.get("payload_type", "")
            if payload_type == "encrypted":
                encrypted += 1
            elif payload_type.startswith("tunnel.packet."):
                plaintext += 1
print(total, encrypted, plaintext)
PY
}

disable_mailbox() {
    local directory="$1"
    chmod 0500 "$directory"
    find "$directory" -maxdepth 1 -type f -name '*.jsonl' -exec chmod 0400 {} +
}

enable_mailbox() {
    local directory="$1"
    chmod 0700 "$directory"
    find "$directory" -maxdepth 1 -type f -name '*.jsonl' -exec chmod 0600 {} +
}

wait_for_route() {
    local route="$1"
    for _ in $(seq 1 30); do
        if grep -q "socks5 connected .*route=$route" "$CLIENT_LOG"; then
            printf '%s' "$route"
            return 0
        fi
        sleep .1
    done
    return 1
}

probe_nonce() {
    local path="$1"
    curl --noproxy '' --max-time 8 -sf -x "socks5h://127.0.0.1:${CLIENT_SOCKS_PORT}" "http://127.0.0.1:${HTTP_PORT}/$path"
}

PHASE=config
render_config "$REPO_ROOT/config/dev/local-failover-node.json.template" "$NODE_CFG"
render_config "$REPO_ROOT/config/dev/local-failover-client.json.template" "$CLIENT_CFG"
python3 - "$RUN_DIR" "$NODE_CFG" "$CLIENT_CFG" <<'PY' || fail 'generated configs are not isolated local file-mailbox configs'
import json, os, sys
run_dir=os.path.realpath(sys.argv[1])
for path in sys.argv[2:]:
    cfg=json.load(open(path, encoding="utf-8"))
    store=cfg.get("token_store", {})
    assert store.get("tokens") == [{
        "id":"local-test-bootstrap", "platform":"local", "kind":"api_key",
        "lifecycle":"embedded", "status":"active", "value":"deterministic-local-session-key-not-a-secret",
    }]
    assert store.get("bindings") == [{
        "token_id":"local-test-bootstrap", "platform":"local", "connection_type":"messages",
        "channel_id":"control", "role":"discovery", "priority":10, "enabled":True,
    }]
    assert not cfg.get("admin_relay", {}).get("enabled", False)
    assert not cfg.get("upstream_proxy", {}).get("url")
    carriers=cfg.get("carrier_configs", [])
    assert carriers and all(item.get("carrier_type")=="file.mailbox" for item in carriers)
    for item in carriers:
        mailbox=os.path.realpath(item["file_mailbox"]["dir"])
        assert os.path.commonpath((run_dir, mailbox)) == run_dir
PY
CONFIG_ISOLATION_VERIFIED=true
export WT_DEBUG=1

PHASE=build
if [[ -n "${WT_LOCAL_DAEMON_BINARY:-}" ]]; then
    [[ -f "$WT_LOCAL_DAEMON_BINARY" && -x "$WT_LOCAL_DAEMON_BINARY" ]] || fail 'WT_LOCAL_DAEMON_BINARY must name an executable file'
    echo "[*] Copying exact prebuilt daemon into isolated run directory..."
    install -m 0700 "$WT_LOCAL_DAEMON_BINARY" "$BINARY"
    BINARY_SOURCE=prebuilt
else
    echo "[*] Building daemon with external module access disabled..."
    (cd "$GO_ROOT" && GOPROXY=off /usr/local/go/bin/go build -o "$BINARY" ./cmd/whitetransportd/)
fi
BINARY_SHA256="$(sha256sum "$BINARY" | awk '{print $1}')"

PHASE=start_node
"$BINARY" -config "$NODE_CFG" -serve > "$NODE_LOG" 2>&1 &
NODE_PID=$!
sleep 1
kill -0 "$NODE_PID" 2>/dev/null || fail 'node daemon exited during startup'

PHASE=start_client
"$BINARY" -config "$CLIENT_CFG" -serve > "$CLIENT_LOG" 2>&1 &
CLIENT_PID=$!
sleep .2
kill -0 "$CLIENT_PID" 2>/dev/null || fail 'client daemon exited during startup'

PHASE=discovery
for _ in $(seq 1 60); do
    if curl -sf "http://127.0.0.1:${CLIENT_API_PORT}/v1/nodes" | python3 -c 'import json,sys; raise SystemExit(0 if any(n.get("node_id")=="local-failover-node" and n.get("available") for n in json.load(sys.stdin)) else 1)'; then
        break
    fi
    sleep .1
done
curl -sf "http://127.0.0.1:${CLIENT_API_PORT}/v1/nodes" | python3 -c 'import json,sys; raise SystemExit(0 if any(n.get("node_id")=="local-failover-node" and n.get("available") for n in json.load(sys.stdin)) else 1)' || fail 'local node was not discovered'

PHASE=connect
curl --max-time 15 -sf -X POST "http://127.0.0.1:${CLIENT_API_PORT}/v1/session/connect" -H 'Content-Type: application/json' -d '{"node_id":"local-failover-node"}' >/dev/null || fail 'session connect failed'
for _ in $(seq 1 60); do
    if curl -sf "http://127.0.0.1:${CLIENT_API_PORT}/v1/status" | python3 -c 'import json,sys; raise SystemExit(0 if json.load(sys.stdin).get("state")=="connected" else 1)'; then
        break
    fi
    sleep .1
done
curl -sf "http://127.0.0.1:${CLIENT_API_PORT}/v1/status" | python3 -c 'import json,sys; raise SystemExit(0 if json.load(sys.stdin).get("state")=="connected" else 1)' || fail 'client did not reach connected state'

PHASE=http_target
python3 - "$HTTP_PORT" "$PRIMARY_NONCE" "$BACKUP_NONCE" <<'PY' >"$RUN_DIR/http.log" 2>&1 &
import http.server, sys
responses={"/primary":sys.argv[2].encode(), "/backup":sys.argv[3].encode()}
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body=responses.get(self.path)
        if body is None: self.send_error(404); return
        self.send_response(200); self.send_header("Content-Length",str(len(body))); self.end_headers(); self.wfile.write(body)
    def log_message(self,*_args): pass
http.server.HTTPServer(("127.0.0.1",int(sys.argv[1])),Handler).serve_forever()
PY
HTTP_PID=$!
sleep .2
kill -0 "$HTTP_PID" 2>/dev/null || fail 'nonce HTTP target failed to start'

PHASE=udp_target
python3 - "$UDP_PORT" <<'PY' >"$RUN_DIR/udp.log" 2>&1 &
import select, socket, sys
port = int(sys.argv[1])
sockets = []
ipv4 = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
ipv4.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
ipv4.bind(("127.0.0.1", port))
sockets.append(ipv4)
try:
    ipv6 = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM)
    ipv6.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
    ipv6.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    ipv6.bind(("::1", port))
    sockets.append(ipv6)
except OSError:
    pass
while True:
    readable, _, _ = select.select(sockets, [], [])
    for sock in readable:
        payload, address = sock.recvfrom(65535)
        sock.sendto(payload, address)
PY
UDP_PID=$!
sleep .2
kill -0 "$UDP_PID" 2>/dev/null || fail 'nonce UDP target failed to start'

PHASE=primary_baseline
disable_mailbox "$BACKUP_DIR"
primary_before="$(mailbox_line_count "$PRIMARY_DIR")"
primary_result="$(probe_nonce primary)" || fail 'primary SOCKS payload failed'
[[ "$primary_result" == "$PRIMARY_NONCE" ]] || fail "primary payload mismatch: $primary_result"
PRIMARY_NONCE_VALID=true
PRIMARY_ROUTE="$(wait_for_route local.egress.primary)" || fail 'client log did not record primary route'
primary_after="$(mailbox_line_count "$PRIMARY_DIR")"
PRIMARY_FRAME_ARTIFACTS=$((primary_after-primary_before))
(( PRIMARY_FRAME_ARTIFACTS > 0 )) || fail 'primary mailbox recorded no payload frames'

PHASE=udp_primary
UDP_NONCE="udp-primary-${RANDOM}-${RANDOM}"
read -r udp_total_before udp_encrypted_before udp_plaintext_before < <(packet_frame_counts "$PRIMARY_DIR")
udp_exit_resolver_before="$(grep -c 'packet exit resolver success' "$NODE_LOG" || true)"
udp_result="$(python3 - "$CLIENT_SOCKS_PORT" "$UDP_PORT" "$UDP_NONCE" <<'PY'
import socket, struct, sys

def recv_exact(sock, size):
    data = b""
    while len(data) < size:
        chunk = sock.recv(size - len(data))
        if not chunk:
            raise RuntimeError("SOCKS control connection closed")
        data += chunk
    return data

socks_port, target_port, nonce = int(sys.argv[1]), int(sys.argv[2]), sys.argv[3].encode()
dns_payload = b"\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00" + nonce
udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
udp.bind(("127.0.0.1", 0))
udp.settimeout(8)
udp_host, udp_port = udp.getsockname()
control = socket.create_connection(("127.0.0.1", socks_port), timeout=8)
control.sendall(b"\x05\x01\x00")
if recv_exact(control, 2) != b"\x05\x00":
    raise RuntimeError("SOCKS no-auth negotiation failed")
control.sendall(b"\x05\x03\x00\x01" + socket.inet_aton(udp_host) + struct.pack("!H", udp_port))
reply = recv_exact(control, 4)
if reply[1] != 0:
    raise RuntimeError(f"UDP ASSOCIATE failed: {reply[1]}")
if reply[3] == 1:
    relay_host = socket.inet_ntoa(recv_exact(control, 4))
elif reply[3] == 3:
    length = recv_exact(control, 1)[0]
    relay_host = recv_exact(control, length).decode()
elif reply[3] == 4:
    relay_host = socket.inet_ntop(socket.AF_INET6, recv_exact(control, 16))
else:
    raise RuntimeError(f"unsupported relay address type: {reply[3]}")
relay_port = struct.unpack("!H", recv_exact(control, 2))[0]
domain = b"localhost"
frame = b"\x00\x00\x00\x03" + bytes([len(domain)]) + domain + struct.pack("!H", target_port) + dns_payload
udp.sendto(frame, (relay_host, relay_port))
response, _ = udp.recvfrom(65535)
if len(response) < 4 or response[:3] != b"\x00\x00\x00":
    raise RuntimeError("invalid SOCKS UDP response framing")
if response[3] == 1:
    source_host = socket.inet_ntoa(response[4:8]); offset = 8
elif response[3] == 3:
    length = response[4]; source_host = response[5:5+length].decode(); offset = 5 + length
elif response[3] == 4:
    source_host = socket.inet_ntop(socket.AF_INET6, response[4:20]); offset = 20
else:
    raise RuntimeError(f"unsupported source address type: {response[3]}")
source_port = struct.unpack("!H", response[offset:offset+2])[0]
payload = response[offset+2:]
if payload != dns_payload:
    raise RuntimeError(f"UDP nonce mismatch: {payload!r}")
print(f"udp-ok:{source_host}:{source_port}")
control.close(); udp.close()
PY
)" || fail 'primary UDP SOCKS payload failed'
[[ "$udp_result" == udp-ok:* ]] || fail "unexpected UDP result: $udp_result"
UDP_NONCE_VALID=true
udp_exit_resolver_after="$(grep -c 'packet exit resolver success' "$NODE_LOG" || true)"
UDP_EXIT_RESOLVER_INVOCATIONS=$((udp_exit_resolver_after-udp_exit_resolver_before))
(( UDP_EXIT_RESOLVER_INVOCATIONS > 0 )) || fail 'node exit resolver counter did not observe the SOCKS domain destination'
if grep -q 'packet exit resolver success' "$CLIENT_LOG"; then
    fail 'client emitted an exit-only packet resolver counter'
fi
read -r udp_total_after udp_encrypted_after udp_plaintext_after < <(packet_frame_counts "$PRIMARY_DIR")
UDP_FRAME_ARTIFACTS=$((udp_total_after-udp_total_before))
UDP_ENCRYPTED_FRAMES=$((udp_encrypted_after-udp_encrypted_before))
UDP_PLAINTEXT_FRAMES=$((udp_plaintext_after-udp_plaintext_before))
(( UDP_FRAME_ARTIFACTS > 0 )) || fail 'UDP proof recorded no file-mailbox packet frames'
(( UDP_ENCRYPTED_FRAMES == UDP_FRAME_ARTIFACTS )) || fail 'UDP proof observed a packet frame that was not encrypted'
(( UDP_PLAINTEXT_FRAMES == 0 )) || fail 'UDP proof observed plaintext tunnel.packet frames'
udp_plaintext_payload="$(python3 - "$UDP_NONCE" <<'PY'
import base64, sys
payload = b"\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00" + sys.argv[1].encode()
print(base64.b64encode(payload).decode())
PY
)"
if grep -R -F -e "$UDP_NONCE" -e "$udp_plaintext_payload" "$PRIMARY_DIR" >/dev/null 2>&1; then
    fail 'UDP nonce material was visible in raw mailbox frames'
fi
UDP_CIPHERTEXT_CONFIRMED=true

PHASE=backup_failover
enable_mailbox "$BACKUP_DIR"
disable_mailbox "$PRIMARY_DIR"
backup_before="$(mailbox_line_count "$BACKUP_DIR")"
backup_result="$(probe_nonce backup)" || fail 'backup SOCKS payload failed'
[[ "$backup_result" == "$BACKUP_NONCE" ]] || fail "backup payload mismatch: $backup_result"
BACKUP_NONCE_VALID=true
BACKUP_ROUTE="$(wait_for_route local.egress.backup)" || fail 'client log did not record backup route'
backup_after="$(mailbox_line_count "$BACKUP_DIR")"
BACKUP_FRAME_ARTIFACTS=$((backup_after-backup_before))
(( BACKUP_FRAME_ARTIFACTS > 0 )) || fail 'backup mailbox recorded no payload frames'
kill -0 "$NODE_PID" 2>/dev/null || fail 'node daemon died before failover proof completed'
kill -0 "$CLIENT_PID" 2>/dev/null || fail 'client daemon died before failover proof completed'

PHASE=primary_failure_observation
failure_log_start=$(( $(wc -l < "$CLIENT_LOG") + 1 ))
selection_status="$(curl -sf -X POST "http://127.0.0.1:${CLIENT_API_PORT}/v1/session/egress/select" \
    -H 'Content-Type: application/json' -d '{"egress_endpoint_id":"local.egress.primary"}')" || fail 'could not force disabled primary route'
printf '%s' "$selection_status" | python3 -c 'import json,sys; raise SystemExit(0 if json.load(sys.stdin).get("selected_egress_endpoint_id")=="local.egress.primary" else 1)' || fail 'runtime did not retain forced primary selection'
if probe_nonce primary >/dev/null 2>&1; then
    fail 'disabled selected primary unexpectedly transferred a SOCKS payload'
fi
primary_failure_log=""
for _ in $(seq 1 30); do
    primary_failure_log="$(tail -n +"$failure_log_start" "$CLIENT_LOG" | grep 'socks5 connect error' | grep '/primary/primary.jsonl' | tail -n 1 || true)"
    [[ -n "$primary_failure_log" ]] && break
    sleep .1
done
[[ -n "$primary_failure_log" ]] || fail 'forced primary SOCKS failure was not recorded in client log'
PRIMARY_FAILURE_EVIDENCE="selected=local.egress.primary; $primary_failure_log"
PRIMARY_FAILURE_OBSERVED=true

PHASE=disconnect
curl -sf -X POST "http://127.0.0.1:${CLIENT_API_PORT}/v1/session/disconnect" --data '' >/dev/null || fail 'disconnect failed'
SUCCEEDED=true
echo "[*] Local primary-to-backup SOCKS integration passed"
