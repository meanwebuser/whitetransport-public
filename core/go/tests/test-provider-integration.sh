#!/usr/bin/env bash
# test-provider-integration.sh — isolated real-WBStream egress integration.
# Discovery and session control use only an isolated file.mailbox namespace.
# WBStream is deliberately present only as the egress carrier, so live nodes
# cannot publish advertisements or consume offers from this test.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/provider-test-origin.sh
source "$SCRIPT_DIR/lib/provider-test-origin.sh"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
CORE_GO="$REPO_ROOT/core/go"
CONFIG_DEV="$REPO_ROOT/config/dev"
BINARY_LOCAL="/tmp/whitetransportd-provider-integration"
NODE_PID=""
CLIENT_PID=""
HTTP_PID=""
NODE_LOG="/tmp/node-local-production.log"
CLIENT_LOG="/tmp/client-local-production.log"
MAILBOX="/tmp/wt-mailbox-provider"
NODE_STATE="/tmp/wt-provider-node-cursors.json"
CLIENT_STATE="/tmp/wt-provider-client-cursors.json"
NODE_CFG="/tmp/wt-test-provider-node.json"
CLIENT_CFG="/tmp/wt-test-provider-client.json"

cleanup() {
    echo "[*] Cleaning up..."
    [[ -n "$NODE_PID" ]] && kill "$NODE_PID" 2>/dev/null || true
    [[ -n "$CLIENT_PID" ]] && kill "$CLIENT_PID" 2>/dev/null || true
    stop_provider_test_origin
    wait 2>/dev/null || true
    rm -f "$BINARY_LOCAL" "$NODE_CFG" "$CLIENT_CFG" "$NODE_STATE" "$CLIENT_STATE"
    rm -rf "$MAILBOX"
    echo "[*] Cleanup complete"
}

trap cleanup EXIT

echo "[*] Building daemon for provider integration..."
(cd "$CORE_GO" && go build -o "$BINARY_LOCAL" ./cmd/whitetransportd/)

# --- Generate token store and inject into configs ---
echo "[*] Generating token store from secrets..."
if [[ "${WT_PROVIDER_REFRESH_TOKEN_STORE:-0}" == "1" ]]; then
    echo "[*] Explicit browser-download credential refresh enabled"
    "$REPO_ROOT/secrets/generate-token-store.sh" --fresh-downloads
else
    "$REPO_ROOT/secrets/generate-token-store.sh"
fi

# Create patched configs with token_store block.
python3 "$REPO_ROOT/ops/config/inject-token-store.py" "$CONFIG_DEV/provider-local-node.json" "$NODE_CFG"
python3 "$REPO_ROOT/ops/config/inject-token-store.py" "$CONFIG_DEV/provider-local-client.json" "$CLIENT_CFG"

# Check the generated form: token injection must not accidentally add a live
# mailbox provider or change the isolated file.mailbox namespace.
python3 - "$NODE_CFG" "$CLIENT_CFG" <<'PY'
import json
import sys

expected = {"file.mailbox", "wbstream"}
for path in sys.argv[1:]:
    with open(path) as source:
        cfg = json.load(source)
    if set(cfg.get("enabled_carriers", [])) != expected:
        raise SystemExit(f"{path}: enabled_carriers must be exactly {sorted(expected)}")
    carrier_configs = {item.get("id"): item for item in cfg.get("carrier_configs", [])}
    if set(carrier_configs) != expected:
        raise SystemExit(f"{path}: carrier_configs must be exactly {sorted(expected)}")
    mailbox = carrier_configs["file.mailbox"].get("file_mailbox", {}).get("dir")
    if mailbox != "/tmp/wt-mailbox-provider":
        raise SystemExit(f"{path}: unexpected mailbox namespace {mailbox!r}")
    wbstream = carrier_configs["wbstream"].get("wbstream", {})
    if not wbstream.get("access_token"):
        raise SystemExit(f"{path}: WBStream access token was not injected")
    if not (wbstream.get("cookie_header") or wbstream.get("local_storage_file")):
        raise SystemExit(f"{path}: WBStream session credential was not injected")
print("[*] Provider config isolation and WBStream credential preflight passed")
PY

echo "[*] Built: $BINARY_LOCAL"

# --- Set environment variables ---
export WT_NETWORK_LATENCY_MS="${WT_NETWORK_LATENCY_MS:-0}"
export WT_DEBUG=1  # Enable debug logging
rm -rf "$MAILBOX"
rm -f "$NODE_STATE" "$CLIENT_STATE"

echo "[*] Environment variables:"
echo "  WT_NETWORK_LATENCY_MS=$WT_NETWORK_LATENCY_MS"
echo "  mailbox=$MAILBOX"

# --- Start enhanced node ---
echo "[*] Starting provider node daemon (SOCKS5 on 127.0.0.1:1081, API on 127.0.0.1:17681)..."
"$BINARY_LOCAL" -config "$NODE_CFG" -serve > "$NODE_LOG" 2>&1 &
NODE_PID=$!

echo "[*] Node PID: $NODE_PID"
echo "[*] Waiting 5s for node startup..."
sleep 5

# Check node is alive
if ! kill -0 "$NODE_PID" 2>/dev/null; then
    echo "[!] Node process died. Checking logs:"
    cat "$NODE_LOG"
    exit 1
fi

echo "[✓] Node started successfully"

# --- Start enhanced client ---
echo "[*] Starting provider client daemon (SOCKS5 on 127.0.0.1:1080, API on 127.0.0.1:17682)..."
"$BINARY_LOCAL" -config "$CLIENT_CFG" -serve > "$CLIENT_LOG" 2>&1 &
CLIENT_PID=$!

echo "[*] Client PID: $CLIENT_PID"
echo "[*] Waiting 5s for client startup..."
sleep 5

# Check client is alive
if ! kill -0 "$CLIENT_PID" 2>/dev/null; then
    echo "[!] Client process died. Checking logs:"
    cat "$CLIENT_LOG"
    exit 1
fi

echo "[✓] Client started successfully"

# --- Test enhanced carrier discovery ---
echo "[*] Testing isolated file.mailbox discovery plus real WBStream egress..."
NODES_FOUND=false
for i in $(seq 1 30); do
    NODES=$(curl -sf http://127.0.0.1:17682/v1/nodes 2>/dev/null || echo '[]')
    NODE_COUNT=$(echo "$NODES" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "0")

    if [[ "$NODE_COUNT" -eq 1 ]] && echo "$NODES" | python3 -c 'import json,sys; assert json.load(sys.stdin)[0]["node_id"] == "provider-local-node"'; then
        echo "[✓] Found only provider-local-node after ${i}s: $NODES"
        NODES_FOUND=true
        break
    fi
    sleep 1
done

if [[ "$NODES_FOUND" != "true" ]]; then
    echo "[!] No nodes discovered after 30s."
    echo "[*] Client status:"
    curl -sf http://127.0.0.1:17682/v1/status 2>/dev/null || echo "(unreachable)"
    echo "[*] Node status:"
    curl -sf http://127.0.0.1:17681/v1/status 2>/dev/null || echo "(unreachable)"
    echo "[*] Recent node logs:"
    tail -10 "$NODE_LOG"
    echo "[*] Recent client logs:"
    tail -10 "$CLIENT_LOG"
    exit 1
fi

# --- Test enhanced session connect ---
echo "[*] Connecting to node 'provider-local-node'..."
curl -sf -X POST http://127.0.0.1:17682/v1/session/connect \
    -H 'Content-Type: application/json' \
    -d '{"node_id":"provider-local-node"}' 2>&1 || echo "(connect request returned error)"

echo "[*] Waiting for provider session establishment (up to 60s)..."

# Poll client API for connected state with enhanced carrier support
CONNECTED=false
for i in $(seq 1 60); do
    STATUS=$(curl -sf http://127.0.0.1:17682/v1/status 2>/dev/null || echo '{"state":"unknown"}')
    STATE=$(echo "$STATUS" | python3 -c "import sys,json; print(json.load(sys.stdin).get('state','unknown'))" 2>/dev/null || echo "unknown")
    LAST_ERROR=$(echo "$STATUS" | python3 -c "import sys,json; print(json.load(sys.stdin).get('last_error',''))" 2>/dev/null || echo "")

    if [[ "$STATE" == "connected" ]]; then
        echo "[✓] Provider session connected after ${i}s!"
        echo "[✓] Status: $STATUS"

        if ! echo "$STATUS" | python3 -c '
import json
import sys

status = json.load(sys.stdin)
if not any(endpoint.get("carrier") == "wbstream" for endpoint in status.get("egress_endpoints", [])):
    raise SystemExit("connected session has no wbstream egress endpoint")
'; then
            echo "[!] Provider session connected without a WBStream egress endpoint"
            break
        fi
        echo "[✓] WBStream egress endpoint selected"

        # WBStream must be the egress path; file.mailbox is control only.
        CARRIERS=$(echo "$STATUS" | python3 -c "import sys,json; print(json.dumps(json.load(sys.stdin).get('carriers', {}), indent=2))" 2>/dev/null || echo "{}")
        echo "[✓] Active carriers:"
        echo "$CARRIERS" | grep -E "(file.mailbox|wbstream)" || echo "  No carrier details available"

        CONNECTED=true
        break
    fi
    if [[ "$STATE" == "error" || ( "$STATE" == "disconnected" && -n "$LAST_ERROR" ) ]]; then
        echo "[!] Provider session failed immediately (state=$STATE, last_error=$LAST_ERROR)"
        break
    fi
    # Print progress every 10 seconds for longer timeout
    if (( i % 10 == 0 )); then
        echo "[*] ...still waiting (${i}s, state=$STATE)"
    fi
    sleep 1
done

if [[ "$CONNECTED" != "true" ]]; then
    echo "[!] Provider session did not establish."
    echo "[*] Client status:"
    curl -sf http://127.0.0.1:17682/v1/status 2>/dev/null || echo "(unreachable)"
    echo "[*] Node status:"
    curl -sf http://127.0.0.1:17681/v1/status 2>/dev/null || echo "(unreachable)"
    echo "[*] Enhanced client detailed health:"
    curl -sf http://127.0.0.1:17682/v1/health/detailed 2>/dev/null || echo "(unreachable)"
    exit 1
fi

# Prove payload transfer through the real WBStream session before relying on
# an external service. This has no direct-curl fallback.
start_provider_test_origin "provider-integration"
HTTP_PID="$PROVIDER_ORIGIN_PID"
echo "[*] Test-owned HTTP origin ready on ephemeral port $PROVIDER_ORIGIN_PORT"

echo "[*] Testing SOCKS payload through WBStream to a local HTTP origin..."
CLIENT_DIAL_MARKER="[dataegress] DialContext carrier=wbstream"
NODE_DIAL_MARKER="[dataegress] node connect"
TARGET_MARKER="target=127.0.0.1:${PROVIDER_ORIGIN_PORT}"
CLIENT_DIALS_BEFORE="$(grep -F "$CLIENT_DIAL_MARKER" "$CLIENT_LOG" 2>/dev/null | grep -Fc "$TARGET_MARKER" || true)"
NODE_DIALS_BEFORE="$(grep -F "$NODE_DIAL_MARKER" "$NODE_LOG" 2>/dev/null | grep -Fc "$TARGET_MARKER" || true)"
LOCAL_PAYLOAD="$(curl --noproxy '' -x socks5h://127.0.0.1:1080 -m 15 -sf "http://127.0.0.1:${PROVIDER_ORIGIN_PORT}/")" || {
    echo "[!] Local SOCKS payload test failed"
    echo "[*] Client DialContext evidence for the exact target:"
    grep -F "$CLIENT_DIAL_MARKER" "$CLIENT_LOG" 2>/dev/null | grep -F "$TARGET_MARKER" || echo "  none"
    echo "[*] Node connect evidence for the exact target:"
    grep -F "$NODE_DIAL_MARKER" "$NODE_LOG" 2>/dev/null | grep -F "$TARGET_MARKER" || echo "  none"
    exit 1
}
if [[ "$LOCAL_PAYLOAD" != "$PROVIDER_ORIGIN_NONCE" ]]; then
    echo "[!] Local SOCKS payload did not match the test-owned origin nonce"
    exit 1
fi
kill -0 "$HTTP_PID" 2>/dev/null || {
    echo "[!] Test-owned HTTP origin died during the SOCKS payload probe"
    exit 1
}
CLIENT_DIALS_AFTER="$(grep -F "$CLIENT_DIAL_MARKER" "$CLIENT_LOG" 2>/dev/null | grep -Fc "$TARGET_MARKER" || true)"
NODE_DIALS_AFTER="$(grep -F "$NODE_DIAL_MARKER" "$NODE_LOG" 2>/dev/null | grep -Fc "$TARGET_MARKER" || true)"
if (( CLIENT_DIALS_AFTER <= CLIENT_DIALS_BEFORE || NODE_DIALS_AFTER <= NODE_DIALS_BEFORE )); then
    echo "[!] Exact nonce returned without observable client DialContext and node connect evidence"
    exit 1
fi
echo "[✓] Local SOCKS payload passed through the WBStream session"

echo "[*] Testing external SOCKS egress via 127.0.0.1:1080..."
RESULT=$(curl --noproxy '' -x socks5h://127.0.0.1:1080 -m 20 -sf https://api.ipify.org 2>&1) || {
    echo "[!] External SOCKS egress test failed: $RESULT"
    exit 1
}
echo "[✓] External SOCKS egress passed. Exit IP: $RESULT"

# Failure/failover belongs to the deterministic local multi-carrier suite.
# This test records the real provider's state without pretending it tested it.
echo "[*] Recording provider carrier state..."
CARRIER_INFO=$(curl -sf http://127.0.0.1:17682/v1/carriers 2>/dev/null || echo '{"error":"unreachable"}')
echo "[*] Carrier information:"
echo "$CARRIER_INFO"

# --- Check state persistence ---
if [[ -f "$NODE_STATE" && -f "$CLIENT_STATE" ]]; then
    echo "[✓] Isolated state files persisted"
    echo "[*] Node state bytes: $(wc -c < "$NODE_STATE"), client state bytes: $(wc -c < "$CLIENT_STATE")"
else
    echo "[!] Isolated state files were not both created"
    exit 1
fi

echo ""
echo "[✓] === PROVIDER INTEGRATION TEST PASSED ==="
echo "[✓] WBStream carrier tested with real credentials"
echo "[✓] Discovery/control remained isolated to file.mailbox"
echo "[✓] State persistence working"
echo "[✓] SOCKS5 proxy functional"
echo "[✓] External SOCKS payload verified through the provider path"
echo ""
