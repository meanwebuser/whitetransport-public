#!/usr/bin/env bash
# One real daemon without production credentials: startup, API health/status/carriers, exact cleanup.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
GO_ROOT="$REPO_ROOT/core/go"
RUN_DIR="$(mktemp -d /tmp/wt-daemon-startup-health.XXXXXX)"
BINARY="$RUN_DIR/whitetransportd"
CONFIG="$RUN_DIR/node.json"
LOG="$RUN_DIR/node.log"
NODE_PID=""
API_PORT=0
SOCKS_PORT=0
START_MS="$(date +%s%3N)"
PHASE=setup
ERRORS=()
CLEANUP_RAN=false
SUCCEEDED=false
CONFIG_ISOLATION_VERIFIED=false
HEALTH_OK=false
STATUS_VERIFIED=false
CARRIERS_VERIFIED=false
OBSERVED_CARRIER_IDS=""
PID_EXITED=false
LISTENERS_RELEASED=false
ARTIFACTS_REMOVED=false
BINARY_SHA256=""

reserve_port() {
    python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}
record_error() { local text="${1//$'\n'/ }"; ERRORS+=("${PHASE}:${text:0:1000}"); }
fail() { record_error "$1"; exit 1; }
listener_exists() {
    local port="$1"
    ss -H -lnt "sport = :$port" 2>/dev/null | grep -q .
}

emit_json() {
    local status="$1" elapsed error_lines
    elapsed=$(($(date +%s%3N)-START_MS))
    error_lines="$(printf '%s\n' "${ERRORS[@]:-}")"
    STATUS="$status" ELAPSED="$elapsed" ERROR_LINES="$error_lines" API_PORT="$API_PORT" SOCKS_PORT="$SOCKS_PORT" \
    CONFIG_ISOLATION_VERIFIED="$CONFIG_ISOLATION_VERIFIED" HEALTH_OK="$HEALTH_OK" STATUS_VERIFIED="$STATUS_VERIFIED" \
    CARRIERS_VERIFIED="$CARRIERS_VERIFIED" OBSERVED_CARRIER_IDS="$OBSERVED_CARRIER_IDS" \
    PID_EXITED="$PID_EXITED" LISTENERS_RELEASED="$LISTENERS_RELEASED" \
    ARTIFACTS_REMOVED="$ARTIFACTS_REMOVED" BINARY_SHA256="$BINARY_SHA256" python3 - <<'PY'
import json, os
print(json.dumps({
    "test":"daemon-local-startup-health",
    "proofLevel":"startup-health",
    "daemonCount":1,
    "productionCredentialsUsed":False,
    "tokenStorePresent":False,
    "syntheticTestCredentialUsed":False,
    "credentialScope":"none",
    "bootstrapSecretConfigured":True,
    "bootstrapSecretScope":"deterministic-local-fixture",
    "providerEndpointsConfigured":False,
    "configIsolationVerified":os.environ["CONFIG_ISOLATION_VERIFIED"]=="true",
    "healthOk":os.environ["HEALTH_OK"]=="true",
    "statusVerified":os.environ["STATUS_VERIFIED"]=="true",
    "carriersVerified":os.environ["CARRIERS_VERIFIED"]=="true",
    "expectedCarrierIds":["local.control","local.egress.backup","local.egress.primary"],
    "minimumObservedCarrierIds":["local.control"],
    "observedCarrierIds":[value for value in os.environ["OBSERVED_CARRIER_IDS"].split(",") if value],
    "configuredCarrierTypes":["file.mailbox"],
    "apiHost":"127.0.0.1",
    "apiPort":int(os.environ["API_PORT"]),
    "socksHost":"127.0.0.1",
    "socksPort":int(os.environ["SOCKS_PORT"]),
    "binarySha256":os.environ["BINARY_SHA256"] or None,
    "cleanup":{
        "pidExited":os.environ["PID_EXITED"]=="true",
        "listenersReleased":os.environ["LISTENERS_RELEASED"]=="true",
        "artifactsRemoved":os.environ["ARTIFACTS_REMOVED"]=="true",
    },
    "latencyMs":int(os.environ["ELAPSED"]),
    "exit":int(os.environ["STATUS"]),
    "phaseErrors":[line for line in os.environ["ERROR_LINES"].splitlines() if line],
},separators=(",",":")))
PY
}

cleanup() {
    local status=$?
    [[ "$CLEANUP_RAN" == false ]] || return
    CLEANUP_RAN=true
    trap - EXIT ERR INT TERM
    PHASE=cleanup
    if [[ "$SUCCEEDED" != true && -f "$LOG" ]]; then
        echo "[*] Daemon log after startup-health failure:" >&2
        tail -n 60 "$LOG" >&2 || true
    fi
    if [[ -n "$NODE_PID" ]]; then
        kill "$NODE_PID" 2>/dev/null || true
        wait "$NODE_PID" 2>/dev/null || true
        if ! kill -0 "$NODE_PID" 2>/dev/null; then PID_EXITED=true; else record_error "daemon PID remained alive"; status=1; fi
    else
        PID_EXITED=true
    fi
    LISTENERS_RELEASED=true
    for _ in $(seq 1 30); do
        if ! listener_exists "$API_PORT" && ! listener_exists "$SOCKS_PORT"; then break; fi
        LISTENERS_RELEASED=false
        sleep .1
    done
    if listener_exists "$API_PORT" || listener_exists "$SOCKS_PORT"; then
        LISTENERS_RELEASED=false
        record_error "owned listener remained after daemon exit"
        status=1
    else
        LISTENERS_RELEASED=true
    fi
    chmod -R u+rwX "$RUN_DIR" 2>/dev/null || true
    rm -rf "$RUN_DIR"
    if [[ ! -e "$RUN_DIR" ]]; then ARTIFACTS_REMOVED=true; else record_error "run directory remained"; status=1; fi
    emit_json "$status"
    exit "$status"
}
trap cleanup EXIT
trap 'record_error "unexpected command failure at line $LINENO"' ERR
trap 'exit 130' INT TERM

command -v ss >/dev/null 2>&1 || fail "ss is required for listener cleanup proof"
export PATH="$PATH:/usr/local/go/bin"

PHASE=build
if [[ -n "${WT_LOCAL_DAEMON_BINARY:-}" ]]; then
    [[ -f "$WT_LOCAL_DAEMON_BINARY" && -x "$WT_LOCAL_DAEMON_BINARY" ]] || fail "WT_LOCAL_DAEMON_BINARY must be executable"
    install -m 0700 "$WT_LOCAL_DAEMON_BINARY" "$BINARY"
else
    (cd "$GO_ROOT" && GOPROXY=off /usr/local/go/bin/go build -o "$BINARY" ./cmd/whitetransportd/)
fi
BINARY_SHA256="$(sha256sum "$BINARY" | awk '{print $1}')"

PHASE=startup
for attempt in 1 2 3; do
    API_PORT="$(reserve_port)"
    SOCKS_PORT="$(reserve_port)"
    CONTROL_DIR="$RUN_DIR/control-$attempt"
    PRIMARY_DIR="$RUN_DIR/primary-$attempt"
    BACKUP_DIR="$RUN_DIR/backup-$attempt"
    mkdir -p "$CONTROL_DIR" "$PRIMARY_DIR" "$BACKUP_DIR"
    sed \
        -e "s|__NODE_API_PORT__|$API_PORT|g" \
        -e "s|__NODE_SOCKS_PORT__|$SOCKS_PORT|g" \
        -e "s|__CONTROL_DIR__|$CONTROL_DIR|g" \
        -e "s|__PRIMARY_DIR__|$PRIMARY_DIR|g" \
        -e "s|__BACKUP_DIR__|$BACKUP_DIR|g" \
        "$REPO_ROOT/config/dev/local-failover-node.json.template" > "$CONFIG"
    python3 - "$RUN_DIR" "$CONFIG" <<'PY' || fail "generated startup config is not isolated"
import json, os, sys
root=os.path.realpath(sys.argv[1]); path=sys.argv[2]
cfg=json.load(open(path, encoding="utf-8"))
cfg["state_file"]=os.path.join(root,"node-state.json")
assert "token_store" not in cfg
assert cfg.get("bootstrap_secret")=="deterministic-local-bootstrap-fixture"
assert not cfg.get("upstream_proxy",{}).get("url")
assert not cfg.get("admin_relay",{}).get("enabled",False)
carriers=cfg.get("carrier_configs",[])
assert carriers and all(item.get("carrier_type")=="file.mailbox" for item in carriers)
assert not any(any(name in json.dumps(item).lower() for name in ("vk.com","ok.ru","wbstream","dion","telemost")) for item in carriers)
for item in carriers:
    mailbox=os.path.realpath(item["file_mailbox"]["dir"])
    assert os.path.commonpath((root,mailbox))==root
json.dump(cfg,open(path,"w",encoding="utf-8"),separators=(",",":"))
PY
    chmod 0600 "$CONFIG"
    CONFIG_ISOLATION_VERIFIED=true
    : > "$LOG"
    "$BINARY" -config "$CONFIG" -serve >"$LOG" 2>&1 &
    NODE_PID=$!
    ready=false
    for _ in $(seq 1 50); do
        if curl --noproxy '*' -sf "http://127.0.0.1:${API_PORT}/health" >/dev/null 2>&1; then ready=true; break; fi
        kill -0 "$NODE_PID" 2>/dev/null || break
        sleep .1
    done
    if [[ "$ready" == true ]]; then break; fi
    kill "$NODE_PID" 2>/dev/null || true
    wait "$NODE_PID" 2>/dev/null || true
    NODE_PID=""
    if [[ "$attempt" == 3 ]]; then fail "daemon did not reach health after bounded port retries"; fi
done

PHASE=api
health_json="$(curl --noproxy '*' -sf "http://127.0.0.1:${API_PORT}/health")" || fail "health request failed"
status_json="$(curl --noproxy '*' -sf "http://127.0.0.1:${API_PORT}/v1/status")" || fail "status request failed"
carriers_json=""
for _ in $(seq 1 50); do
    carriers_json="$(curl --noproxy '*' -sf "http://127.0.0.1:${API_PORT}/v1/carriers")" || fail "carriers request failed"
    if python3 - "$carriers_json" <<'PY'
import json, sys
carriers=json.loads(sys.argv[1])
raise SystemExit(0 if isinstance(carriers,dict) and "local.control" in carriers else 1)
PY
    then
        break
    fi
    sleep .1
done
read -r HEALTH_OK STATUS_VERIFIED CARRIERS_VERIFIED OBSERVED_CARRIER_IDS < <(python3 - "$health_json" "$status_json" "$carriers_json" <<'PY'
import json, sys
health,status,carriers=map(json.loads,sys.argv[1:])
health_ok=health.get("ok") is True and health.get("carriers")==3
status_ok=status.get("role")=="node" and status.get("node_id")=="local-failover-node"
expected={"local.control","local.egress.primary","local.egress.backup"}
observed=set(carriers) if isinstance(carriers,dict) else set()
carrier_ok=observed==expected and all(
    isinstance(snapshot,dict) and snapshot.get("carrier_id")==carrier_id
    for carrier_id,snapshot in carriers.items()
)
print(str(health_ok).lower(),str(status_ok).lower(),str(carrier_ok).lower(),",".join(sorted(observed)) or "-")
PY
)
[[ "$OBSERVED_CARRIER_IDS" != - ]] || OBSERVED_CARRIER_IDS=""
[[ "$HEALTH_OK" == true ]] || fail "health response did not prove configured carriers"
[[ "$STATUS_VERIFIED" == true ]] || fail "status response did not prove node identity"
[[ "$CARRIERS_VERIFIED" == true ]] || fail "carrier telemetry did not match isolated config"
kill -0 "$NODE_PID" 2>/dev/null || fail "daemon exited before cleanup proof"

SUCCEEDED=true
exit 0
