#!/usr/bin/env bash
# Rootless three-surface process proof: Git control+egress -> file egress failover.
set -Eeuo pipefail

on_error() {
    local status=$?
    printf 'Git failover inner failure: line=%s status=%s command=%q\n' "$1" "$status" "$2" >&2
    return "$status"
}
trap 'on_error "$LINENO" "$BASH_COMMAND"' ERR

: "${WT_REPO_ROOT:?}"
: "${WT_RUN_DIR:?}"
: "${WT_BIN:?}"
: "${WT_SCRIPT_DIR:?}"

GIT_ROOT="$WT_RUN_DIR/git-private"
CLIENT_ROOT="$WT_RUN_DIR/client-private"
NODE_ROOT="$WT_RUN_DIR/node-private"
BACKUP_ROOT="$WT_RUN_DIR/file-backup"
GIT_LOG="$WT_RUN_DIR/git-daemon.log"
CLIENT_LOG="$WT_RUN_DIR/client.log"
NODE_LOG="$WT_RUN_DIR/node.log"
CLIENT_CFG="$WT_RUN_DIR/client.json"
NODE_CFG="$WT_RUN_DIR/node.json"
STREAM_OUT="$WT_RUN_DIR/stream.out"
STREAM_ERR="$WT_RUN_DIR/stream.err"
STREAM_MARKER="$NODE_ROOT/stream.started"
RESULT_FILE="$WT_RUN_DIR/result.json"
CLEANUP_RECEIPT="$WT_RUN_DIR/cleanup.ok"

GIT_ADDR=10.203.0.2
CLIENT_ADDR=10.203.0.3
NODE_ADDR=10.203.0.4
GIT_PORT=19418
CLIENT_API=19631
CLIENT_SOCKS=19632
NODE_API=19641
NODE_SOCKS=19642
TARGET_PORT=19701
GIT_URL="git://${GIT_ADDR}:${GIT_PORT}/transport.git"
PRIMARY_NONCE="git-primary-${RANDOM}-${RANDOM}"
BACKUP_NONCE="file-backup-${RANDOM}-${RANDOM}"

GIT_HOLDER=""
CLIENT_HOLDER=""
NODE_HOLDER=""
GIT_PID=""
GIT_SERVICE_PID=""
GIT_SERVICE_PGID=""
CLIENT_PID=""
NODE_PID=""
TARGET_PID=""
STREAM_PID=""
SUCCESS=false

kill_git_network_processes() {
    [[ -n "$GIT_SERVICE_PGID" ]] || return 0
    kill -- "-$GIT_SERVICE_PGID" 2>/dev/null || true
    sleep .2
    kill -KILL -- "-$GIT_SERVICE_PGID" 2>/dev/null || true
    GIT_SERVICE_PGID=""
    GIT_SERVICE_PID=""
}

stop_git_launcher() {
    [[ -n "$GIT_PID" ]] || return 0
    kill "$GIT_PID" 2>/dev/null || true
    for _ in $(seq 1 50); do
        kill -0 "$GIT_PID" 2>/dev/null || break
        sleep .02
    done
    kill -KILL "$GIT_PID" 2>/dev/null || true
    wait "$GIT_PID" 2>/dev/null || true
    GIT_PID=""
}

cleanup() {
    local status=$?
    local cleanup_ok=true pid link
    local tracked_pids="$GIT_SERVICE_PID $GIT_PID $CLIENT_PID $NODE_PID $TARGET_PID $CLIENT_HOLDER $NODE_HOLDER $GIT_HOLDER"
    set +e
    kill_git_network_processes
    stop_git_launcher
    for pid in "$STREAM_PID" "$CLIENT_PID" "$NODE_PID" "$TARGET_PID"; do
        [[ -z "$pid" ]] || kill "$pid" 2>/dev/null || true
    done
    for pid in "$STREAM_PID" "$CLIENT_PID" "$NODE_PID" "$TARGET_PID" "$GIT_PID"; do
        [[ -z "$pid" ]] || wait "$pid" 2>/dev/null || true
    done
    for pid in "$CLIENT_HOLDER" "$NODE_HOLDER" "$GIT_HOLDER"; do
        [[ -z "$pid" ]] || kill "$pid" 2>/dev/null || true
    done
    for pid in "$CLIENT_HOLDER" "$NODE_HOLDER" "$GIT_HOLDER"; do
        [[ -z "$pid" ]] || wait "$pid" 2>/dev/null || true
    done
    for link in veth-git veth-client veth-node; do
        ip link del "$link" 2>/dev/null || true
    done
    ip link del wtbr0 2>/dev/null || true
    for pid in $tracked_pids; do
        if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
            printf 'cleanup leaked pid %s\n' "$pid" >&2
            cleanup_ok=false
        fi
    done
    for link in wtbr0 veth-git veth-client veth-node; do
        if ip link show "$link" >/dev/null 2>&1; then
            printf 'cleanup leaked network link %s\n' "$link" >&2
            cleanup_ok=false
        fi
    done
    if grep -Fq " $GIT_ROOT " /proc/self/mountinfo \
        || grep -Fq " $CLIENT_ROOT " /proc/self/mountinfo \
        || grep -Fq " $NODE_ROOT " /proc/self/mountinfo; then
        printf 'cleanup leaked private mount into orchestrator surface\n' >&2
        cleanup_ok=false
    fi
    if ss -ltnH 2>/dev/null | grep -Eq ":(${GIT_PORT}|${CLIENT_API}|${CLIENT_SOCKS}|${NODE_API}|${NODE_SOCKS}|${TARGET_PORT})[[:space:]]"; then
        printf 'cleanup leaked test listener\n' >&2
        cleanup_ok=false
    fi
    if pgrep -f "$WT_RUN_DIR" >/dev/null 2>&1; then
        printf 'cleanup leaked child referencing run directory\n' >&2
        cleanup_ok=false
    fi
    if [[ "$SUCCESS" == true && "$cleanup_ok" == true && "$status" -eq 0 ]]; then
        : >"$CLEANUP_RECEIPT"
    else
        status=1
    fi
    if [[ "$SUCCESS" != true || "$cleanup_ok" != true ]]; then
        printf 'Git failover artifacts: %s\n' "$WT_RUN_DIR" >&2
        [[ ! -f "$CLIENT_LOG" ]] || tail -n 120 "$CLIENT_LOG" >&2
        [[ ! -f "$NODE_LOG" ]] || tail -n 100 "$NODE_LOG" >&2
        [[ ! -f "$GIT_LOG" ]] || tail -n 80 "$GIT_LOG" >&2
    fi
    exit "$status"
}
trap cleanup EXIT

wait_pid_namespace() {
    local pid="$1"
    for _ in $(seq 1 100); do
        kill -0 "$pid" 2>/dev/null && [[ -e "/proc/$pid/ns/net" ]] && return 0
        sleep .02
    done
    return 1
}

start_surface() {
    unshare --net --mount --propagation private bash -ceu '
        mount --make-rprivate /
        exec sleep infinity
    ' >/dev/null 2>&1 &
    local pid=$!
    wait_pid_namespace "$pid"
    printf '%s\n' "$pid"
}

attach_surface() {
    local holder="$1" host_if="$2" address="$3"
    ip link add "$host_if" type veth peer name eth0
    ip link set "$host_if" master wtbr0
    ip link set "$host_if" up
    ip link set eth0 netns "$holder"
    nsenter -t "$holder" -n -- ip link set lo up
    nsenter -t "$holder" -n -- ip addr add "${address}/24" dev eth0
    nsenter -t "$holder" -n -- ip link set eth0 up
}

client_curl() {
    nsenter -t "$CLIENT_HOLDER" -n -- curl "$@"
}

wait_client_http() {
    local path="$1"
    for _ in $(seq 1 180); do
        kill -0 "$CLIENT_PID" 2>/dev/null || return 1
        client_curl --noproxy '*' --max-time 1 -sf "http://127.0.0.1:${CLIENT_API}${path}" >/dev/null 2>&1 && return 0
        sleep .1
    done
    return 1
}

mkdir -p "$GIT_ROOT" "$CLIENT_ROOT" "$NODE_ROOT" "$BACKUP_ROOT"
mount --make-rprivate /
ip link set lo up
ip link add wtbr0 type bridge
ip addr add 10.203.0.1/24 dev wtbr0
ip link set wtbr0 up

GIT_HOLDER="$(start_surface)"
CLIENT_HOLDER="$(start_surface)"
NODE_HOLDER="$(start_surface)"
attach_surface "$GIT_HOLDER" veth-git "$GIT_ADDR"
attach_surface "$CLIENT_HOLDER" veth-client "$CLIENT_ADDR"
attach_surface "$NODE_HOLDER" veth-node "$NODE_ADDR"

nsenter -t "$GIT_HOLDER" -m -- mount -t tmpfs -o mode=0700 tmpfs "$GIT_ROOT"
nsenter -t "$CLIENT_HOLDER" -m -- mount -t tmpfs -o mode=0700 tmpfs "$CLIENT_ROOT"
nsenter -t "$NODE_HOLDER" -m -- mount -t tmpfs -o mode=0700 tmpfs "$NODE_ROOT"

nsenter -t "$GIT_HOLDER" -m -- git -c init.defaultBranch=main init --bare "$GIT_ROOT/transport.git" >/dev/null 2>&1
nsenter -t "$GIT_HOLDER" -m -- git -C "$GIT_ROOT/transport.git" config daemon.receivepack true
nsenter -t "$GIT_HOLDER" -m -- touch "$GIT_ROOT/transport.git/WT_BARE_SENTINEL"

# The bare repository and both clone roots are absent from sibling mount surfaces.
nsenter -t "$GIT_HOLDER" -m -- grep -Fq " $GIT_ROOT " /proc/self/mountinfo
nsenter -t "$CLIENT_HOLDER" -m -- sh -ceu 'test ! -e "$1/transport.git/WT_BARE_SENTINEL"; ! grep -Fq " $1 " /proc/self/mountinfo' sh "$GIT_ROOT"
nsenter -t "$NODE_HOLDER" -m -- sh -ceu 'test ! -e "$1/transport.git/WT_BARE_SENTINEL"; ! grep -Fq " $1 " /proc/self/mountinfo' sh "$GIT_ROOT"
nsenter -t "$CLIENT_HOLDER" -m -- grep -Fq " $CLIENT_ROOT " /proc/self/mountinfo
nsenter -t "$CLIENT_HOLDER" -m -- sh -ceu '! grep -Fq " $1 " /proc/self/mountinfo' sh "$NODE_ROOT"
nsenter -t "$NODE_HOLDER" -m -- grep -Fq " $NODE_ROOT " /proc/self/mountinfo
nsenter -t "$NODE_HOLDER" -m -- sh -ceu '! grep -Fq " $1 " /proc/self/mountinfo' sh "$CLIENT_ROOT"

nsenter -t "$GIT_HOLDER" -n -m -- env \
    WT_GIT_PIDFILE="$WT_RUN_DIR/git-service.pid" WT_GIT_ROOT="$GIT_ROOT" \
    WT_GIT_ADDR="$GIT_ADDR" WT_GIT_PORT="$GIT_PORT" \
    setsid bash -ceu '
        printf "%s\n" "$$" >"$WT_GIT_PIDFILE"
        exec git daemon --verbose --reuseaddr --export-all --enable=receive-pack \
            --base-path="$WT_GIT_ROOT" --listen="$WT_GIT_ADDR" --port="$WT_GIT_PORT" \
            "$WT_GIT_ROOT/transport.git"
    ' >"$GIT_LOG" 2>&1 &
GIT_PID=$!
for _ in $(seq 1 100); do [[ -s "$WT_RUN_DIR/git-service.pid" ]] && break; sleep .02; done
GIT_SERVICE_PID="$(cat "$WT_RUN_DIR/git-service.pid")"
GIT_SERVICE_PGID="$(ps -o pgid= -p "$GIT_SERVICE_PID" | tr -d ' ')"
[[ -n "$GIT_SERVICE_PGID" ]]
for _ in $(seq 1 100); do
    GIT_TERMINAL_PROMPT=0 git ls-remote "$GIT_URL" >/dev/null 2>&1 && break
    sleep .05
done
GIT_TERMINAL_PROMPT=0 git ls-remote "$GIT_URL" >/dev/null

python3 - "$NODE_CFG" "$CLIENT_CFG" "$GIT_URL" "$NODE_ROOT" "$CLIENT_ROOT" "$BACKUP_ROOT" <<'PY'
import json
import os
import sys

node_path, client_path, git_url, node_root, client_root, backup_root = sys.argv[1:]
store = {
    "tokens": [{
        "id": "local-test-bootstrap",
        "platform": "local",
        "kind": "api_key",
        "lifecycle": "embedded",
        "status": "active",
        "value": "deterministic-local-session-key-not-a-secret",
    }],
    "bindings": [{
        "token_id": "local-test-bootstrap",
        "platform": "local",
        "connection_type": "messages",
        "channel_id": "control",
        "role": "discovery",
        "priority": 10,
        "enabled": True,
    }],
}

def carriers(root: str, writer_prefix: str) -> list[dict]:
    return [
        {
            "id": "git.control",
            "carrier_type": "git.repository",
            "role": "discovery",
            "endpoint": {"id": "git.control", "address": "control"},
            "git_repository": {
                "remote_url": git_url,
                "work_dir": os.path.join(root, "control-clone"),
                "writer_id": writer_prefix + "-control",
                "command_timeout_seconds": 2,
            },
        },
        {
            "id": "git.primary",
            "carrier_type": "git.repository",
            "role": "egress",
            "endpoint": {"id": "git.primary", "address": "egress-primary"},
            "git_repository": {
                "remote_url": git_url,
                "work_dir": os.path.join(root, "egress-clone"),
                "writer_id": writer_prefix + "-egress",
                "command_timeout_seconds": 2,
            },
        },
        {
            "id": "file.backup",
            "carrier_type": "file.mailbox",
            "role": "egress",
            "endpoint": {"id": "file.backup", "address": "egress-backup"},
            "file_mailbox": {"dir": backup_root, "allow_egress": True},
        },
    ]

common = {
    "enabled_carriers": ["git.control", "git.primary", "file.backup"],
    "token_store": store,
    "upstream_proxy": {"url": "", "client_egress_only": True, "apply_to_carriers": False},
}
node = {
    **common,
    "role": "node",
    "node_id": "git-failover-node",
    "listen_api": "127.0.0.1:19641",
    "socks_listen": "127.0.0.1:19642",
    "state_file": os.path.join(node_root, "state.json"),
    "carrier_configs": carriers(node_root, "node"),
}
client = {
    **common,
    "role": "client",
    "client_id": "git-failover-client",
    "listen_api": "127.0.0.1:19631",
    "socks_listen": "127.0.0.1:19632",
    "state_file": os.path.join(client_root, "state.json"),
    "carrier_configs": carriers(client_root, "client"),
}
for path, config in ((node_path, node), (client_path, client)):
    with open(path, "w", encoding="utf-8") as handle:
        json.dump(config, handle, separators=(",", ":"))
PY

nsenter -t "$NODE_HOLDER" -n -m -- python3 "$WT_SCRIPT_DIR/git_stream_fixture.py" \
    --port "$TARGET_PORT" --nonce "$PRIMARY_NONCE" --backup-nonce "$BACKUP_NONCE" \
    --stream-marker "$STREAM_MARKER" >"$WT_RUN_DIR/target.log" 2>&1 &
TARGET_PID=$!
nsenter -t "$NODE_HOLDER" -n -m -- env WT_DEBUG=1 HOME="$NODE_ROOT" \
    "$WT_BIN" -config "$NODE_CFG" -serve >"$NODE_LOG" 2>&1 &
NODE_PID=$!
nsenter -t "$CLIENT_HOLDER" -n -m -- env WT_DEBUG=1 HOME="$CLIENT_ROOT" \
    "$WT_BIN" -config "$CLIENT_CFG" -serve >"$CLIENT_LOG" 2>&1 &
CLIENT_PID=$!

wait_client_http /health
CLIENT_START_TICKS="$(awk '{print $22}' "/proc/$CLIENT_PID/stat")"
NODE_START_TICKS="$(awk '{print $22}' "/proc/$NODE_PID/stat")"
for _ in $(seq 1 300); do
    if client_curl --noproxy '*' --max-time 2 -sf "http://127.0.0.1:${CLIENT_API}/v1/nodes" \
        | python3 -c 'import json,sys; raise SystemExit(0 if any(n.get("node_id")=="git-failover-node" and n.get("available") for n in json.load(sys.stdin)) else 1)' 2>/dev/null; then
        break
    fi
    sleep .1
done
client_curl --noproxy '*' -sf -X POST "http://127.0.0.1:${CLIENT_API}/v1/session/connect" \
    -H 'Content-Type: application/json' -d '{"node_id":"git-failover-node"}' >/dev/null
for _ in $(seq 1 300); do
    if client_curl --noproxy '*' --max-time 2 -sf "http://127.0.0.1:${CLIENT_API}/v1/status" \
        | python3 -c 'import json,sys; raise SystemExit(0 if json.load(sys.stdin).get("state")=="connected" else 1)' 2>/dev/null; then
        break
    fi
    sleep .1
done
client_curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API}/v1/status" \
    | python3 -c 'import json,sys; s=json.load(sys.stdin); assert s.get("state")=="connected",s'

# The target exists only in the node network surface.
if client_curl --noproxy '*' --max-time 1 -sf "http://127.0.0.1:${TARGET_PORT}/nonce" >/dev/null 2>&1; then
    printf 'exit target reachable directly from client surface\n' >&2
    exit 1
fi

client_curl --noproxy '*' -sf -X POST "http://127.0.0.1:${CLIENT_API}/v1/session/egress/select" \
    -H 'Content-Type: application/json' -d '{"egress_endpoint_id":"git.primary"}' >/dev/null
FIRST="$(client_curl --noproxy '' --max-time 70 -sf -x "socks5h://127.0.0.1:${CLIENT_SOCKS}" "http://127.0.0.1:${TARGET_PORT}/nonce")"
[[ "$FIRST" == "$PRIMARY_NONCE" ]]
grep -q 'socks5 connected .*route=git.primary' "$CLIENT_LOG"
BACKUP_BEFORE="$(find "$BACKUP_ROOT" -type f | wc -l)"
[[ "$BACKUP_BEFORE" -eq 0 ]]

# Audit the server-visible object database: encrypted envelopes must hide target payloads.
GIT_TERMINAL_PROMPT=0 git clone --mirror "$GIT_URL" "$WT_RUN_DIR/audit.git" >/dev/null 2>&1
git -C "$WT_RUN_DIR/audit.git" fsck --full >/dev/null 2>&1
git -C "$WT_RUN_DIR/audit.git" rev-list --objects --all | awk '{print $1}' \
    | git -C "$WT_RUN_DIR/audit.git" cat-file --batch >"$WT_RUN_DIR/git-objects.dump"
if grep -aFq "$PRIMARY_NONCE" "$WT_RUN_DIR/git-objects.dump" \
    || grep -aFq "http://127.0.0.1:${TARGET_PORT}" "$WT_RUN_DIR/git-objects.dump"; then
    printf 'Git object database exposed plaintext egress payload\n' >&2
    exit 1
fi
GIT_COMMITS="$(git -C "$WT_RUN_DIR/audit.git" rev-list --count --all)"
[[ "$GIT_COMMITS" -gt 0 ]]
grep -q "Connection from ${CLIENT_ADDR}:" "$GIT_LOG"
grep -q "Connection from ${NODE_ADDR}:" "$GIT_LOG"
grep -q 'Request receive-pack' "$GIT_LOG"
grep -q 'Request upload-pack' "$GIT_LOG"

client_curl --no-buffer --noproxy '' --max-time 70 -sf -x "socks5h://127.0.0.1:${CLIENT_SOCKS}" \
    "http://127.0.0.1:${TARGET_PORT}/stream" >"$STREAM_OUT" 2>"$STREAM_ERR" &
STREAM_PID=$!
for _ in $(seq 1 200); do
    nsenter -t "$NODE_HOLDER" -m -- test -f "$STREAM_MARKER" \
        && grep -q 'chunk-0002' "$STREAM_OUT" \
        && [[ "$(grep -c 'socks5 connected .*route=git.primary' "$CLIENT_LOG" || true)" -ge 2 ]] \
        && break
    sleep .1
done
nsenter -t "$NODE_HOLDER" -m -- test -f "$STREAM_MARKER"
grep -q 'chunk-0002' "$STREAM_OUT"

client_curl --noproxy '*' -sf -X POST "http://127.0.0.1:${CLIENT_API}/v1/session/egress/select" \
    -H 'Content-Type: application/json' -d '{"egress_endpoint_id":"auto"}' \
    | python3 -c 'import json,sys; s=json.load(sys.stdin); assert not s.get("selected_egress_endpoint_id") and s.get("automatic_egress_endpoint_id")=="git.primary",s'

FAILURE_STARTED_MS="$(date +%s%3N)"
kill_git_network_processes
stop_git_launcher
if timeout 3s env GIT_TERMINAL_PROMPT=0 git ls-remote "$GIT_URL" >/dev/null 2>&1; then
    printf 'Git daemon remained reachable after outage injection\n' >&2
    exit 1
fi
for _ in $(seq 1 180); do
    kill -0 "$STREAM_PID" 2>/dev/null || break
    sleep .1
done
if kill -0 "$STREAM_PID" 2>/dev/null; then
    printf 'open Git stream exceeded interruption bound\n' >&2
    exit 1
fi
STREAM_STATUS=0
wait "$STREAM_PID" 2>/dev/null || STREAM_STATUS=$?
STREAM_PID=""
[[ "$STREAM_STATUS" -ne 0 ]]
INTERRUPTION_MS="$(( $(date +%s%3N) - FAILURE_STARTED_MS ))"
[[ "$INTERRUPTION_MS" -le 20000 ]]

SECOND="$(client_curl --noproxy '' --max-time 70 -sf -x "socks5h://127.0.0.1:${CLIENT_SOCKS}" "http://127.0.0.1:${TARGET_PORT}/backup")"
[[ "$SECOND" == "$BACKUP_NONCE" ]]
grep -q 'socks5 connected .*route=file.backup' "$CLIENT_LOG"
kill -0 "$CLIENT_PID" 2>/dev/null
kill -0 "$NODE_PID" 2>/dev/null
[[ "$(awk '{print $22}' "/proc/$CLIENT_PID/stat")" == "$CLIENT_START_TICKS" ]]
[[ "$(awk '{print $22}' "/proc/$NODE_PID/stat")" == "$NODE_START_TICKS" ]]
client_curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API}/v1/status" \
    | python3 -c 'import json,sys; s=json.load(sys.stdin); assert s.get("state")=="connected",s'
client_curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API}/v1/carriers" \
    | python3 -c 'import json,sys; c=json.load(sys.stdin); g=c["git.primary"]; assert g.get("lifecycle_state")=="degraded" and g.get("failure_stage")=="runtime" and g.get("error_code")=="io_failure",g; assert c["file.backup"].get("lifecycle_state")=="constructed",c["file.backup"]'
BACKUP_AFTER="$(find "$BACKUP_ROOT" -type f | wc -l)"
[[ "$BACKUP_AFTER" -gt "$BACKUP_BEFORE" ]]
python3 - "$BACKUP_ROOT" <<'PY'
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
records = []
for path in root.glob("*.jsonl"):
    for line in path.read_text(encoding="utf-8").splitlines():
        if line.strip():
            records.append(json.loads(line))
assert records, "file backup contains no envelopes"
classes = {record.get("traffic_class") for record in records}
assert classes == {"egress"}, f"file backup carried non-egress traffic: {classes}"
PY

printf '{"test":"carrier-git-file-failover","exit":0,"controlRoute":"git.control","primaryRoute":"git.primary","backupRoute":"file.backup","primaryNonceValid":true,"backupNonceValid":true,"directTargetReachable":false,"gitProtocolObserved":true,"mountSurfacesIsolated":true,"plaintextPayloadInGit":false,"openStreamInterrupted":true,"interruptionMs":%s,"primaryLifecycle":"degraded","primaryFailureStage":"runtime","daemonRestarted":false,"processIdentityUnchanged":true,"backupTrafficClassesOnlyEgress":true,"gitCommits":%s,"backupFilesBefore":%s,"backupFilesAfter":%s}\n' \
    "$INTERRUPTION_MS" "$GIT_COMMITS" "$BACKUP_BEFORE" "$BACKUP_AFTER" >"$RESULT_FILE"
SUCCESS=true
