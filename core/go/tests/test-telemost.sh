#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
GO_ROOT="$REPO_ROOT/core/go"
export LC_ALL=C
export TMPDIR="$REPO_ROOT/.tmp"
export GOTMPDIR="$REPO_ROOT/.tmp/go"
mkdir -p "$TMPDIR" "$GOTMPDIR"
RUN_DIR="$(mktemp -d "$TMPDIR/telemost-smoke-XXXXXX")"
BINARY="$RUN_DIR/wtd-telemost"
PROBE="$RUN_DIR/socksprobe-telemost"
HTTP_PID=""
NODE_PID=""
CLIENT_PID=""
CONNECT_PID=""
NODE_CFG="$RUN_DIR/node-config.json"
CLIENT_CFG="$RUN_DIR/client-config.json"
MAILBOX="$RUN_DIR/mailbox"
NODE_LOG="$RUN_DIR/node.log"
CLIENT_LOG="$RUN_DIR/client.log"
HTTP_LOG="$RUN_DIR/http.log"
HTTP_PORT_FILE="$RUN_DIR/http.port"
export WT_TELEMOST_MAILBOX="$MAILBOX"

cleanup() {
	local status=$?
	[[ -n "$NODE_PID" ]] && kill "$NODE_PID" 2>/dev/null || true
	[[ -n "$CLIENT_PID" ]] && kill "$CLIENT_PID" 2>/dev/null || true
	[[ -n "$HTTP_PID" ]] && kill "$HTTP_PID" 2>/dev/null || true
	[[ -n "$CONNECT_PID" ]] && kill "$CONNECT_PID" 2>/dev/null || true
	[[ -n "${NODE_PID:-}" ]] && wait "$NODE_PID" 2>/dev/null || true
	[[ -n "${CLIENT_PID:-}" ]] && wait "$CLIENT_PID" 2>/dev/null || true
	[[ -n "${HTTP_PID:-}" ]] && wait "$HTTP_PID" 2>/dev/null || true
	[[ -n "${CONNECT_PID:-}" ]] && wait "$CONNECT_PID" 2>/dev/null || true
	rm -f "$NODE_CFG" "$CLIENT_CFG"
	rm -rf "$MAILBOX"
	if (( status == 0 )); then
		rm -rf "$RUN_DIR"
	else
		echo "TELEMOST_SMOKE_ARTIFACTS=$RUN_DIR" >&2
	fi
	return "$status"
}
trap cleanup EXIT

export PATH="$PATH:/usr/local/go/bin"

if [[ -z "${WT_TELEMOST_JOIN_LINK:-}" ]]; then
	echo "WT_TELEMOST_JOIN_LINK must name an existing Telemost room for the live smoke" >&2
	exit 2
fi
export WT_TELEMOST_JOIN_LINK

if [[ -z "${WT_YANDEX_COOKIE:-}" ]]; then
	WT_YANDEX_COOKIE=$(python3 - "$REPO_ROOT/secrets/production/yandex/yandex-cookies.json" <<'PYEOF'
import json, os, sys
with open(sys.argv[1]) as f:
    cookies = json.load(f)
print('; '.join(c['name'] + '=' + c['value'] for c in cookies))
PYEOF
)
	export WT_YANDEX_COOKIE
fi

(cd "$GO_ROOT" && /usr/local/go/bin/go build -o "$BINARY" ./cmd/whitetransportd && /usr/local/go/bin/go build -o "$PROBE" ./cmd/socksprobe)

rm -rf "$MAILBOX"
mkdir -p "$MAILBOX"

python3 - "$REPO_ROOT" "$NODE_CFG" "$CLIENT_CFG" <<'PYEOF'
import json, os, sys
repo = sys.argv[1]
node_cfg = {
  "role": "node",
  "node_id": "telemost-node",
  "display_name": "Telemost Node",
  "listen_api": "127.0.0.1:0",
  "socks_listen": "127.0.0.1:0",
  "enabled_carriers": ["file.mailbox", "telemost"],
  "carrier_configs": [{
    "id": "file.mailbox",
    "file_mailbox": {"dir": os.environ["WT_TELEMOST_MAILBOX"]},
    "endpoint": {"id": "control", "address": "control"}
  }, {
    "id": "telemost",
    "telemost": {"join_link": os.environ["WT_TELEMOST_JOIN_LINK"], "cookie_env": "WT_YANDEX_COOKIE", "display_name": "Telemost Node", "role": "creator"},
    "endpoint": {"id": "telemost-egress", "address": "telemost-local"}
  }],
  "upstream_proxy": {"url": "", "client_egress_only": True, "apply_to_carriers": False}
}
client_cfg = {
  "role": "client",
  "client_id": "telemost-client",
  "display_name": "Telemost Client",
  "listen_api": "127.0.0.1:0",
  "socks_listen": "127.0.0.1:0",
  "enabled_carriers": ["file.mailbox", "telemost"],
  "carrier_configs": [{
    "id": "file.mailbox",
    "file_mailbox": {"dir": os.environ["WT_TELEMOST_MAILBOX"]},
    "endpoint": {"id": "control", "address": "control"}
  }, {
    "id": "telemost",
    "telemost": {"join_link": os.environ["WT_TELEMOST_JOIN_LINK"], "display_name": "Telemost Client", "role": "joiner", "cookie_env": "WT_YANDEX_COOKIE"},
    "endpoint": {"id": "telemost-egress", "address": "telemost-local"}
  }],
  "upstream_proxy": {"url": "", "client_egress_only": True, "apply_to_carriers": False}
}
open(sys.argv[2], 'w').write(json.dumps(node_cfg, indent=2))
open(sys.argv[3], 'w').write(json.dumps(client_cfg, indent=2))
PYEOF

echo "---TEST--- Starting telemost nodes..."
"$BINARY" -config "$NODE_CFG" -serve >"$NODE_LOG" 2>&1 &
NODE_PID=$!
"$BINARY" -config "$CLIENT_CFG" -serve >"$CLIENT_LOG" 2>&1 &
CLIENT_PID=$!
sleep 4

python3 - "$HTTP_PORT_FILE" >"$HTTP_LOG" 2>&1 <<'PYEOF' &
import http.server
import pathlib
import sys

port_file = pathlib.Path(sys.argv[1])
class Handler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, format, *args):
        pass
    def do_GET(self):
        body = b"telemost-http-fixture-ok\\n"
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
port_file.write_text(str(server.server_address[1]), encoding="ascii")
server.serve_forever()
PYEOF
HTTP_PID=$!

wait_for_http_port() {
	local deadline=$((SECONDS + 30))
	while (( SECONDS < deadline )); do
		if [[ -s "$HTTP_PORT_FILE" ]]; then
			cat "$HTTP_PORT_FILE"
			return 0
		fi
		sleep 1
	 done
	return 1
}

HTTP_PORT="$(wait_for_http_port)"

wait_for_endpoint() {
	local log_file="$1"
	local marker="$2"
	local deadline=$((SECONDS + 60))
	while (( SECONDS < deadline )); do
		local endpoint=""
		if endpoint="$(python3 - "$log_file" "$marker" <<'PYEOF'
import sys
log_file, marker = sys.argv[1], sys.argv[2]
try:
    with open(log_file, 'r', encoding='utf-8', errors='ignore') as f:
        for line in f:
            if marker in line:
                print(line.split(marker, 1)[1].strip().split()[0])
                raise SystemExit(0)
except FileNotFoundError:
    pass
raise SystemExit(1)
PYEOF
)"; then
			printf '%s\n' "$endpoint"
			return 0
		fi
		sleep 1
	done
	return 1
}

CLIENT_API="$(wait_for_endpoint "$CLIENT_LOG" 'whitetransportd api listening on http://')"
CLIENT_SOCKS="$(wait_for_endpoint "$CLIENT_LOG" 'whitetransportd socks5 listening on ')"

timeout 15s curl -sf -X POST "http://$CLIENT_API/v1/session/connect" -H 'Content-Type: application/json' -d '{"node_id":"telemost-node"}' >/dev/null 2>&1 &
CONNECT_PID=$!

wait_for_active() {
	local log_file="$1"
	local marker="$2"
	local deadline=$((SECONDS + 60))
	while (( SECONDS < deadline )); do
		if python3 - "$log_file" "$marker" <<'PYEOF'
import sys
log_file, marker = sys.argv[1], sys.argv[2]
try:
    with open(log_file, 'r', encoding='utf-8', errors='ignore') as f:
        data = f.read()
except FileNotFoundError:
    sys.exit(1)
sys.exit(0 if marker in data else 1)
PYEOF
		then
			return 0
		fi
		sleep 1
	done
	return 1
}

wait_for_active "$CLIENT_LOG" 'telemost-joiner: === VP8 TUNNEL CONNECTED ==='
sleep 2

run_probe_until_success() {
	local desc="$1"
	shift
	local deadline=$((SECONDS + 30))
	local attempt=1
	while (( SECONDS < deadline )); do
		echo "---TEST--- $desc (attempt $attempt)..."
		if "$@"; then
			return 0
		fi
		attempt=$((attempt + 1))
		sleep 3
	done
	return 1
}

run_probe_until_success "Telemost raw data" "$PROBE" -mode echo -socks "$CLIENT_SOCKS" -payload telemost-raw-data -timeout 20s
run_probe_until_success "Telemost custom TCP packet" "$PROBE" -mode packet -socks "$CLIENT_SOCKS" -payload telemost-custom-tcp-packet -timeout 20s
run_probe_until_success "Telemost HTTP" "$PROBE" -mode http -socks "$CLIENT_SOCKS" -target "127.0.0.1:$HTTP_PORT" -host local.test -timeout 20s

echo "TELEMOST_SMOKE_OK"
