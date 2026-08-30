#!/usr/bin/env bash
set -euo pipefail

# The public snapshot must retain the patched upstream module selected by both
# Go modules; GUI release builds must not depend on a sibling private checkout.
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
UPSTREAM_MODULE="$ROOT_DIR/third_party/tun2socks/upstream/go.mod"

test -f "$UPSTREAM_MODULE"
grep -Fq 'module github.com/xjasonlyu/tun2socks/v2' "$UPSTREAM_MODULE"

# These helpers are referenced by the public synthetic-system-VPN regression
# tests. Keeping them here makes a source-only public checkout testable.
HELPERS="$ROOT_DIR/apps/native-gui/public_release_helpers_test.go"
test -f "$HELPERS"
grep -Fq 'type stubRuntimeService struct' "$HELPERS"
grep -Fq 'func newTestLogSink' "$HELPERS"

# The deterministic daemon integration lane uses only these public,
# file-mailbox fixture templates and must not reach a private config directory.
test -f "$ROOT_DIR/config/dev/local-failover-node.json.template"
test -f "$ROOT_DIR/config/dev/local-failover-client.json.template"

echo "public release source contract: PASS"
