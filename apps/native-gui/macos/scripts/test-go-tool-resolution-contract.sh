#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACKAGE_SCRIPT="$SCRIPT_DIR/package-wails-network-extension.sh"

# The Mac SSH environment may not expose /usr/local/go/bin on PATH. The
# source-only builder must resolve that standard install location while still
# honoring an explicit WT_GO_BIN override.
grep -Fq 'WT_GO_BIN' "$PACKAGE_SCRIPT"
grep -Fq 'command -v go' "$PACKAGE_SCRIPT"
grep -Fq '/usr/local/go/bin/go' "$PACKAGE_SCRIPT"

BUILD_SCRIPT="$SCRIPT_DIR/build-source-only-wails-network-extension.sh"
grep -Fq 'WT_WAILS_BIN' "$BUILD_SCRIPT"
grep -Fq 'command -v wails' "$BUILD_SCRIPT"
grep -Fq '$HOME/go/bin/wails' "$BUILD_SCRIPT"

# Source-only packaging must remain usable without a signing identity. The
# privileged helper is optional in that lane and is built only when its
# explicit signing override is present.
grep -Fq 'WT_SMAPP_SIGNING_IDENTITY' "$PACKAGE_SCRIPT"
grep -Fq 'deferred to signed build' "$PACKAGE_SCRIPT"

echo "macOS Wails Go tool resolution contract: OK"
