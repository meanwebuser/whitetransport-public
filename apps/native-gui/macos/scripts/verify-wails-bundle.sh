#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <WhiteTransport.app>" >&2
  exit 64
fi

APP_PATH="$1"
APP_BINARY="$APP_PATH/Contents/MacOS/WhiteTransport"
FRAMEWORK_BINARY="$APP_PATH/Contents/Frameworks/WhiteTransportMacOS.framework/Versions/A/WhiteTransportMacOS"
APPEX_PATH="$APP_PATH/Contents/PlugIns/WhiteTransportPacketTunnel.appex"
APPEX_BINARY="$APPEX_PATH/Contents/MacOS/WhiteTransportPacketTunnel"
NOTICE="$APP_PATH/Contents/Resources/third-party-notices/xjasonlyu-tun2socks-MIT.txt"
DIRECT_HELPER="$APP_PATH/Contents/Resources/direct-helper"
TUN2SOCKS="$APP_PATH/Contents/Resources/tun2socks"
DAEMON="$APP_PATH/Contents/MacOS/whitetransportd"

test -x "$APP_BINARY"
test -f "$FRAMEWORK_BINARY"
test -x "$APPEX_BINARY"
test -s "$NOTICE"
test -x "$DIRECT_HELPER"
test -x "$TUN2SOCKS"
test -x "$DAEMON"
if [[ "${WT_ALLOW_CREDENTIALS:-0}" == "1" ]]; then
  CREDENTIAL_TOKEN_STORE="$APP_PATH/Contents/MacOS/resources/token-store.json"
  test -f "$CREDENTIAL_TOKEN_STORE"
  if [[ "$(uname -s)" == "Darwin" ]]; then
    credential_mode="$(stat -f '%Lp' "$CREDENTIAL_TOKEN_STORE")"
  else
    credential_mode="$(stat -c '%a' "$CREDENTIAL_TOKEN_STORE")"
  fi
  [[ "$credential_mode" == 600 ]]
  CREDENTIAL_DAEMON_CONFIG="$APP_PATH/Contents/Resources/daemon.json"
  test -f "$CREDENTIAL_DAEMON_CONFIG"
  python3 - "$CREDENTIAL_DAEMON_CONFIG" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    config = json.load(handle)
if config.get("role") != "client":
    raise SystemExit("credentialed bundle daemon config must use role=client")
token_store = config.get("token_store")
if not isinstance(token_store, dict):
    raise SystemExit("credentialed bundle daemon config must embed client TokenStore")
if not token_store.get("tokens") or not token_store.get("bindings"):
    raise SystemExit("credentialed bundle daemon config must contain client tokens and bindings")
if "token_store_path" in config:
    raise SystemExit("credentialed bundle daemon config must not depend on a relative token_store_path")
PY
else
  test ! -e "$APP_PATH/Contents/MacOS/resources/token-store.json"
  test ! -e "$APP_PATH/Contents/Resources/daemon.json"
fi
test "$(plutil -extract CFBundleIdentifier raw "$APPEX_PATH/Contents/Info.plist")" = "com.meanwebuser.whitetransport.packet-tunnel"

nm -gU "$APPEX_BINARY" | grep -q '_WTStartTun2Socks'
nm -gU "$APPEX_BINARY" | grep -q '_WTStopTun2Socks'
for symbol in \
  WTSystemVPNPermission \
  WTSystemVPNStart \
  WTSystemVPNStop \
  WTSystemVPNStatus \
  WTSystemVPNLogs \
  WTSystemVPNFreeCString; do
  if ! nm -gU "$FRAMEWORK_BINARY" | grep -q "_$symbol"; then
    echo "WhiteTransportMacOS framework does not export $symbol" >&2
    exit 1
  fi
done
if nm -gU "$APP_BINARY" | grep -q '_WTStartTun2Socks'; then
  echo "tun2socks C ABI leaked into the Wails app executable" >&2
  exit 1
fi
if nm -gU "$FRAMEWORK_BINARY" | grep -q '_WTStartTun2Socks'; then
  echo "tun2socks C ABI leaked into the shared framework" >&2
  exit 1
fi
otool -L "$APPEX_BINARY" | grep -q '@rpath/WhiteTransportMacOS.framework/Versions/A/WhiteTransportMacOS'
if ! otool -L "$APP_BINARY" | grep -q '@rpath/WhiteTransportMacOS.framework/Versions/A/WhiteTransportMacOS'; then
  echo "Wails executable does not import WhiteTransportMacOS.framework" >&2
  exit 1
fi

echo "verified Wails bundle: $APP_PATH"
echo "framework: Contents/Frameworks/WhiteTransportMacOS.framework"
echo "Wails link: @rpath/WhiteTransportMacOS.framework/Versions/A/WhiteTransportMacOS"
echo "extension: Contents/PlugIns/WhiteTransportPacketTunnel.appex"
echo "engine: Contents/PlugIns/WhiteTransportPacketTunnel.appex/Contents/MacOS/WhiteTransportPacketTunnel"
echo "notice: Contents/Resources/third-party-notices/xjasonlyu-tun2socks-MIT.txt"
echo "direct helper: Contents/Resources/direct-helper"
echo "tun2socks sidecar: Contents/Resources/tun2socks (darwin-arm64)"
echo "daemon: Contents/MacOS/whitetransportd"
