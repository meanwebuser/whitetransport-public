#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
scratch_root="$repo_root/.tmp/tests"
mkdir -p "$scratch_root"
test_root="$(mktemp -d "$scratch_root/wt-post-build-windows.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT

app_dir="$test_root/app"
mkdir -p "$app_dir"
printf 'gui' >"$app_dir/WhiteTransport.exe"
printf 'daemon' >"$test_root/whitetransportd-windows-x64.exe"
printf 'wintun' >"$test_root/wintun.dll"
cat >"$test_root/managed-daemon.json" <<'JSON'
{
  "role": "client",
  "client_id": "fixture-client",
  "display_name": "fixture client",
  "listen_api": "127.0.0.1:17680",
  "socks_listen": "127.0.0.1:1085",
  "state_file": "fixture-client-cursors.json",
  "enabled_carriers": ["vk.messages", "wbstream"],
  "routing": {
    "destination_cidrs": [],
    "destination_split": false,
    "dns_servers": ["1.1.1.1"],
    "full_tunnel": true,
    "lan_access": false,
    "mode": "none",
    "mtu": 1500
  },
  "carrier_configs": [
    {
      "id": "vk.messages",
      "endpoint": {"address": "fixture-peer", "id": "vk-control"},
      "vk_messages": {"channels": [{"peer_id": "fixture-peer", "role": "discovery"}]}
    },
    {
      "id": "wbstream",
      "endpoint": {"id": "wbstream-egress"},
      "wbstream": {"display_name": "fixture client"}
    }
  ]
}
JSON
cat >"$test_root/token-store.json" <<'JSON'
{
  "tokens": [
    {"id": "vk-client", "platform": "vk", "kind": "api_key", "lifecycle": "embedded", "status": "active", "value": "fixture-client"},
    {"id": "vk-server", "platform": "vk", "kind": "api_key", "lifecycle": "embedded", "status": "active", "value": "fixture-server"}
  ],
  "bindings": [
    {"token_id": "vk-client", "platform": "vk", "connection_type": "messages", "channel_id": "fixture", "role": "discovery", "priority": 10, "enabled": true},
    {"token_id": "vk-server", "platform": "vk", "connection_type": "messages", "channel_id": "fixture", "role": "node", "priority": 10, "enabled": true}
  ]
}
JSON

WT_NATIVE_GUI_WINTUN_DLL="$test_root/wintun.dll" \
  bash "$repo_root/apps/native-gui/scripts/ensure-wintun.sh" --destination "$app_dir/wintun.dll" >/dev/null
WT_CLIENT_TOKEN_STORE="$test_root/token-store.json" \
WT_NATIVE_GUI_DAEMON_CONFIG="$test_root/managed-daemon.json" \
WT_NATIVE_GUI_WINDOWS_DAEMON_BINARY="$test_root/whitetransportd-windows-x64.exe" \
WT_NATIVE_GUI_WINTUN_DLL="$test_root/wintun.dll" \
  bash "$repo_root/apps/native-gui/scripts/post-build-pack-windows.sh" "$app_dir" >/dev/null

test -f "$app_dir/whitetransportd.exe"
cmp "$test_root/whitetransportd-windows-x64.exe" "$app_dir/whitetransportd.exe"
cmp "$test_root/wintun.dll" "$app_dir/wintun.dll"
test -f "$app_dir/resources/token-store.json"
test -f "$app_dir/resources/daemon.json"
test -f "$app_dir/resources/package-manifest.json"
python3 - "$app_dir/resources/token-store.json" "$app_dir/resources/daemon.json" "$app_dir/resources/package-manifest.json" <<'PY'
import json
import sys

store = json.load(open(sys.argv[1], encoding="utf-8"))
daemon = json.load(open(sys.argv[2], encoding="utf-8"))
manifest = json.load(open(sys.argv[3], encoding="utf-8"))
assert [token["id"] for token in store["tokens"]] == ["vk-client"]
assert [binding["token_id"] for binding in store["bindings"]] == ["vk-client"]
assert daemon["role"] == "client"
assert daemon["token_store"]["tokens"][0]["id"] == "vk-client"
assert daemon["listen_api"] == "127.0.0.1:17680"
assert daemon["socks_listen"] == "127.0.0.1:1085"
assert daemon["enabled_carriers"] == ["vk.messages", "wbstream"]
assert [carrier["id"] for carrier in daemon["carrier_configs"]] == ["vk.messages", "wbstream"]
assert daemon["routing"]["full_tunnel"] is True
assert daemon["routing"]["mode"] == "none"
assert daemon["routing"]["mtu"] == 1500
assert manifest["daemon"] == "../whitetransportd.exe"
assert manifest["singBox"] is None
PY

default_app_dir="$test_root/default-app"
mkdir -p "$default_app_dir"
printf 'gui' >"$default_app_dir/WhiteTransport.exe"
cp "$test_root/wintun.dll" "$default_app_dir/wintun.dll"
WT_CLIENT_TOKEN_STORE="$test_root/token-store.json" \
WT_NATIVE_GUI_WINDOWS_DAEMON_BINARY="$test_root/whitetransportd-windows-x64.exe" \
  bash "$repo_root/apps/native-gui/scripts/post-build-pack-windows.sh" "$default_app_dir" >/dev/null

python3 - "$default_app_dir/resources/daemon.json" <<'PY'
import json
import sys

daemon = json.load(open(sys.argv[1], encoding="utf-8"))
assert daemon["role"] == "client"
assert daemon["listen_api"] == "127.0.0.1:17680"
assert daemon["socks_listen"] == "127.0.0.1:1085"
assert daemon["state_file"] == "cursors.json"
assert daemon["routing"]["full_tunnel"] is True
assert daemon["routing"]["mode"] == "none"
assert daemon["enabled_carriers"]
assert daemon["carrier_configs"]
assert [token["id"] for token in daemon["token_store"]["tokens"]] == ["vk-client"]
PY

echo "Windows post-build resource contract: PASS"
