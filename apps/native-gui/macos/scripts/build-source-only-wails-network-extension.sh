#!/usr/bin/env bash
set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "macOS Network Extension packaging must run on Darwin" >&2
  exit 69
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MACOS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
NATIVE_GUI_DIR="$(cd "$MACOS_DIR/.." && pwd)"
REPO_ROOT="$(cd "$NATIVE_GUI_DIR/../.." && pwd)"
SYMROOT="${1:-${TMPDIR:-/tmp}/wt-macos-products}"
PRODUCTS_DIR="$SYMROOT/Release"
APP_PATH="$NATIVE_GUI_DIR/build/bin/WhiteTransport.app"
DAEMON_BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/wt-wails-daemon.XXXXXX")"
trap 'rm -rf "$DAEMON_BUILD_DIR"' EXIT

if [[ -z "${WT_CLIENT_BOOTSTRAP_FILES:-}" && -z "${WT_CLIENT_TOKEN_STORE:-}" && "${WT_NATIVE_GUI_PROVISIONING_ONLY:-0}" != "1" ]]; then
  echo "source-only client build requires WT_CLIENT_BOOTSTRAP_FILES/WT_CLIENT_TOKEN_STORE; set WT_NATIVE_GUI_PROVISIONING_ONLY=1 for provisioning-only" >&2
  exit 64
fi

# Build the framework first: the following Wails cgo link must resolve its six WTSystemVPN symbols.
xcodebuild -project "$MACOS_DIR/WhiteTransport.xcodeproj" \
  -target WhiteTransportCore -configuration Release \
  CODE_SIGNING_ALLOWED=NO SYMROOT="$SYMROOT" build
xcodebuild -project "$MACOS_DIR/WhiteTransport.xcodeproj" \
  -target WhiteTransportPacketTunnel -configuration Release \
  CODE_SIGNING_ALLOWED=NO SYMROOT="$SYMROOT" build

export CGO_ENABLED=1
export CGO_CFLAGS="${CGO_CFLAGS:+$CGO_CFLAGS }-F$PRODUCTS_DIR"
export CGO_LDFLAGS="${CGO_LDFLAGS:+$CGO_LDFLAGS }-F$PRODUCTS_DIR -framework WhiteTransportMacOS -Wl,-rpath,@executable_path/../Frameworks"

if [[ -n "${WT_WAILS_BIN:-}" ]]; then
  WAILS_BIN="$WT_WAILS_BIN"
elif command -v wails >/dev/null 2>&1; then
  WAILS_BIN="$(command -v wails)"
elif [[ -x "$HOME/go/bin/wails" ]]; then
  # The Mac's non-interactive SSH PATH omits the default `go install` bin.
  WAILS_BIN="$HOME/go/bin/wails"
else
  echo "macOS Wails packaging requires Wails (set WT_WAILS_BIN or install ~/go/bin/wails)" >&2
  exit 70
fi
if [[ ! -x "$WAILS_BIN" ]]; then
  echo "configured Wails binary is not executable: $WAILS_BIN" >&2
  exit 70
fi

GO_BIN="${WT_GO_BIN:-}"
if [[ -z "$GO_BIN" ]] && command -v go >/dev/null 2>&1; then
  GO_BIN="$(command -v go)"
elif [[ -z "$GO_BIN" && -x /usr/local/go/bin/go ]]; then
  GO_BIN=/usr/local/go/bin/go
fi
if [[ -z "$GO_BIN" || ! -x "$GO_BIN" ]]; then
  echo "macOS Wails packaging requires Go (set WT_GO_BIN or install /usr/local/go/bin/go)" >&2
  exit 70
fi
(
  cd "$REPO_ROOT/core/go"
  GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 "$GO_BIN" build -trimpath -o "$DAEMON_BUILD_DIR/whitetransportd" ./cmd/whitetransportd
)
export WT_DAEMON_SOURCE="$DAEMON_BUILD_DIR/whitetransportd"
(
  cd "$NATIVE_GUI_DIR"
  export DYLD_FRAMEWORK_PATH="$PRODUCTS_DIR${DYLD_FRAMEWORK_PATH:+:$DYLD_FRAMEWORK_PATH}"
  "$WAILS_BIN" build -platform darwin/universal
)

"$SCRIPT_DIR/package-wails-network-extension.sh" --source-only "$APP_PATH" "$PRODUCTS_DIR"

if [[ -n "${WT_CLIENT_BOOTSTRAP_FILES:-}" || -n "${WT_CLIENT_TOKEN_STORE:-}" ]]; then
  if [[ -z "${WT_NATIVE_GUI_SING_BOX:-}" ]]; then
    echo "credentialed source-only client build requires WT_NATIVE_GUI_SING_BOX" >&2
    exit 64
  fi
  bash "$NATIVE_GUI_DIR/scripts/post-build-pack.sh" "$APP_PATH"
fi
