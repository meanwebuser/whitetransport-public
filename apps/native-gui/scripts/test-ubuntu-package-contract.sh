#!/usr/bin/env bash
set -Eeuo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
RUN_DIR="$(mktemp -d /tmp/wt-ubuntu-package-contract.XXXXXX)"
trap 'rm -rf "$RUN_DIR"' EXIT
mkdir -p "$RUN_DIR/input" "$RUN_DIR/output"
printf '#!/bin/sh\necho "WhiteTransport GUI fixture"\n' >"$RUN_DIR/input/WhiteTransport"
printf '#!/bin/sh\necho "whitetransportd 0.1.269 commit=%s date=fixture"\n' "$(git -C "$ROOT_DIR" rev-parse HEAD)" >"$RUN_DIR/input/whitetransportd"
printf '#!/bin/sh\necho "sing-box version 1.13.15"\n' >"$RUN_DIR/input/sing-box"
chmod 0755 "$RUN_DIR/input/WhiteTransport" "$RUN_DIR/input/whitetransportd" "$RUN_DIR/input/sing-box"
python3 - "$RUN_DIR/input/token-store.json" "$RUN_DIR/input/daemon.json" <<'PY'
import json, sys
from pathlib import Path
tokens = [
    {"id": "vk-client", "platform": "vk", "kind": "api_key", "lifecycle": "embedded", "status": "active", "value": "client-placeholder"},
    {"id": "vk-server", "platform": "vk", "kind": "api_key", "lifecycle": "embedded", "status": "active", "value": "server-placeholder"},
]
bindings = [
    {"token_id": "vk-client", "platform": "vk", "connection_type": "messages", "channel_id": "123", "role": "discovery", "priority": 10, "enabled": True},
    {"token_id": "vk-server", "platform": "vk", "connection_type": "messages", "channel_id": "456", "role": "bulk", "priority": 10, "enabled": True},
]
Path(sys.argv[1]).write_text(json.dumps({"tokens": tokens, "bindings": bindings}) + "\n")
Path(sys.argv[2]).write_text(json.dumps({"role":"client","listen_api":"127.0.0.1:17694","socks_listen":"127.0.0.1:18894","state_file":"/tmp/wt-ubuntu-contract-state.json","enabled_carriers":["fixture.messages"],"carrier_configs":[{"id":"fixture.messages","endpoint":{"id":"fixture","address":"fixture"}}]}) + "\n")
PY
WT_UBUNTU_GUI_BINARY="$RUN_DIR/input/WhiteTransport" WT_UBUNTU_DAEMON_BINARY="$RUN_DIR/input/whitetransportd" WT_UBUNTU_DAEMON_CONFIG="$RUN_DIR/input/daemon.json" WT_UBUNTU_SING_BOX_BINARY="$RUN_DIR/input/sing-box" WT_UBUNTU_TOKEN_STORE="$RUN_DIR/input/token-store.json" WT_UBUNTU_PACKAGE_OUTPUT="$RUN_DIR/output" bash "$ROOT_DIR/apps/native-gui/scripts/package-ubuntu.sh"
python3 - "$RUN_DIR/output/package-manifest.json" "$RUN_DIR/output" <<'PY'
import hashlib, json, sys
from pathlib import Path
manifest, root = json.loads(Path(sys.argv[1]).read_text()), Path(sys.argv[2])
assert manifest["schemaVersion"] == 1 and manifest["platform"] == "ubuntu-x64" and manifest["packageFormat"] == "unpacked", manifest
for rel in ("WhiteTransport","whitetransportd-linux-x64","sing-box-linux-x64","resources/token-store.json","resources/daemon.json"): assert (root/rel).is_file(), rel
runtime_store = json.loads((root/"resources/token-store.json").read_text()); daemon = json.loads((root/"resources/daemon.json").read_text())
assert [item["id"] for item in runtime_store["tokens"]] == ["vk-client"], runtime_store
assert [item["token_id"] for item in runtime_store["bindings"]] == ["vk-client"], runtime_store
assert daemon["token_store"] == runtime_store and daemon["listen_api"] == "127.0.0.1:17694" and daemon["socks_listen"] == "127.0.0.1:18894"
assert manifest["tokenStore"]["tokenCount"] == 1 and manifest["tokenStore"]["bindingCount"] == 1
for item in manifest["artifacts"]: assert item["sha256"] == hashlib.sha256((root/item["path"]).read_bytes()).hexdigest(), item
print("ubuntu package contract: OK")
PY
