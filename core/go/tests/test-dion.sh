#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
GO_ROOT="$REPO_ROOT/core/go"
BINARY="/tmp/wtd-dion"
PROBE="/tmp/socksprobe-dion"
HTTP_PID=""
NODE_PID=""
CLIENT_PID=""
CONNECT_PID=""
NODE_CFG="/tmp/dion-node-config.json"
CLIENT_CFG="/tmp/dion-client-config.json"

cleanup() {
	[[ -n "$NODE_PID" ]] && kill "$NODE_PID" 2>/dev/null || true
	[[ -n "$CLIENT_PID" ]] && kill "$CLIENT_PID" 2>/dev/null || true
	[[ -n "$HTTP_PID" ]] && kill "$HTTP_PID" 2>/dev/null || true
	[[ -n "$CONNECT_PID" ]] && kill "$CONNECT_PID" 2>/dev/null || true
	[[ -n "${NODE_PID:-}" ]] && wait "$NODE_PID" 2>/dev/null || true
	[[ -n "${CLIENT_PID:-}" ]] && wait "$CLIENT_PID" 2>/dev/null || true
	[[ -n "${HTTP_PID:-}" ]] && wait "$HTTP_PID" 2>/dev/null || true
	[[ -n "${CONNECT_PID:-}" ]] && wait "$CONNECT_PID" 2>/dev/null || true
}
trap cleanup EXIT

export PATH="$PATH:/usr/local/go/bin"

if [[ -z "${WT_DION_ACCESS_TOKEN:-}" ]]; then
	WT_DION_ACCESS_TOKEN=$(python3 - "$REPO_ROOT/secrets/production/dion-tokens.json" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    doc = json.load(f)
print(doc['dion']['access_token'])
PYEOF
)
	export WT_DION_ACCESS_TOKEN
fi

if [[ -z "${WT_DION_REFRESH_TOKEN:-}" ]]; then
	WT_DION_REFRESH_TOKEN=$(python3 - "$REPO_ROOT/secrets/production/dion-tokens.json" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    doc = json.load(f)
print(doc['dion']['refresh_token'])
PYEOF
)
	export WT_DION_REFRESH_TOKEN
fi

(cd "$GO_ROOT" && /usr/local/go/bin/go build -o "$BINARY" ./cmd/whitetransportd && /usr/local/go/bin/go build -o "$PROBE" ./cmd/socksprobe)

python3 - "$REPO_ROOT" "$NODE_CFG" "$CLIENT_CFG" <<'PYEOF'
import json, sys
repo = sys.argv[1]
node_cfg = {
  "role": "node",
  "node_id": "dion-node",
  "display_name": "DION Node",
  "listen_api": "127.0.0.1:17781",
  "socks_listen": "127.0.0.1:1181",
  "enabled_carriers": ["file.mailbox", "dion"],
  "carrier_configs": [{
    "id": "file.mailbox",
    "file_mailbox": {"dir": "/tmp/wt-mailbox-dion"},
    "endpoint": {"id": "control", "address": "control"}
  }, {
    "id": "dion",
    "dion": {"event_id": "dion-local", "access_token_env": "WT_DION_ACCESS_TOKEN", "refresh_token_env": "WT_DION_REFRESH_TOKEN", "cookies_file": f"{repo}/secrets/production/dion/dion-cookies.json", "display_name": "DION Node", "role": "creator"},
    "endpoint": {"id": "dion-egress", "address": "dion-local"}
  }],
  "upstream_proxy": {"url": "", "client_egress_only": True, "apply_to_carriers": False}
}
client_cfg = {
  "role": "client",
  "client_id": "dion-client",
  "display_name": "DION Client",
  "listen_api": "127.0.0.1:17782",
  "socks_listen": "127.0.0.1:1180",
  "enabled_carriers": ["file.mailbox", "dion"],
  "carrier_configs": [{
    "id": "file.mailbox",
    "file_mailbox": {"dir": "/tmp/wt-mailbox-dion"},
    "endpoint": {"id": "control", "address": "control"}
  }, {
    "id": "dion",
    "dion": {"event_id": "dion-local", "access_token_env": "WT_DION_ACCESS_TOKEN", "refresh_token_env": "WT_DION_REFRESH_TOKEN", "cookies_file": f"{repo}/secrets/production/dion/dion-cookies.json", "display_name": "DION Client", "role": "joiner"},
    "endpoint": {"id": "dion-egress", "address": "dion-local"}
  }],
  "upstream_proxy": {"url": "", "client_egress_only": True, "apply_to_carriers": False}
}
open(sys.argv[2], 'w').write(json.dumps(node_cfg, indent=2))
open(sys.argv[3], 'w').write(json.dumps(client_cfg, indent=2))
PYEOF

echo "---TEST--- Starting dion nodes..."
rm -rf /tmp/wt-mailbox-dion
mkdir -p /tmp/wt-mailbox-dion
"$BINARY" -config "$NODE_CFG" -serve >/tmp/dion-node.log 2>&1 &
NODE_PID=$!
"$BINARY" -config "$CLIENT_CFG" -serve >/tmp/dion-client.log 2>&1 &
CLIENT_PID=$!
sleep 4

python3 -m http.server 18080 --bind 127.0.0.1 >/tmp/dion-http.log 2>&1 &
HTTP_PID=$!
sleep 1

timeout 15s curl -sf -X POST http://127.0.0.1:17782/v1/session/connect -H 'Content-Type: application/json' -d '{"node_id":"dion-node"}' >/dev/null 2>&1 &
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

wait_for_active /tmp/dion-client.log 'dion-joiner: === TUNNEL CONNECTED ==='
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

run_probe_until_success "DION raw data" "$PROBE" -mode echo -socks 127.0.0.1:1180 -payload dion-raw-data -timeout 6s
run_probe_until_success "DION custom TCP packet" "$PROBE" -mode packet -socks 127.0.0.1:1180 -payload dion-custom-tcp-packet -timeout 6s
run_probe_until_success "DION HTTP" "$PROBE" -mode http -socks 127.0.0.1:1180 -target 127.0.0.1:18080 -host local.test -timeout 6s
