#!/usr/bin/env bash
# Direct daemon test: start daemon, wait for discovery, call connect, capture logs.
set -euo pipefail
cd "$(dirname "$0")/../../.."
REPO_ROOT="$(pwd)"

TOKEN_STORE="$REPO_ROOT/secrets/token-store.json"
DAEMON_BIN="$REPO_ROOT/artifacts/desktop/whitetransportd-linux-x64"

# Extract tokens same as run-payload.sh
eval "$(python3 -c "
import json, sys
with open('$TOKEN_STORE') as f:
    d = json.load(f)
vk = [t for t in d['tokens'] if t['platform']=='vk' and t.get('value','').startswith('vk1.a.')]
ok = [t for t in d['tokens'] if t['platform']=='ok' and t.get('parts',{}).get('access_token','')]
if not vk:
    print('ERROR: No VK tokens found', file=sys.stderr); sys.exit(1)
print(f'export WT_VK_TOKEN={vk[0][\"value\"]!r}')
if ok:
    print(f'export WT_OK_TOKEN={ok[0][\"parts\"][\"access_token\"]!r}')
disc = [b['channel_id'] for b in d['bindings'] if b['platform']=='vk' and b['role']=='discovery']
ctrl = [b['channel_id'] for b in d['bindings'] if b['platform']=='vk' and b['role']=='node-client']
logs = [b['channel_id'] for b in d['bindings'] if b['platform']=='vk' and b['role']=='logs']
adm  = [b['channel_id'] for b in d['bindings'] if b['platform']=='vk' and b['role']=='admin']
if disc: print(f'export WT_VK_DISCOVERY_PEER_ID={disc[0]!r}')
if ctrl: print(f'export WT_VK_CONTROL_PEER_ID={ctrl[0]!r}')
if logs: print(f'export WT_VK_LOGS_PEER_ID={logs[0]!r}')
if adm:  print(f'export WT_VK_ADMIN_PEER_ID={adm[0]!r}')
ok_ctrl = [b['channel_id'] for b in d['bindings'] if b['platform']=='ok' and b['role']=='control' and b['channel_id'] != '*']
if ok_ctrl:
    print(f'export WT_OK_CHAT_ID={ok_ctrl[0]!r}')
")"

# Generate daemon config using electron-main's approach
# We'll use a simpler config that just has the essential carriers
DAEMON_DIR=$(mktemp -d /tmp/wt-direct-test-XXXXXX)

# Build config with token store
python3 -c "
import json

with open('$TOKEN_STORE') as f:
    ts = json.load(f)

def first_binding(platform, role, connection_type=None):
    for binding in ts['bindings']:
        if binding.get('platform') != platform or binding.get('role') != role:
            continue
        if connection_type and binding.get('connection_type') != connection_type:
            continue
        channel_id = binding.get('channel_id', '')
        if channel_id and channel_id != '*':
            return channel_id
    return ''

vk_discovery_peer = first_binding('vk', 'discovery', 'messages')

# Build carrier configs from token store
carrier_configs = []
carrier_ids = set()

for b in ts['bindings']:
    if b['platform'] == 'vk' and b['connection_type'] == 'messages':
        carrier_ids.add('vk.messages')
    elif b['platform'] == 'vk' and b['connection_type'] in ('docs.256', 'docs.1024'):
        carrier_ids.add(f'vk.{b[\"connection_type\"]}')
    elif b['platform'] == 'ok' and b['connection_type'] == 'messages':
        carrier_ids.add('ok.messages')

# Build vk.messages config
vk_channels = []
for b in ts['bindings']:
    if b['platform'] == 'vk' and b['connection_type'] == 'messages' and b.get('channel_id', '') != '*':
        vk_channels.append({'peer_id': b['channel_id'], 'role': b['role']})

if 'vk.messages' in carrier_ids:
    carrier_configs.append({
        'id': 'vk.messages',
        'token_ref': 'vk-example-community',
        'vk_messages': {'channels': vk_channels},
        'endpoint': {'id': 'vk-discovery', 'address': vk_discovery_peer}
    })

for doc_type in ['vk.docs.256', 'vk.docs.1024']:
    if doc_type in carrier_ids:
        parts = doc_type.split('.')
        carrier_configs.append({
            'id': doc_type,
            'token_ref': 'vk-example-community',
            'vk_docs': {},
            'endpoint': {'id': 'vk-bulk-' + parts[1], 'address': vk_discovery_peer}
        })

if 'ok.messages' in carrier_ids:
    carrier_configs.append({
        'id': 'ok.messages',
        'token_ref': 'ok-set-1',
        'ok_messages': {},
        'endpoint': {'id': 'ok-control', 'address': '${WT_OK_CHAT_ID:-*}'}
    })

config = {
    'role': 'client',
    'node_id': '',
    'listen_api': '127.0.0.1:17680',
    'socks_listen': '127.0.0.1:18809',
    'enabled_carriers': sorted(carrier_ids),
    'carrier_configs': carrier_configs,
    'token_store': ts
}

with open('$DAEMON_DIR/daemon.json', 'w') as f:
    json.dump(config, f, indent=2)
print(f'Config written to $DAEMON_DIR/daemon.json')
print(f'Enabled carriers: {sorted(carrier_ids)}')
"

echo "=== Starting daemon directly ==="
DAEMON_LOG="$DAEMON_DIR/daemon.log"
"$DAEMON_BIN" --config "$DAEMON_DIR/daemon.json" --serve >"$DAEMON_LOG" 2>&1 &
DAEMON_PID=$!
echo "Daemon PID: $DAEMON_PID (log: $DAEMON_LOG)"

# Wait for daemon to start
for i in $(seq 1 20); do
  if curl -s http://127.0.0.1:17680/v1/status >/dev/null 2>&1; then
    echo "Daemon API ready after ${i}s"
    break
  fi
  sleep 1
done

# Check status
echo "=== Initial status ==="
curl -s http://127.0.0.1:17680/v1/status | python3 -m json.tool

# Wait for node discovery
echo "=== Waiting for nodes (30s) ==="
for i in $(seq 1 30); do
  NODES=$(curl -s http://127.0.0.1:17680/v1/nodes 2>/dev/null || echo "[]")
  COUNT=$(echo "$NODES" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "0")
  if [ "$COUNT" -gt 0 ]; then
    echo "Found $COUNT node(s) after ${i}s:"
    echo "$NODES" | python3 -m json.tool
    break
  fi
  sleep 1
done

# Call connect and capture response
echo "=== Calling connect ==="
CONNECT_RESP=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X POST http://127.0.0.1:17680/v1/session/connect \
  -H 'Content-Type: application/json' \
  -d '{}' \
  --max-time 120 2>&1)
echo "$CONNECT_RESP"

echo "=== Final status ==="
curl -s http://127.0.0.1:17680/v1/status | python3 -m json.tool 2>/dev/null || true

# Print daemon log
echo ""
echo "=== Daemon log (last 80 lines) ==="
tail -80 "$DAEMON_LOG" 2>/dev/null || echo "(no log)"

# Cleanup
kill $DAEMON_PID 2>/dev/null || true
wait $DAEMON_PID 2>/dev/null || true
echo ""
echo "=== Daemon log dir preserved at: $DAEMON_DIR ==="
echo "=== Done ==="
