#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

for path in \
    third_party/tun2socks/upstream/go.mod \
    config/dev/local-failover-node.json.template \
    config/dev/local-failover-client.json.template \
    apps/native-gui/public_release_helpers_test.go \
    tools/public_secret_scan.py
do
    test -f "$ROOT_DIR/$path"
done

grep -Fq 'module github.com/xjasonlyu/tun2socks/v2' "$ROOT_DIR/third_party/tun2socks/upstream/go.mod"
python3 "$ROOT_DIR/tools/public_secret_scan.py" "$ROOT_DIR"

echo "public release source contract: PASS"
