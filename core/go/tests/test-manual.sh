#!/usr/bin/env bash
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
DOWNLOADS="$REPO_ROOT/downloads"
BINARY="/tmp/wtd-test"
NODE_LOG="/tmp/node.log"
CLIENT_LOG="/tmp/client.log"
NODE_PID=""
CLIENT_PID=""

cleanup() {
    echo "[*] Cleaning up..."
    [[ -n "$NODE_PID" ]] && kill "$NODE_PID" 2>/dev/null || true
    [[ -n "$CLIENT_PID" ]] && kill "$CLIENT_PID" 2>/dev/null || true
    wait 2>/dev/null || true
}
trap cleanup EXIT

# Extract creds
NODE_CREDS=$(python3 - "$DOWNLOADS/wbauth1.json" "$DOWNLOADS/wbauth1localstorage.json" << 'PYEOF'
import json, sys
with open(sys.argv[1]) as f: cookies = json.load(f)
wanted = {'x_wbaas_token', 'wbx-validation-key', '_wbauid'}
parts = [c['name']+'='+c['value'] for c in cookies if c['name'] in wanted]
with open(sys.argv[2]) as f:
    for line in f:
        if line.startswith('wb_auth_auth_slice\t'):
            obj = json.loads(line.split('\t',1)[1].strip())
            print('; '.join(parts)); print(obj['accessToken']); break
PYEOF
)
NODE_COOKIE=$(echo "$NODE_CREDS" | sed -n '1p')
NODE_TOKEN=$(echo "$NODE_CREDS" | sed -n '2p')

CLIENT_CREDS=$(python3 - "$DOWNLOADS/wbauth2.json" "$DOWNLOADS/wbauth2localstorage.json" << 'PYEOF'
import json, sys
with open(sys.argv[1]) as f: cookies = json.load(f)
wanted = {'x_wbaas_token', 'wbx-validation-key', '_wbauid'}
parts = [c['name']+'='+c['value'] for c in cookies if c['name'] in wanted]
with open(sys.argv[2]) as f:
    for line in f:
        if line.startswith('wb_auth_auth_slice\t'):
            obj = json.loads(line.split('\t',1)[1].strip())
            print('; '.join(parts)); print(obj['accessToken']); break
PYEOF
)
CLIENT_COOKIE=$(echo "$CLIENT_CREDS" | sed -n '1p')
CLIENT_TOKEN=$(echo "$CLIENT_CREDS" | sed -n '2p')

export WB_NODE_ACCESS_TOKEN="$NODE_TOKEN"
export WB_NODE_COOKIE_HEADER="$NODE_COOKIE"
export WB_CLIENT_ACCESS_TOKEN="$CLIENT_TOKEN"
export WB_CLIENT_COOKIE_HEADER="$CLIENT_COOKIE"

# Start node
echo "[*] Starting node..."
"$BINARY" -config "$REPO_ROOT/config/dev/local-node.json" -serve > "$NODE_LOG" 2>&1 &
NODE_PID=$!
echo "[*] Waiting 8s for node startup..."
sleep 8

# Start client
echo "[*] Starting client..."
"$BINARY" -config "$REPO_ROOT/config/dev/local-client.json" -serve > "$CLIENT_LOG" 2>&1 &
CLIENT_PID=$!
echo "[*] Waiting 10s for client startup and discovery..."
sleep 10

# Show discovered nodes
echo "[*] Discovered nodes:"
curl -sf http://127.0.0.1:17682/v1/nodes 2>/dev/null | python3 -m json.tool 2>/dev/null || echo "(none)"

# Connect explicitly to local-node
echo "[*] Connecting to local-node..."
CONNECT_RESP=$(curl -s -X POST http://127.0.0.1:17682/v1/session/connect \
    -H 'Content-Type: application/json' \
    -d '{"node_id":"local-node"}' -m 60 2>&1)
echo "[*] Connect response:"
echo "$CONNECT_RESP" | python3 -m json.tool 2>/dev/null || echo "$CONNECT_RESP"

# Check if connected
STATE=$(echo "$CONNECT_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('state','unknown'))" 2>/dev/null || echo "unknown")
if [[ "$STATE" != "connected" ]]; then
    echo "[!] Not connected. Aborting SOCKS5 test."
    echo "=== NODE LOG ==="
    cat "$NODE_LOG"
    echo ""
    echo "=== CLIENT LOG ==="
    cat "$CLIENT_LOG"
    exit 1
fi

# Wait for WBStream DataChannel to fully establish
echo "[*] Waiting 10s for DataChannel to stabilize..."
sleep 10

# Test SOCKS5 with longer timeout
echo ""
echo "[*] Testing SOCKS5 (30s timeout)..."
RESULT=$(curl -x socks5h://127.0.0.1:1080 -m 30 -sf http://ifconfig.me 2>&1) || {
    echo "[!] SOCKS5 test failed: $RESULT"
    echo "[*] Trying with verbose..."
    curl -v --raw -x socks5h://127.0.0.1:1080 -m 30 http://ifconfig.me 2>&1 || true
}
if [[ -n "$RESULT" && "$RESULT" != *"Received HTTP"* ]]; then
    echo "[*] SOCKS5 SUCCESS! Remote IP: $RESULT"
fi

echo ""
echo "=== NODE LOG (last 40 lines) ==="
tail -40 "$NODE_LOG"
echo ""
echo "=== CLIENT LOG (last 20 lines) ==="
tail -20 "$CLIENT_LOG"
