#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 --source-only <Wails.app> <xcode-products-dir>" >&2
  echo "       $0 --credentialed <Wails.app> <xcode-products-dir> <daemon-binary> <token-store-json> <sing-box>" >&2
}

PACKAGE_MODE=""
DAEMON_SOURCE=""
TOKEN_STORE_SOURCE=""
SINGBOX_SOURCE=""
if [[ $# -eq 3 && "$1" == "--source-only" ]]; then
  PACKAGE_MODE=source-only
  APP_PATH="$2"
  PRODUCTS_DIR="$3"
  DAEMON_SOURCE="${WT_DAEMON_SOURCE:-}"
elif [[ $# -eq 6 && "$1" == "--credentialed" ]]; then
  PACKAGE_MODE=credentialed
  APP_PATH="$2"
  PRODUCTS_DIR="$3"
  DAEMON_SOURCE="$4"
  TOKEN_STORE_SOURCE="$5"
  SINGBOX_SOURCE="$6"
  for exact_path in "$APP_PATH" "$PRODUCTS_DIR" "$DAEMON_SOURCE" "$TOKEN_STORE_SOURCE" "$SINGBOX_SOURCE"; do
    if [[ "$exact_path" != /* ]]; then
      echo "credentialed mode requires absolute paths: $exact_path" >&2
      exit 64
    fi
  done
  test -x "$DAEMON_SOURCE"
  test -f "$TOKEN_STORE_SOURCE"
  test -x "$SINGBOX_SOURCE"
else
  usage
  exit 64
fi

if [[ -n "$DAEMON_SOURCE" ]]; then
  if [[ "$DAEMON_SOURCE" != /* ]]; then
    echo "daemon source must be an absolute path: $DAEMON_SOURCE" >&2
    exit 64
  fi
  test -x "$DAEMON_SOURCE"
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
FRAMEWORK_SOURCE="$PRODUCTS_DIR/WhiteTransportMacOS.framework"
APPEX_SOURCE="$PRODUCTS_DIR/WhiteTransportPacketTunnel.appex"
FRAMEWORK_DESTINATION="$APP_PATH/Contents/Frameworks/WhiteTransportMacOS.framework"
APPEX_DESTINATION="$APP_PATH/Contents/PlugIns/WhiteTransportPacketTunnel.appex"
NOTICE_DESTINATION="$APP_PATH/Contents/Resources/third-party-notices/xjasonlyu-tun2socks-MIT.txt"
RESOURCES_DESTINATION="$APP_PATH/Contents/Resources"
DAEMON_CONFIG_DESTINATION="$RESOURCES_DESTINATION/daemon.json"
PROBE_LAUNCHD_DESTINATION="$APP_PATH/Contents/Library/LaunchDaemons"
MACOS_DAEMON_DESTINATION="$APP_PATH/Contents/MacOS/whitetransportd"
MACOS_TOKEN_STORE_DESTINATION="$APP_PATH/Contents/MacOS/resources/token-store.json"
MACOS_SINGBOX_DESTINATION="$APP_PATH/Contents/MacOS/resources/sing-box"

test -x "$APP_PATH/Contents/MacOS/WhiteTransport"
test -d "$FRAMEWORK_SOURCE"
test -d "$APPEX_SOURCE"
test -f "$REPO_ROOT/third_party/tun2socks/upstream/LICENSE"

# Source-only is credential-free, not daemon-free: the managed GUI still needs
# the runtime executable in the bundle. The build script supplies the exact
# daemon path; a direct invocation fails verification if it omits one.
rm -f "$MACOS_TOKEN_STORE_DESTINATION" "$MACOS_SINGBOX_DESTINATION" "$DAEMON_CONFIG_DESTINATION"
if [[ -n "$DAEMON_SOURCE" ]]; then
  mkdir -p "$(dirname "$MACOS_DAEMON_DESTINATION")"
  cp "$DAEMON_SOURCE" "$MACOS_DAEMON_DESTINATION"
  chmod 755 "$MACOS_DAEMON_DESTINATION"
else
  # A source-only postprocess may be rerun over an already built bundle. Keep
  # its daemon unless the caller explicitly supplies a replacement; removing
  # it would make the verifier reject an otherwise valid credential-free app.
  :
fi
mkdir -p "$(dirname "$FRAMEWORK_DESTINATION")" "$(dirname "$APPEX_DESTINATION")" "$(dirname "$NOTICE_DESTINATION")"
rm -rf "$FRAMEWORK_DESTINATION" "$APPEX_DESTINATION"
cp -R "$FRAMEWORK_SOURCE" "$FRAMEWORK_DESTINATION"
cp -R "$APPEX_SOURCE" "$APPEX_DESTINATION"
cp "$REPO_ROOT/third_party/tun2socks/upstream/LICENSE" "$NOTICE_DESTINATION"

if [[ "$PACKAGE_MODE" == credentialed ]]; then
  mkdir -p "$(dirname "$MACOS_TOKEN_STORE_DESTINATION")"
  cp "$DAEMON_SOURCE" "$MACOS_DAEMON_DESTINATION"
  chmod 755 "$MACOS_DAEMON_DESTINATION"
  FILTERED_TOKEN_STORE="$(mktemp "${TMPDIR:-/tmp}/wt-macos-client-store.XXXXXX.json")"
  python3 "$REPO_ROOT/ops/config/filter-client-token-store.py" "$TOKEN_STORE_SOURCE" "$FILTERED_TOKEN_STORE"
  cp "$FILTERED_TOKEN_STORE" "$MACOS_TOKEN_STORE_DESTINATION"
  chmod 600 "$MACOS_TOKEN_STORE_DESTINATION"
  mkdir -p "$RESOURCES_DESTINATION"
  python3 - "$DAEMON_CONFIG_DESTINATION" "$REPO_ROOT" "$FILTERED_TOKEN_STORE" <<'PY'
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(sys.argv[2]) / "ops" / "config"))
from bootstrap_secret import read_bootstrap_secret

payload = {
  "role": "client",
  "token_store": json.loads(Path(sys.argv[3]).read_text(encoding="utf-8")),
}
secret = read_bootstrap_secret()
if secret is not None:
    payload["bootstrap_secret"] = secret
json.dump(payload, open(sys.argv[1], "w", encoding="utf-8"), indent=2)
with open(sys.argv[1], "a", encoding="utf-8") as handle:
    handle.write("\n")
PY
  rm -f "$FILTERED_TOKEN_STORE"
  chmod 600 "$DAEMON_CONFIG_DESTINATION"
  cp "$SINGBOX_SOURCE" "$MACOS_SINGBOX_DESTINATION"
  chmod 755 "$MACOS_SINGBOX_DESTINATION"
fi

# The direct backend is intentionally self-contained. Build both sidecars for
# the target architecture and place them in Contents/Resources so the GUI can
# copy them to the console user's Application Support bin on first use.
SIDECAR_BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/wt-direct-sidecars.XXXXXX")"
trap 'rm -rf "$SIDECAR_BUILD_DIR"' EXIT
DIRECT_HELPER_SOURCE="${WT_DIRECT_HELPER_SOURCE:-}"
TUN2SOCKS_SOURCE="${WT_TUN2SOCKS_SOURCE:-}"
GO_BIN="${WT_GO_BIN:-}"
if [[ -z "$DIRECT_HELPER_SOURCE" || -z "$TUN2SOCKS_SOURCE" ]]; then
  if [[ -z "$GO_BIN" ]] && command -v go >/dev/null 2>&1; then
    GO_BIN="$(command -v go)"
  elif [[ -z "$GO_BIN" && -x /usr/local/go/bin/go ]]; then
    # Non-interactive SSH sessions on the build Mac do not always source the
    # shell profile that adds the standard Go installer path.
    GO_BIN=/usr/local/go/bin/go
  fi
  if [[ -z "$GO_BIN" ]]; then
    echo "macOS Wails packaging requires Go (set WT_GO_BIN or install /usr/local/go/bin/go)" >&2
    exit 70
  fi
  if [[ ! -x "$GO_BIN" ]]; then
    echo "configured Go binary is not executable: $GO_BIN" >&2
    exit 70
  fi
fi
if [[ -z "$DIRECT_HELPER_SOURCE" ]]; then
  (cd "$REPO_ROOT/apps/native-gui/macos/direct-helper" && GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 "$GO_BIN" build -trimpath -o "$SIDECAR_BUILD_DIR/direct-helper" .)
  DIRECT_HELPER_SOURCE="$SIDECAR_BUILD_DIR/direct-helper"
fi
if [[ -z "$TUN2SOCKS_SOURCE" ]]; then
  (cd "$REPO_ROOT/third_party/tun2socks/upstream" && GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 "$GO_BIN" build -trimpath -ldflags='-s -w' -o "$SIDECAR_BUILD_DIR/tun2socks" .)
  TUN2SOCKS_SOURCE="$SIDECAR_BUILD_DIR/tun2socks"
fi
for sidecar in direct-helper tun2socks; do
  if [[ "$sidecar" == "direct-helper" ]]; then
    source_var=DIRECT_HELPER_SOURCE
  else
    source_var=TUN2SOCKS_SOURCE
  fi
  source_path="${!source_var}"
  test -f "$source_path"
  mkdir -p "$RESOURCES_DESTINATION"
  cp "$source_path" "$RESOURCES_DESTINATION/$sidecar"
  chmod 755 "$RESOURCES_DESTINATION/$sidecar"
  test -x "$RESOURCES_DESTINATION/$sidecar"
done

# The SMAppService probe is a signed app-bundled LaunchDaemon. Linux source-
# only packaging keeps the immutable plist for topology checks; the helper
# executable is built on macOS (or supplied explicitly for CI packaging).
mkdir -p "$PROBE_LAUNCHD_DESTINATION"
cp "$REPO_ROOT/apps/native-gui/macos/direct-helper/com.meanwebuser.whitetransport.net-helper.plist" "$PROBE_LAUNCHD_DESTINATION/"
if [[ -n "${WT_SMAPP_HELPER_SOURCE:-}" ]]; then
  if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "refusing arbitrary SMAppService helper source without macOS codesign verification" >&2
    exit 69
  fi
  cp "$WT_SMAPP_HELPER_SOURCE" "$PROBE_LAUNCHD_DESTINATION/com.meanwebuser.whitetransport.net-helper"
elif [[ "$(uname -s)" == "Darwin" && -n "${WT_SMAPP_SIGNING_IDENTITY:-}" ]]; then
  "$REPO_ROOT/apps/native-gui/macos/direct-helper/build-probe-helper.sh" "$PROBE_LAUNCHD_DESTINATION/com.meanwebuser.whitetransport.net-helper"
elif [[ "$(uname -s)" == "Darwin" ]]; then
  echo "[package-wails-network-extension] SMAppService helper deferred to signed build (set WT_SMAPP_SIGNING_IDENTITY)" >&2
else
  echo "[package-wails-network-extension] SMAppService helper executable deferred to macOS build host" >&2
fi
if [[ -f "$PROBE_LAUNCHD_DESTINATION/com.meanwebuser.whitetransport.net-helper" ]]; then
  chmod 755 "$PROBE_LAUNCHD_DESTINATION/com.meanwebuser.whitetransport.net-helper"
fi
if [[ "$(uname -s)" == "Darwin" && -f "$PROBE_LAUNCHD_DESTINATION/com.meanwebuser.whitetransport.net-helper" ]]; then
  "$SCRIPT_DIR/verify-smappservice-bundle.sh" "$APP_PATH"
fi

if [[ "$PACKAGE_MODE" == credentialed ]]; then
  WT_ALLOW_CREDENTIALS=1 "$SCRIPT_DIR/verify-wails-bundle.sh" "$APP_PATH"
else
  "$SCRIPT_DIR/verify-wails-bundle.sh" "$APP_PATH"
fi
