#!/usr/bin/env bash
# A daemon with no surviving carrier stays observable and never dials directly.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
RUN_DIR="$(mktemp -d /tmp/wt-carrier-blocked-startup.XXXXXX)"
BIN="$RUN_DIR/whitetransportd" CFG="$RUN_DIR/client.json" LOG="$RUN_DIR/daemon.log"
DAEMON_PID="" HTTP_PID="" SUCCESS=false

reserve_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }
cleanup() {
    local status=$?
    [[ -z "$DAEMON_PID" ]] || kill "$DAEMON_PID" 2>/dev/null || true
    [[ -z "$HTTP_PID" ]] || kill "$HTTP_PID" 2>/dev/null || true
    [[ -z "$DAEMON_PID" ]] || wait "$DAEMON_PID" 2>/dev/null || true
    [[ -z "$HTTP_PID" ]] || wait "$HTTP_PID" 2>/dev/null || true
    if [[ "$SUCCESS" != true ]]; then printf 'blocked startup artifacts: %s\n' "$RUN_DIR" >&2; [[ ! -f "$LOG" ]] || tail -n 60 "$LOG" >&2; fi
    rm -rf "$RUN_DIR"
    exit "$status"
}
trap cleanup EXIT

API_PORT="$(reserve_port)" SOCKS_PORT="$(reserve_port)" HTTP_PORT="$(reserve_port)"
python3 - "$CFG" "$API_PORT" "$SOCKS_PORT" <<'PY'
import json,sys
path,api,socks=sys.argv[1:]
cfg={"role":"client","client_id":"blocked-client","listen_api":f"127.0.0.1:{api}","socks_listen":f"127.0.0.1:{socks}","enabled_carriers":["broken.vk"],"carrier_configs":[{"id":"broken.vk","carrier_type":"vk.messages","endpoint":{"id":"broken.vk","address":"2000000001"},"vk_messages":{}}],"upstream_proxy":{"url":"","client_egress_only":True,"apply_to_carriers":False}}
json.dump(cfg,open(path,"w",encoding="utf-8"),separators=(",",":"))
PY

python3 - "$HTTP_PORT" <<'PY' >"$RUN_DIR/http.log" 2>&1 &
import http.server,sys
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body=b"direct-target-must-not-be-reached"; self.send_response(200); self.send_header("Content-Length",str(len(body))); self.end_headers(); self.wfile.write(body)
    def log_message(self,*_args): pass
http.server.HTTPServer(("127.0.0.1",int(sys.argv[1])),Handler).serve_forever()
PY
HTTP_PID=$!

(cd "$REPO_ROOT/core/go" && GOPROXY=off /usr/local/go/bin/go build -o "$BIN" ./cmd/whitetransportd)
"$BIN" -config "$CFG" -serve >"$LOG" 2>&1 &
DAEMON_PID=$!
for _ in $(seq 1 100); do
    kill -0 "$DAEMON_PID" 2>/dev/null || { printf 'blocked daemon exited\n' >&2; exit 1; }
    curl --noproxy '*' -sf "http://127.0.0.1:${API_PORT}/health" >/dev/null 2>&1 && break
    sleep .1
done
curl --noproxy '*' -sf "http://127.0.0.1:${API_PORT}/health" >/dev/null
curl --noproxy '*' -sf "http://127.0.0.1:${API_PORT}/v1/status" | python3 -c 'import json,sys; s=json.load(sys.stdin); assert s.get("state")=="blocked" and s.get("last_error")=="no executable control carrier",s'
curl --noproxy '*' -sf "http://127.0.0.1:${API_PORT}/v1/carriers" | python3 -c 'import json,sys; c=json.load(sys.stdin)["broken.vk"]; assert c.get("lifecycle_state")=="degraded" and c.get("error_code")=="credential_missing",c'

if curl --noproxy '' --max-time 3 -sf -x "socks5h://127.0.0.1:${SOCKS_PORT}" "http://127.0.0.1:${HTTP_PORT}/" >/dev/null 2>&1; then
    printf 'blocked SOCKS unexpectedly reached direct target\n' >&2
    exit 1
fi
kill -0 "$DAEMON_PID" 2>/dev/null
grep -q 'no egress dialer configured' "$LOG"

SUCCESS=true
printf '{"test":"carrier-blocked-startup","exit":0,"daemonAlive":true,"state":"blocked","socksDirectFallback":false,"degradedCarrier":"broken.vk"}\n'
