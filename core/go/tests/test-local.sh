#!/usr/bin/env bash
# Deterministic two-daemon SOCKS5 primary-to-backup integration proof.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
GO_ROOT="$REPO_ROOT/core/go"
GO_BIN="${WT_GO_BIN:-/usr/local/go/bin/go}"
if [[ ! -x "$GO_BIN" ]]; then
    GO_BIN="$(command -v go || true)"
fi
[[ -n "$GO_BIN" && -x "$GO_BIN" ]] || {
    echo "missing Go compiler; set WT_GO_BIN or install go on PATH" >&2
    exit 127
}
TEST_TMP_ROOT="${WT_TEST_TMP_ROOT:-$REPO_ROOT/.tmp}"
mkdir -p "$TEST_TMP_ROOT"
RUN_DIR="$(mktemp -d "$TEST_TMP_ROOT/wt-local-failover.XXXXXX")"
BINARY="$RUN_DIR/whitetransportd"
NODE_LOG="$RUN_DIR/node.log"
CLIENT_LOG="$RUN_DIR/client.log"
NODE_PID="" CLIENT_PID="" HTTP_PID="" UDP_PID="" COLLISION_PID=""
HOST_NETNS_INODE="" CLIENT_NETNS_INODE=""
SUCCEEDED=false PHASE=setup ERRORS=()
PRIMARY_ROUTE="" BACKUP_ROUTE="" PRIMARY_NONCE_VALID=false BACKUP_NONCE_VALID=false
PRIMARY_FAILURE_OBSERVED=false PRIMARY_FRAME_ARTIFACTS=0 BACKUP_FRAME_ARTIFACTS=0
BINARY_SHA256="" BINARY_SOURCE="local-build"
PRIMARY_FAILURE_EVIDENCE="" CONFIG_ISOLATION_VERIFIED=false
UDP_NONCE_VALID=false UDP_EXIT_RESOLVER_INVOCATIONS=0 UDP_CIPHERTEXT_CONFIRMED=false UDP_FRAME_ARTIFACTS=0 UDP_ENCRYPTED_FRAMES=0 UDP_PLAINTEXT_FRAMES=0
DIRECT_IP="" SOCKS_IP="" SELECTED_NODE_ID=""
HEALTH_OK=false STATUS_VERIFIED=false CARRIERS_VERIFIED=false
CLIENT_NETWORK_NAMESPACE_ISOLATED=false DIRECT_ORIGIN_REACHABLE_FROM_CLIENT=false SOCKS_ORIGIN_REACHABLE_FROM_CLIENT=false NO_DIRECT_FALLBACK_PROVED=false
BIND_COLLISION_RETRY_PROVED=false
NODE_PID_EXITED=false CLIENT_PID_EXITED=false LISTENERS_RELEASED=false ARTIFACTS_REMOVED=false NETWORK_NAMESPACE_RELEASED=false
GIT_COMMIT="$(git -C "$REPO_ROOT" rev-parse HEAD)"
GIT_DIRTY=false
[[ -z "$(git -C "$REPO_ROOT" status --porcelain --untracked-files=normal)" ]] || GIT_DIRTY=true
START_MS="$(date +%s%3N)"

reserve_port() {
    python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}
allocate_ports() {
    NODE_API_PORT="$(reserve_port)"
    CLIENT_API_PORT="$(reserve_port)"
    NODE_SOCKS_PORT="$(reserve_port)"
    CLIENT_SOCKS_PORT="$(reserve_port)"
    HTTP_PORT="$(reserve_port)"
    UDP_PORT="$(reserve_port)"
}
record_error() { local text="${1//$'\n'/ }"; ERRORS+=("${PHASE}:${text:0:4000}"); }
fail() { record_error "$1"; exit 1; }
client_ns() {
    nsenter -t "$CLIENT_PID" -U -n --preserve-credentials -- "$@"
}
health_probe() {
    curl --noproxy '*' --max-time 0.2 -sf "$1" >/dev/null 2>&1
}
host_tcp_listener_exists() {
    local port="$1"
    ss -H -lnt "sport = :$port" 2>/dev/null | grep -q .
}
host_udp_listener_exists() {
    local port="$1"
    ss -H -lnu "sport = :$port" 2>/dev/null | grep -q .
}

allocate_ports
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
    DIRECT_IP="$DIRECT_IP" SOCKS_IP="$SOCKS_IP" SELECTED_NODE_ID="$SELECTED_NODE_ID" CLIENT_API_PORT="$CLIENT_API_PORT" CLIENT_SOCKS_PORT="$CLIENT_SOCKS_PORT" \
    HEALTH_OK="$HEALTH_OK" STATUS_VERIFIED="$STATUS_VERIFIED" CARRIERS_VERIFIED="$CARRIERS_VERIFIED" \
    CLIENT_NETWORK_NAMESPACE_ISOLATED="$CLIENT_NETWORK_NAMESPACE_ISOLATED" DIRECT_ORIGIN_REACHABLE_FROM_CLIENT="$DIRECT_ORIGIN_REACHABLE_FROM_CLIENT" \
    SOCKS_ORIGIN_REACHABLE_FROM_CLIENT="$SOCKS_ORIGIN_REACHABLE_FROM_CLIENT" NO_DIRECT_FALLBACK_PROVED="$NO_DIRECT_FALLBACK_PROVED" \
    BIND_COLLISION_RETRY_PROVED="$BIND_COLLISION_RETRY_PROVED" \
    NODE_PID_EXITED="$NODE_PID_EXITED" CLIENT_PID_EXITED="$CLIENT_PID_EXITED" LISTENERS_RELEASED="$LISTENERS_RELEASED" \
    ARTIFACTS_REMOVED="$ARTIFACTS_REMOVED" NETWORK_NAMESPACE_RELEASED="$NETWORK_NAMESPACE_RELEASED" \
    GIT_COMMIT="$GIT_COMMIT" GIT_DIRTY="$GIT_DIRTY" BINARY_SHA256="$BINARY_SHA256" BINARY_SOURCE="$BINARY_SOURCE" python3 - <<'PY'
import json, os
print(json.dumps({
    "test":"desktop-local-fast", "proofLevel":"local-integration", "transport":"file.mailbox",
    "daemonCount":2, "productionCredentialsUsed":False, "tokenStorePresent":False,
    "syntheticTestCredentialUsed":False, "credentialScope":"none",
    "bootstrapSecretConfigured":True, "bootstrapSecretScope":"deterministic-local-fixture",
    "providerEndpointsConfigured":False,
    "configIsolationVerified":os.environ["CONFIG_ISOLATION_VERIFIED"]=="true", "socksStrict":True,
    "ipProofScope":"loopback-local", "directIp":os.environ["DIRECT_IP"] or None, "socksIp":os.environ["SOCKS_IP"] or None,
    "selectedNodeId":os.environ["SELECTED_NODE_ID"] or None,
    "apiHost":"127.0.0.1", "apiPort":int(os.environ["CLIENT_API_PORT"]),
    "socksHost":"127.0.0.1", "socksPort":int(os.environ["CLIENT_SOCKS_PORT"]),
    "healthOk":os.environ["HEALTH_OK"]=="true", "statusVerified":os.environ["STATUS_VERIFIED"]=="true",
    "carriersVerified":os.environ["CARRIERS_VERIFIED"]=="true",
    "clientNetworkNamespaceIsolated":os.environ["CLIENT_NETWORK_NAMESPACE_ISOLATED"]=="true",
    "directOriginReachableFromClient":os.environ["DIRECT_ORIGIN_REACHABLE_FROM_CLIENT"]=="true",
    "socksOriginReachableFromClient":os.environ["SOCKS_ORIGIN_REACHABLE_FROM_CLIENT"]=="true",
    "noDirectFallbackProved":os.environ["NO_DIRECT_FALLBACK_PROVED"]=="true",
    "bindCollisionRetryProved":os.environ["BIND_COLLISION_RETRY_PROVED"]=="true",
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
    "cleanup":{
        "nodePidExited":os.environ["NODE_PID_EXITED"]=="true",
        "clientPidExited":os.environ["CLIENT_PID_EXITED"]=="true",
        "listenersReleased":os.environ["LISTENERS_RELEASED"]=="true",
        "artifactsRemoved":os.environ["ARTIFACTS_REMOVED"]=="true",
        "networkNamespaceReleased":os.environ["NETWORK_NAMESPACE_RELEASED"]=="true",
    },
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
    if [[ -n "$CLIENT_PID" ]] && kill -0 "$CLIENT_PID" 2>/dev/null; then
        client_ns curl --noproxy '*' -sf -X POST "http://127.0.0.1:${CLIENT_API_PORT}/v1/session/disconnect" >/dev/null 2>&1 || true
    fi
    for pid in "$NODE_PID" "$CLIENT_PID" "$HTTP_PID" "$UDP_PID" "$COLLISION_PID"; do
        [[ -z "$pid" ]] || kill "$pid" 2>/dev/null || true
    done
    for pid in "$NODE_PID" "$CLIENT_PID" "$HTTP_PID" "$UDP_PID" "$COLLISION_PID"; do
        [[ -z "$pid" ]] || wait "$pid" 2>/dev/null || true
    done
    if [[ -z "$NODE_PID" ]] || ! kill -0 "$NODE_PID" 2>/dev/null; then NODE_PID_EXITED=true; else record_error 'node PID remained alive'; status=1; fi
    if [[ -z "$CLIENT_PID" ]] || ! kill -0 "$CLIENT_PID" 2>/dev/null; then CLIENT_PID_EXITED=true; else record_error 'client PID remained alive'; status=1; fi
    if [[ -z "$CLIENT_PID" ]] || [[ ! -e "/proc/$CLIENT_PID/ns/net" ]]; then NETWORK_NAMESPACE_RELEASED=true; else record_error 'client network namespace remained reachable'; status=1; fi
    LISTENERS_RELEASED=true
    for _ in $(seq 1 30); do
        if ! host_tcp_listener_exists "$NODE_API_PORT" && ! host_tcp_listener_exists "$NODE_SOCKS_PORT" && \
           ! host_tcp_listener_exists "$HTTP_PORT" && ! host_udp_listener_exists "$UDP_PORT"; then
            break
        fi
        LISTENERS_RELEASED=false
        sleep .1
    done
    if host_tcp_listener_exists "$NODE_API_PORT" || host_tcp_listener_exists "$NODE_SOCKS_PORT" || \
       host_tcp_listener_exists "$HTTP_PORT" || host_udp_listener_exists "$UDP_PORT"; then
        LISTENERS_RELEASED=false
        record_error 'owned host listener remained after cleanup'
        status=1
    else
        LISTENERS_RELEASED=true
    fi
    chmod -R u+rwX "$RUN_DIR" 2>/dev/null || true
    rm -rf "$RUN_DIR"
    if [[ ! -e "$RUN_DIR" ]]; then ARTIFACTS_REMOVED=true; else record_error 'run directory remained after cleanup'; status=1; fi
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
    client_ns curl --noproxy '' --max-time 8 -sf -x "socks5h://127.0.0.1:${CLIENT_SOCKS_PORT}" "http://127.0.0.1:${HTTP_PORT}/$path"
}

select_egress() {
    local endpoint_id="$1" response
    response="$(client_ns curl --noproxy '*' -sf -X POST "http://127.0.0.1:${CLIENT_API_PORT}/v1/session/egress/select" \
        -H 'Content-Type: application/json' -d "{\"egress_endpoint_id\":\"$endpoint_id\"}")" || return 1
    printf '%s' "$response" | python3 -c 'import json,sys; expected=sys.argv[1]; raise SystemExit(0 if json.load(sys.stdin).get("selected_egress_endpoint_id")==expected else 1)' "$endpoint_id"
}

PHASE=namespace_preflight
for command_name in unshare nsenter ip ss; do
    command -v "$command_name" >/dev/null 2>&1 || fail "$command_name is required for dynamic client network isolation"
done
unshare -Un --map-root-user true >/dev/null 2>&1 || fail 'unprivileged user+network namespaces are unavailable'

PHASE=config
render_config "$REPO_ROOT/config/dev/local-failover-node.json.template" "$NODE_CFG"
render_config "$REPO_ROOT/config/dev/local-failover-client.json.template" "$CLIENT_CFG"
python3 - "$RUN_DIR" "$NODE_CFG" "$CLIENT_CFG" <<'PY' || fail 'generated configs are not isolated local file-mailbox configs'
import json, os, sys
run_dir=os.path.realpath(sys.argv[1])
for path in sys.argv[2:]:
    cfg=json.load(open(path, encoding="utf-8"))
    assert "token_store" not in cfg
    assert cfg.get("bootstrap_secret") == "deterministic-local-bootstrap-fixture"
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
    (cd "$GO_ROOT" && GOPROXY=off "$GO_BIN" build -o "$BINARY" ./cmd/whitetransportd/)
fi
BINARY_SHA256="$(sha256sum "$BINARY" | awk '{print $1}')"

PHASE=start_daemons
daemons_ready=false
for launch_attempt in 1 2 3; do
    if [[ "$launch_attempt" -gt 1 ]]; then
        allocate_ports
        render_config "$REPO_ROOT/config/dev/local-failover-node.json.template" "$NODE_CFG"
        render_config "$REPO_ROOT/config/dev/local-failover-client.json.template" "$CLIENT_CFG"
    fi
    : > "$NODE_LOG"
    : > "$CLIENT_LOG"
    if [[ "${WT_TEST_FORCE_BIND_COLLISION_ONCE:-0}" == 1 && "$launch_attempt" == 1 ]]; then
        python3 - "$NODE_API_PORT" <<'PY' >"$RUN_DIR/collision.log" 2>&1 &
import socket, sys, time
s=socket.socket(); s.bind(("127.0.0.1",int(sys.argv[1]))); s.listen()
time.sleep(30)
PY
        COLLISION_PID=$!
        for _ in $(seq 1 30); do host_tcp_listener_exists "$NODE_API_PORT" && break; sleep .05; done
        host_tcp_listener_exists "$NODE_API_PORT" || fail 'forced bind-collision listener did not start'
    fi
    "$BINARY" -config "$NODE_CFG" -serve > "$NODE_LOG" 2>&1 &
    NODE_PID=$!
    node_ready=false
    for _ in $(seq 1 50); do
        if health_probe "http://127.0.0.1:${NODE_API_PORT}/health"; then node_ready=true; break; fi
        kill -0 "$NODE_PID" 2>/dev/null || break
        sleep .1
    done
    if [[ "$node_ready" != true ]]; then
        kill "$NODE_PID" 2>/dev/null || true; wait "$NODE_PID" 2>/dev/null || true; NODE_PID=""
        if [[ -n "$COLLISION_PID" ]]; then
            kill "$COLLISION_PID" 2>/dev/null || true; wait "$COLLISION_PID" 2>/dev/null || true; COLLISION_PID=""
        fi
        if [[ "${WT_TEST_FORCE_BIND_COLLISION_ONCE:-0}" == 1 && "$launch_attempt" == 1 ]] && grep -Fq 'address already in use' "$NODE_LOG"; then
            BIND_COLLISION_RETRY_PROVED=true
        fi
        [[ "$launch_attempt" -lt 3 ]] && continue
        fail 'node daemon did not reach health after bounded bind retries'
    fi
    unshare -Un --map-root-user sh -c 'ip link set lo up; exec "$@"' sh "$BINARY" -config "$CLIENT_CFG" -serve > "$CLIENT_LOG" 2>&1 &
    CLIENT_PID=$!
    client_ready=false
    for _ in $(seq 1 50); do
        if client_ns curl --noproxy '*' --max-time 0.2 -sf "http://127.0.0.1:${CLIENT_API_PORT}/health" >/dev/null 2>&1; then client_ready=true; break; fi
        kill -0 "$CLIENT_PID" 2>/dev/null || break
        sleep .1
    done
    if [[ "$client_ready" == true ]]; then daemons_ready=true; break; fi
    kill "$CLIENT_PID" "$NODE_PID" 2>/dev/null || true
    wait "$CLIENT_PID" 2>/dev/null || true; wait "$NODE_PID" 2>/dev/null || true
    CLIENT_PID=""; NODE_PID=""
done
[[ "$daemons_ready" == true ]] || fail 'local daemons did not reach health after bounded bind retries'
HOST_NETNS_INODE="$(readlink /proc/self/ns/net)"
CLIENT_NETNS_INODE="$(readlink "/proc/$CLIENT_PID/ns/net")"
[[ -n "$CLIENT_NETNS_INODE" && "$CLIENT_NETNS_INODE" != "$HOST_NETNS_INODE" ]] || fail 'client did not enter a distinct network namespace'
[[ -z "$(client_ns ip route show)" ]] || fail 'isolated client namespace unexpectedly has a direct route'
CLIENT_NETWORK_NAMESPACE_ISOLATED=true

PHASE=discovery
for _ in $(seq 1 60); do
    if client_ns curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API_PORT}/v1/nodes" | python3 -c 'import json,sys; raise SystemExit(0 if any(n.get("node_id")=="local-failover-node" and n.get("available") for n in json.load(sys.stdin)) else 1)'; then
        break
    fi
    sleep .1
done
client_ns curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API_PORT}/v1/nodes" | python3 -c 'import json,sys; raise SystemExit(0 if any(n.get("node_id")=="local-failover-node" and n.get("available") for n in json.load(sys.stdin)) else 1)' || fail 'local node was not discovered'

PHASE=connect
client_ns curl --noproxy '*' --max-time 15 -sf -X POST "http://127.0.0.1:${CLIENT_API_PORT}/v1/session/connect" -H 'Content-Type: application/json' -d '{"node_id":"local-failover-node"}' >/dev/null || fail 'session connect failed'
for _ in $(seq 1 60); do
    if client_ns curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API_PORT}/v1/status" | python3 -c 'import json,sys; raise SystemExit(0 if json.load(sys.stdin).get("state")=="connected" else 1)'; then
        break
    fi
    sleep .1
done
client_ns curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API_PORT}/v1/status" | python3 -c 'import json,sys; raise SystemExit(0 if json.load(sys.stdin).get("state")=="connected" else 1)' || fail 'client did not reach connected state'

PHASE=http_target
python3 - "$HTTP_PORT" "$PRIMARY_NONCE" "$BACKUP_NONCE" <<'PY' >"$RUN_DIR/http.log" 2>&1 &
import http.server, sys
responses={"/primary":sys.argv[2].encode(), "/backup":sys.argv[3].encode()}
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body=self.client_address[0].encode() if self.path=="/ip" else responses.get(self.path)
        if body is None: self.send_error(404); return
        self.send_response(200); self.send_header("Content-Length",str(len(body))); self.end_headers(); self.wfile.write(body)
    def log_message(self,*_args): pass
http.server.HTTPServer(("127.0.0.1",int(sys.argv[1])),Handler).serve_forever()
PY
HTTP_PID=$!
sleep .2
kill -0 "$HTTP_PID" 2>/dev/null || fail 'nonce HTTP target failed to start'

PHASE=dynamic_isolation
for _ in $(seq 1 30); do
    DIRECT_IP="$(curl --noproxy '*' --max-time 1 -sf "http://127.0.0.1:${HTTP_PORT}/ip" 2>/dev/null || true)"
    [[ -n "$DIRECT_IP" ]] && break
    sleep .1
done
[[ -n "$DIRECT_IP" ]] || fail 'host direct loopback IP proof failed'
if client_ns curl --noproxy '*' --max-time 2 -sf "http://127.0.0.1:${HTTP_PORT}/ip" >/dev/null 2>&1; then
    DIRECT_ORIGIN_REACHABLE_FROM_CLIENT=true
    fail 'isolated client reached the host origin directly'
fi
DIRECT_ORIGIN_REACHABLE_FROM_CLIENT=false
select_egress local.egress.primary || fail 'could not select primary route before isolated SOCKS proof'
disable_mailbox "$BACKUP_DIR"
SOCKS_IP="$(client_ns curl --noproxy '' --max-time 8 -sf -x "socks5h://127.0.0.1:${CLIENT_SOCKS_PORT}" "http://127.0.0.1:${HTTP_PORT}/ip")" || fail 'isolated client SOCKS IP proof failed'
SOCKS_ORIGIN_REACHABLE_FROM_CLIENT=true
[[ "$DIRECT_IP" == "127.0.0.1" && "$SOCKS_IP" == "127.0.0.1" ]] || fail "unexpected loopback IP evidence direct=$DIRECT_IP socks=$SOCKS_IP"
NO_DIRECT_FALLBACK_PROVED=true

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
udp_result="$(client_ns python3 - "$CLIENT_SOCKS_PORT" "$UDP_PORT" "$UDP_NONCE" <<'PY'
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

PHASE=primary_failure_observation
enable_mailbox "$BACKUP_DIR"
disable_mailbox "$PRIMARY_DIR"
failure_log_start=$(( $(wc -l < "$NODE_LOG") + 1 ))
if probe_nonce primary >/dev/null 2>&1; then
    fail 'disabled selected primary unexpectedly transferred a SOCKS payload'
fi
primary_failure_log=""
for _ in $(seq 1 30); do
    primary_failure_log="$(tail -n +"$failure_log_start" "$NODE_LOG" | grep 'tunnelReader write error' | grep '/primary/primary.jsonl' | tail -n 1 || true)"
    [[ -n "$primary_failure_log" ]] && break
    sleep .1
done
[[ -n "$primary_failure_log" ]] || fail 'forced primary write failure was not recorded in node log'
PRIMARY_FAILURE_EVIDENCE="selected=local.egress.primary; $primary_failure_log"
PRIMARY_FAILURE_OBSERVED=true

PHASE=backup_failover
select_egress local.egress.backup || fail 'could not select compatible backup after primary failure'
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

PHASE=api_evidence
node_health_json="$(curl --noproxy '*' -sf "http://127.0.0.1:${NODE_API_PORT}/health")" || fail 'node health request failed'
client_health_json="$(client_ns curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API_PORT}/health")" || fail 'client health request failed'
node_status_json="$(curl --noproxy '*' -sf "http://127.0.0.1:${NODE_API_PORT}/v1/status")" || fail 'node status request failed'
client_status_json="$(client_ns curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API_PORT}/v1/status")" || fail 'client status request failed'
node_carriers_json="$(curl --noproxy '*' -sf "http://127.0.0.1:${NODE_API_PORT}/v1/carriers")" || fail 'node carriers request failed'
client_carriers_json="$(client_ns curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API_PORT}/v1/carriers")" || fail 'client carriers request failed'
read -r HEALTH_OK STATUS_VERIFIED CARRIERS_VERIFIED SELECTED_NODE_ID < <(python3 - \
    "$node_health_json" "$client_health_json" "$node_status_json" "$client_status_json" "$node_carriers_json" "$client_carriers_json" <<'PY'
import json, sys
node_health,client_health,node_status,client_status,node_carriers,client_carriers=map(json.loads,sys.argv[1:])
expected={"local.control","local.egress.primary","local.egress.backup"}
health_ok=all(item.get("ok") is True and item.get("carriers")==3 for item in (node_health,client_health))
status_ok=(
    node_status.get("role")=="node" and node_status.get("node_id")=="local-failover-node" and
    client_status.get("role")=="client" and client_status.get("state")=="connected" and
    client_status.get("active_node_id")=="local-failover-node"
)
carriers_ok=all(
    isinstance(snapshot,dict) and set(snapshot)==expected and all(
        isinstance(value,dict) and value.get("carrier_id")==carrier_id
        for carrier_id,value in snapshot.items()
    )
    for snapshot in (node_carriers,client_carriers)
)
print(str(health_ok).lower(),str(status_ok).lower(),str(carriers_ok).lower(),client_status.get("active_node_id") or "-")
PY
)
[[ "$SELECTED_NODE_ID" != - ]] || SELECTED_NODE_ID=""
[[ "$HEALTH_OK" == true ]] || fail 'daemon health APIs did not prove both configured runtimes'
[[ "$STATUS_VERIFIED" == true ]] || fail 'daemon status APIs did not prove the connected node identity'
[[ "$CARRIERS_VERIFIED" == true ]] || fail 'daemon carrier telemetry did not prove the exact configured IDs'

PHASE=disconnect
client_ns curl --noproxy '*' -sf -X POST "http://127.0.0.1:${CLIENT_API_PORT}/v1/session/disconnect" --data '' >/dev/null || fail 'disconnect failed'
SUCCEEDED=true
echo "[*] Local primary-to-backup SOCKS integration passed"
