#!/usr/bin/env bash
# Isolated live VK Call DataTunnel + SOCKS proof. Requires an existing call link
# or an explicit peer ID; it never chooses a peer or creates a call implicitly.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
GO_ROOT="$REPO_ROOT/core/go"
source "$SCRIPT_DIR/lib/provider-test-origin.sh"

BINARY="/tmp/wtd-vkcall"
PROBE="/tmp/socksprobe-vkcall"
MAILBOX="/tmp/wt-mailbox-vkcall"
NODE_CFG="/tmp/vkcall-node-config.json"
CLIENT_CFG="/tmp/vkcall-client-config.json"
NODE_PID=""
CLIENT_PID=""
HTTP_PID=""
CONNECT_PID=""

cleanup() {
  [[ -n "$NODE_PID" ]] && kill "$NODE_PID" 2>/dev/null || true
  [[ -n "$CLIENT_PID" ]] && kill "$CLIENT_PID" 2>/dev/null || true
  [[ -n "$HTTP_PID" ]] && kill "$HTTP_PID" 2>/dev/null || true
  [[ -n "$CONNECT_PID" ]] && kill "$CONNECT_PID" 2>/dev/null || true
  [[ -n "$NODE_PID" ]] && wait "$NODE_PID" 2>/dev/null || true
  [[ -n "$CLIENT_PID" ]] && wait "$CLIENT_PID" 2>/dev/null || true
  [[ -n "$HTTP_PID" ]] && wait "$HTTP_PID" 2>/dev/null || true
  [[ -n "$CONNECT_PID" ]] && wait "$CONNECT_PID" 2>/dev/null || true
  stop_provider_test_origin
  rm -f "$NODE_CFG" "$CLIENT_CFG"
  rm -rf "$MAILBOX"
}
trap cleanup EXIT

if [[ -z "${WT_VKCALL_JOIN_LINK:-}" && -z "${WT_VKCALL_PEER_ID:-}" ]]; then
  echo "Set WT_VKCALL_JOIN_LINK to an existing call or WT_VKCALL_PEER_ID to explicitly create one" >&2
  exit 2
fi

export PATH="$PATH:/usr/local/go/bin"
(cd "$GO_ROOT" && /usr/local/go/bin/go build -o "$BINARY" ./cmd/whitetransportd && /usr/local/go/bin/go build -o "$PROBE" ./cmd/socksprobe)

rm -rf "$MAILBOX"
mkdir -p "$MAILBOX"

python3 - "$REPO_ROOT" "$NODE_CFG" "$CLIENT_CFG" <<'PY'
import json, os, sys

root, node_path, client_path = sys.argv[1:]
store = json.load(open(f"{root}/secrets/token-store.json"))
tokens = {item["id"]: item for item in store.get("tokens", []) if isinstance(item, dict)}

def projection(role):
    bindings = [item for item in store.get("bindings", []) if item.get("platform") == "vk" and item.get("connection_type") == "calls" and item.get("role") == role and item.get("enabled")]
    selected = {item["token_id"] for item in bindings}
    return {"tokens": [tokens[token_id] for token_id in sorted(selected)], "bindings": bindings}

link = os.environ.get("WT_VKCALL_JOIN_LINK", "")
peer = os.environ.get("WT_VKCALL_PEER_ID", "")
base = {
    "enabled_carriers": ["file.mailbox", "vkcall"],
    "upstream_proxy": {"url": "", "client_egress_only": True, "apply_to_carriers": False},
}
node_store = projection("node")
client_store = projection("client")
node_token_ids = {item["token_id"] for item in node_store["bindings"]}
client_token_ids = {item["token_id"] for item in client_store["bindings"]}
if node_token_ids & client_token_ids:
    raise SystemExit("VK Call smoke requires distinct node/client TokenStore token IDs")
node = dict(base, role="node", node_id="vkcall-node", display_name="VK Call Node", listen_api="127.0.0.1:17981", socks_listen="127.0.0.1:1381", token_store=node_store)
client = dict(base, role="client", client_id="vkcall-client", display_name="VK Call Client", listen_api="127.0.0.1:17982", socks_listen="127.0.0.1:1380", token_store=client_store)
for cfg, role in ((node, "node"), (client, "client")):
    vk = {"join_link": link, "peer_id": peer if role == "node" else "", "tunnel_mode": "video", "vp8_fps": 2, "vp8_batch": 1}
    cfg["carrier_configs"] = [
        {"id": "file.mailbox", "file_mailbox": {"dir": "/tmp/wt-mailbox-vkcall"}, "endpoint": {"id": "control", "address": "control"}},
        {"id": "vkcall", "vk_call": vk, "endpoint": {"id": "vkcall-egress", "address": "*"}},
    ]
json.dump(node, open(node_path, "w"), separators=(",", ":"))
json.dump(client, open(client_path, "w"), separators=(",", ":"))
PY

"$BINARY" -config "$NODE_CFG" -serve >/tmp/vkcall-node.log 2>&1 & NODE_PID=$!
"$BINARY" -config "$CLIENT_CFG" -serve >/tmp/vkcall-client.log 2>&1 & CLIENT_PID=$!
sleep 4
start_provider_test_origin "vkcall-smoke"; HTTP_PID="$PROVIDER_ORIGIN_PID"
timeout 15s curl -sf -X POST http://127.0.0.1:17982/v1/session/connect -H 'Content-Type: application/json' -d '{"node_id":"vkcall-node"}' >/dev/null & CONNECT_PID=$!

wait_connected() {
  for _ in $(seq 1 60); do
    curl -sf http://127.0.0.1:17982/v1/status | python3 -c 'import json,sys; raise SystemExit(0 if json.load(sys.stdin).get("state")=="connected" else 1)' && return 0
    sleep 1
  done
  return 1
}
run_probe_until_success() {
  local label="$1"; shift
  for attempt in $(seq 1 10); do
    if "$@"; then return 0; fi
    echo "[*] $label attempt $attempt/10 failed; waiting for VK Call DataTunnel..." >&2
    sleep 3
  done
  return 1
}

wait_connected || { echo "VK Call DataTunnel did not become ready" >&2; exit 1; }
run_probe_until_success "VK Call raw data" "$PROBE" -mode echo -socks 127.0.0.1:1380 -payload vkcall-raw-data -timeout 8s
run_probe_until_success "VK Call custom TCP packet" "$PROBE" -mode packet -socks 127.0.0.1:1380 -payload vkcall-custom-tcp-packet -timeout 8s
run_probe_until_success "VK Call HTTP" "$PROBE" -mode http -socks 127.0.0.1:1380 -target "127.0.0.1:${PROVIDER_ORIGIN_PORT}" -host local.test -timeout 8s | grep -F "$PROVIDER_ORIGIN_NONCE"
echo "VKCALL_SMOKE_OK"
