#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACKAGE_SCRIPT="$SCRIPT_DIR/package-wails-network-extension.sh"
BUILD_SCRIPT="$SCRIPT_DIR/build-source-only-wails-network-extension.sh"

# A source-only bundle is credential-free, not daemon-free: the installed GUI
# must carry the runtime binary so a clean package can start its managed API.
grep -Fq 'WT_DAEMON_SOURCE' "$PACKAGE_SCRIPT"
grep -Fq 'cp "$DAEMON_SOURCE" "$MACOS_DAEMON_DESTINATION"' "$PACKAGE_SCRIPT"
grep -Fq 'WT_DAEMON_SOURCE=' "$BUILD_SCRIPT"
grep -Fq 'WT_CLIENT_BOOTSTRAP_FILES' "$BUILD_SCRIPT"
grep -Fq 'WT_NATIVE_GUI_PROVISIONING_ONLY' "$BUILD_SCRIPT"
grep -Fq 'GOOS=darwin GOARCH=arm64' "$BUILD_SCRIPT"
grep -Fq './cmd/whitetransportd' "$BUILD_SCRIPT"

echo "source-only daemon packaging contract: OK"
