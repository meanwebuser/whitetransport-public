#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACKAGE_SCRIPT="$SCRIPT_DIR/package-wails-network-extension.sh"

# Credentialed mode must consume only the exact caller-provided files. These
# source-level guards are intentionally Linux-safe; the Darwin topology test
# remains the runtime proof for the complete bundle.
grep -Fq 'usage: $0 --source-only <Wails.app> <xcode-products-dir>' "$PACKAGE_SCRIPT"
grep -Fq -- '--credentialed <Wails.app> <xcode-products-dir> <daemon-binary> <token-store-json> <sing-box>' "$PACKAGE_SCRIPT"
grep -Fq 'DAEMON_SOURCE' "$PACKAGE_SCRIPT"
grep -Fq 'TOKEN_STORE_SOURCE' "$PACKAGE_SCRIPT"
grep -Fq 'SINGBOX_SOURCE' "$PACKAGE_SCRIPT"
grep -Fq 'test -x "$DAEMON_SOURCE"' "$PACKAGE_SCRIPT"
grep -Fq 'test -f "$TOKEN_STORE_SOURCE"' "$PACKAGE_SCRIPT"
grep -Fq 'test -x "$SINGBOX_SOURCE"' "$PACKAGE_SCRIPT"
grep -Fq 'Contents/MacOS/whitetransportd' "$PACKAGE_SCRIPT"
grep -Fq 'chmod 755 "$MACOS_DAEMON_DESTINATION"' "$PACKAGE_SCRIPT"
grep -Fq 'chmod 600 "$MACOS_TOKEN_STORE_DESTINATION"' "$PACKAGE_SCRIPT"
grep -Fq 'DAEMON_CONFIG_DESTINATION' "$PACKAGE_SCRIPT"
grep -Fq '"token_store"' "$PACKAGE_SCRIPT"
if grep -Fq 'token_store_path' "$PACKAGE_SCRIPT"; then
  echo "credentialed package must not rely on token_store_path" >&2
  exit 1
fi
grep -Fq 'chmod 755 "$MACOS_SINGBOX_DESTINATION"' "$PACKAGE_SCRIPT"
grep -Fq 'WT_ALLOW_CREDENTIALS=1' "$PACKAGE_SCRIPT"
grep -Fq 'WT_ALLOW_CREDENTIALS:-0' "$SCRIPT_DIR/verify-wails-bundle.sh"

echo "credentialed Wails package contract: OK"
