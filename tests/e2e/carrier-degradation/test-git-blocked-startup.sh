#!/usr/bin/env bash
# Actual-process proof: unreachable Git as the only route leaves an observable blocked daemon.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
RUN_DIR="$(mktemp -d /tmp/wt-git-blocked-startup.XXXXXX)"
BIN="$RUN_DIR/whitetransportd"
CFG="$RUN_DIR/client.json"
LOG="$RUN_DIR/daemon.log"
DAEMON_PID=""
HTTP_PID=""
SUCCESS=false

reserve_port() {
    python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}

cleanup() {
    local status=$?
    [[ -z "$DAEMON_PID" ]] || kill "$DAEMON_PID" 2>/dev/null || true
    [[ -z "$HTTP_PID" ]] || kill "$HTTP_PID" 2>/dev/null || true
    [[ -z "$DAEMON_PID" ]] || wait "$DAEMON_PID" 2>/dev/null || true
    [[ -z "$HTTP_PID" ]] || wait "$HTTP_PID" 2>/dev/null || true
    if [[ "$SUCCESS" != true ]]; then
        printf 'Git blocked-startup artifacts: %s\n' "$RUN_DIR" >&2
        [[ ! -f "$LOG" ]] || tail -n 80 "$LOG" >&2
    fi
    rm -rf "$RUN_DIR"
    exit "$status"
}
trap cleanup EXIT

API_PORT="$(reserve_port)"
SOCKS_PORT="$(reserve_port)"
HTTP_PORT="$(reserve_port)"
CLOSED_GIT_PORT="$(reserve_port)"
DIRECT_NONCE="direct-reachable-${RANDOM}-${RANDOM}"

python3 - "$HTTP_PORT" "$DIRECT_NONCE" <<'PY' >"$RUN_DIR/http.log" 2>&1 &
import http.server
import sys

port, nonce = sys.argv[1:]

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):  # noqa: N802
        body = nonce.encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args):
        return

http.server.HTTPServer(("127.0.0.1", int(port)), Handler).serve_forever()
PY
HTTP_PID=$!
for _ in $(seq 1 100); do
    DIRECT_BODY="$(curl --noproxy '*' --max-time 1 -sf "http://127.0.0.1:${HTTP_PORT}/" 2>/dev/null || true)"
    [[ "$DIRECT_BODY" == "$DIRECT_NONCE" ]] && break
    sleep .05
done
[[ "$(curl --noproxy '*' -sf "http://127.0.0.1:${HTTP_PORT}/")" == "$DIRECT_NONCE" ]]

python3 - "$CFG" "$API_PORT" "$SOCKS_PORT" "$CLOSED_GIT_PORT" "$RUN_DIR/git-control" <<'PY'
import json
import sys

path, api_port, socks_port, git_port, work_dir = sys.argv[1:]
config = {
    "role": "client",
    "client_id": "blocked-git-client",
    "listen_api": f"127.0.0.1:{api_port}",
    "socks_listen": f"127.0.0.1:{socks_port}",
    "enabled_carriers": ["git.control"],
    "carrier_configs": [{
        "id": "git.control",
        "carrier_type": "git.repository",
        "role": "discovery",
        "endpoint": {"id": "git.control", "address": "control"},
        "git_repository": {
            "remote_url": f"git://127.0.0.1:{git_port}/missing.git",
            "work_dir": work_dir,
            "writer_id": "blocked-git-client",
            "command_timeout_seconds": 1,
        },
    }],
    "upstream_proxy": {"url": "", "client_egress_only": True, "apply_to_carriers": False},
}
with open(path, "w", encoding="utf-8") as handle:
    json.dump(config, handle, separators=(",", ":"))
PY

(cd "$REPO_ROOT/core/go" && GOPROXY=off /usr/local/go/bin/go build -o "$BIN" ./cmd/whitetransportd)
"$BIN" -config "$CFG" -serve >"$LOG" 2>&1 &
DAEMON_PID=$!
for _ in $(seq 1 120); do
    kill -0 "$DAEMON_PID" 2>/dev/null || { printf 'blocked Git daemon exited\n' >&2; exit 1; }
    curl --noproxy '*' --max-time 1 -sf "http://127.0.0.1:${API_PORT}/health" >/dev/null 2>&1 && break
    sleep .1
done
curl --noproxy '*' -sf "http://127.0.0.1:${API_PORT}/health" >/dev/null
START_TICKS="$(awk '{print $22}' "/proc/$DAEMON_PID/stat")"

curl --noproxy '*' -sf "http://127.0.0.1:${API_PORT}/v1/status" \
    | python3 -c 'import json,sys; s=json.load(sys.stdin); assert s.get("state")=="blocked" and s.get("last_error")=="no executable control carrier" and not s.get("session_active"),s'
curl --noproxy '*' -sf "http://127.0.0.1:${API_PORT}/v1/carriers" \
    | python3 -c 'import json,sys; c=json.load(sys.stdin)["git.control"]; assert c.get("lifecycle_state")=="degraded" and c.get("failure_stage")=="construction" and c.get("error_code")=="remote_unreachable" and c.get("retryable") is True and c.get("resource_group")=="git.control" and c.get("healthy") is False,c'

if curl --noproxy '' --max-time 3 -sf -x "socks5h://127.0.0.1:${SOCKS_PORT}" \
    "http://127.0.0.1:${HTTP_PORT}/" >/dev/null 2>&1; then
    printf 'blocked Git SOCKS unexpectedly reached direct target\n' >&2
    exit 1
fi
kill -0 "$DAEMON_PID" 2>/dev/null
[[ "$(awk '{print $22}' "/proc/$DAEMON_PID/stat")" == "$START_TICKS" ]]
grep -q 'no egress dialer configured' "$LOG"

SUCCESS=true
printf '{"test":"carrier-git-blocked-startup","exit":0,"daemonAlive":true,"processIdentityUnchanged":true,"state":"blocked","failureCode":"remote_unreachable","retryable":true,"directTargetReachable":true,"socksDirectFallback":false}\n'
