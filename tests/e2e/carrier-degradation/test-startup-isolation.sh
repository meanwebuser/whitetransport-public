#!/usr/bin/env bash
# Actual-daemon RED: one missing VK credential must not suppress a valid Yandex lane.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
GO_ROOT="$REPO_ROOT/core/go"
TEST_TMP_ROOT="${WT_TEST_TMP_ROOT:-$REPO_ROOT/.tmp}"
mkdir -p "$TEST_TMP_ROOT"
RUN_DIR="$(mktemp -d "$TEST_TMP_ROOT/wt-carrier-startup-isolation.XXXXXX")"
BIN="$RUN_DIR/whitetransportd"
CFG="$RUN_DIR/client.json"
DAEMON_LOG="$RUN_DIR/daemon.log"
FIXTURE_LOG="$RUN_DIR/yandex-fixture.log"
FIXTURE_STATE="$RUN_DIR/yandex-state"
DAEMON_PID=""
FIXTURE_PID=""
SUCCESS=false

reserve_port() {
    python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}

cleanup() {
    local status=$?
    [[ -z "$DAEMON_PID" ]] || kill "$DAEMON_PID" 2>/dev/null || true
    [[ -z "$FIXTURE_PID" ]] || kill "$FIXTURE_PID" 2>/dev/null || true
    [[ -z "$DAEMON_PID" ]] || wait "$DAEMON_PID" 2>/dev/null || true
    [[ -z "$FIXTURE_PID" ]] || wait "$FIXTURE_PID" 2>/dev/null || true
    if [[ "$SUCCESS" != true ]]; then
        printf 'startup isolation artifacts: %s\n' "$RUN_DIR" >&2
        [[ ! -f "$DAEMON_LOG" ]] || tail -n 40 "$DAEMON_LOG" >&2
    fi
    rm -rf "$RUN_DIR"
    exit "$status"
}
trap cleanup EXIT

API_PORT="$(reserve_port)"
SOCKS_PORT="$(reserve_port)"
FIXTURE_PORT="$(reserve_port)"
mkdir -p "$FIXTURE_STATE"

python3 "$SCRIPT_DIR/yandex_fixture.py" \
    --port "$FIXTURE_PORT" --state-dir "$FIXTURE_STATE" \
    >"$FIXTURE_LOG" 2>&1 &
FIXTURE_PID=$!
for _ in $(seq 1 50); do
    if curl -sf "http://127.0.0.1:${FIXTURE_PORT}/health" >/dev/null; then
        break
    fi
    sleep .05
done
curl -sf "http://127.0.0.1:${FIXTURE_PORT}/health" >/dev/null

python3 - "$CFG" "$API_PORT" "$SOCKS_PORT" "$FIXTURE_PORT" "$RUN_DIR/control" <<'PY'
import json
import sys

path, api_port, socks_port, fixture_port, control_dir = sys.argv[1:]
config = {
    "role": "client",
    "client_id": "startup-isolation-client",
    "listen_api": f"127.0.0.1:{api_port}",
    "socks_listen": f"127.0.0.1:{socks_port}",
    "enabled_carriers": ["local.control", "yandex.primary", "broken.vk"],
    "carrier_configs": [
        {
            "id": "local.control",
            "carrier_type": "file.mailbox",
            "endpoint": {"id": "local.control", "address": "control"},
            "file_mailbox": {"dir": control_dir},
        },
        {
            "id": "yandex.primary",
            "carrier_type": "yandex.disk.files",
            "endpoint": {"id": "yandex.primary", "address": "startup-control"},
            "yandex_disk": {
                "oauth_token": "deterministic-local-yandex-fixture",
                "base_url": f"http://127.0.0.1:{fixture_port}/v1/disk",
                "base_path": "/startup-isolation",
                "cleanup_after_read": True,
            },
        },
        {
            "id": "broken.vk",
            "carrier_type": "vk.messages",
            "endpoint": {"id": "broken.vk", "address": "2000000001"},
            "vk_messages": {},
        },
    ],
    "upstream_proxy": {
        "url": "",
        "client_egress_only": True,
        "apply_to_carriers": False,
    },
}
with open(path, "w", encoding="utf-8") as handle:
    json.dump(config, handle, separators=(",", ":"))
PY

(cd "$GO_ROOT" && GOPROXY=off /usr/local/go/bin/go build -o "$BIN" ./cmd/whitetransportd)
"$BIN" -config "$CFG" -serve >"$DAEMON_LOG" 2>&1 &
DAEMON_PID=$!

for _ in $(seq 1 60); do
    if ! kill -0 "$DAEMON_PID" 2>/dev/null; then
        printf 'daemon exited before degraded health became available\n' >&2
        exit 1
    fi
    if curl -sf "http://127.0.0.1:${API_PORT}/health" >/dev/null; then
        break
    fi
    sleep .1
done

curl -sf "http://127.0.0.1:${API_PORT}/health" >/dev/null
curl -sf "http://127.0.0.1:${API_PORT}/v1/carriers" | python3 -c '
import json, sys
rows=json.load(sys.stdin)
assert "local.control" in rows, rows
assert "yandex.primary" in rows, rows
broken=rows.get("broken.vk")
assert broken is not None, rows
assert broken.get("lifecycle_state") == "degraded", broken
assert broken.get("failure_stage") == "construction", broken
assert broken.get("error_code") == "credential_missing", broken
'
curl -sf "http://127.0.0.1:${API_PORT}/v1/status" | python3 -c '
import json, sys
status=json.load(sys.stdin)
assert status.get("state") == "degraded", status
assert status.get("last_error") == "1 carrier binding(s) unavailable", status
assert "token" not in json.dumps(status).lower(), status
'

SUCCESS=true
printf '{"test":"carrier-startup-isolation","exit":0,"daemonAlive":true,"validCarrier":"yandex.primary","degradedCarrier":"broken.vk"}\n'
