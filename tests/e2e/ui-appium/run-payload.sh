#!/usr/bin/env bash
# Run desktop E2E with real whitetransportd payload.
# Extracts tokens from secrets/token-store.json and injects them into
# the Electron + daemon environment so carriers are enabled.
set -euo pipefail
cd "$(dirname "$0")/../../.."
REPO_ROOT="$(pwd)"

TOKEN_STORE="$REPO_ROOT/secrets/token-store.json"
if [ ! -f "$TOKEN_STORE" ]; then
  echo "ERROR: $TOKEN_STORE not found (is git-crypt unlocked?)" >&2
  exit 1
fi

# ── Extract tokens ──────────────────────────────────────────────
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
# VK peer IDs from bindings
disc = [b['channel_id'] for b in d['bindings'] if b['platform']=='vk' and b['role']=='discovery']
ctrl = [b['channel_id'] for b in d['bindings'] if b['platform']=='vk' and b['role']=='node-client']
logs = [b['channel_id'] for b in d['bindings'] if b['platform']=='vk' and b['role']=='logs']
adm  = [b['channel_id'] for b in d['bindings'] if b['platform']=='vk' and b['role']=='admin']
if disc: print(f'export WT_VK_DISCOVERY_PEER_ID={disc[0]!r}')
if ctrl: print(f'export WT_VK_CONTROL_PEER_ID={ctrl[0]!r}')
if logs: print(f'export WT_VK_LOGS_PEER_ID={logs[0]!r}')
if adm:  print(f'export WT_VK_ADMIN_PEER_ID={adm[0]!r}')
# OK chat ID
ok_ctrl = [b['channel_id'] for b in d['bindings'] if b['platform']=='ok' and b['role']=='control' and b['channel_id'] != '*']
if ok_ctrl:
    print(f'export WT_OK_CHAT_ID={ok_ctrl[0]!r}')
")"

# ── Force whitetransportd runtime mode ──────────────────────────
export WT_DESKTOP_RUNTIME_MODE=whitetransportd
export WT_E2E_REQUIRE_RUNTIME=1
export WT_E2E_PLATFORM=desktop

# ── Daemon binary path (used by electron-main.js) ───────────────
export WT_DAEMON_BIN="$REPO_ROOT/artifacts/desktop/whitetransportd-linux-x64"

# ── Use unique ports to avoid conflicts ──────────────────────────
export WT_DESKTOP_SOCKS_PORT="${WT_DESKTOP_SOCKS_PORT:-18809}"
export WT_RUNTIME_API_PORT="${WT_RUNTIME_API_PORT:-17680}"

echo "=== WhiteTransport Desktop Payload E2E ==="
echo "Daemon binary: $WT_DAEMON_BIN"
echo "SOCKS port:    $WT_DESKTOP_SOCKS_PORT"
echo "API port:      $WT_RUNTIME_API_PORT"
echo "VK token:      ${WT_VK_TOKEN:0:20}..."
echo "OK token:      ${WT_OK_TOKEN:+${WT_OK_TOKEN:0:20}...}"
echo "VK discovery:  ${WT_VK_DISCOVERY_PEER_ID:-default}"
echo "==========================================="

# Kill any leftover daemon on our API port
if command -v fuser &>/dev/null; then
  fuser -k "${WT_RUNTIME_API_PORT}/tcp" 2>/dev/null || true
fi

# ── Ensure desktop is built ──────────────────────────────────────
if [ ! -f "$REPO_ROOT/apps/desktop/dist/electron-main.js" ]; then
  echo "Building desktop app..."
  npm --prefix "$REPO_ROOT/apps/desktop" run build
fi

# ── Run WDIO ────────────────────────────────────────────────────
exec npm --prefix "$REPO_ROOT/tests/e2e/ui-appium" run test
