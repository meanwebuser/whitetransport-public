#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/wt-wails-topology.XXXXXX")"
trap 'rm -rf "$TEMP_ROOT"' EXIT

APP_PATH="$TEMP_ROOT/WhiteTransport.app"
PRODUCTS_DIR="$TEMP_ROOT/products"
FRAMEWORK_DIR="$PRODUCTS_DIR/WhiteTransportMacOS.framework"
APPEX_DIR="$PRODUCTS_DIR/WhiteTransportPacketTunnel.appex"
mkdir -p "$APP_PATH/Contents/MacOS/resources" "$FRAMEWORK_DIR/Versions/A" "$APPEX_DIR/Contents/MacOS"
DAEMON_SOURCE="$TEMP_ROOT/whitetransportd"
printf '#!/bin/sh\nexit 0\n' > "$DAEMON_SOURCE"
chmod 755 "$DAEMON_SOURCE"

printf '%s\n' \
  'extern char *WTSystemVPNStatus(void);' \
  'int main(void) { return WTSystemVPNStatus() == 0; }' > "$TEMP_ROOT/app.c"
printf '%s\n' \
  'void WhiteTransportFrameworkMarker(void) {}' \
  'char *WTSystemVPNPermission(void) { return 0; }' \
  'char *WTSystemVPNStart(const char *configuration) { return (char *)configuration; }' \
  'char *WTSystemVPNStop(void) { return 0; }' \
  'char *WTSystemVPNStatus(void) { return 0; }' \
  'char *WTSystemVPNLogs(void) { return 0; }' \
  'void WTSystemVPNFreeCString(char *value) { (void)value; }' > "$TEMP_ROOT/framework.c"
printf '%s\n' \
  'extern void WhiteTransportFrameworkMarker(void);' \
  'int WTStartTun2Socks(void) { WhiteTransportFrameworkMarker(); return 0; }' \
  'int WTStopTun2Socks(void) { return 0; }' \
  'int main(void) { return WTStartTun2Socks(); }' > "$TEMP_ROOT/extension.c"

xcrun clang -dynamiclib "$TEMP_ROOT/framework.c" \
  -install_name '@rpath/WhiteTransportMacOS.framework/Versions/A/WhiteTransportMacOS' \
  -o "$FRAMEWORK_DIR/Versions/A/WhiteTransportMacOS"
ln -s A "$FRAMEWORK_DIR/Versions/Current"
ln -s Versions/Current/WhiteTransportMacOS "$FRAMEWORK_DIR/WhiteTransportMacOS"
xcrun clang "$TEMP_ROOT/app.c" -F "$PRODUCTS_DIR" -framework WhiteTransportMacOS \
  -Wl,-rpath,@executable_path/../Frameworks \
  -o "$APP_PATH/Contents/MacOS/WhiteTransport"
xcrun clang "$TEMP_ROOT/extension.c" -F "$PRODUCTS_DIR" -framework WhiteTransportMacOS \
  -Wl,-rpath,@executable_path/../../../../Frameworks \
  -o "$APPEX_DIR/Contents/MacOS/WhiteTransportPacketTunnel"
plutil -create xml1 "$APPEX_DIR/Contents/Info.plist"
plutil -insert CFBundleIdentifier -string com.meanwebuser.whitetransport.packet-tunnel "$APPEX_DIR/Contents/Info.plist"
printf '%s\n' '{"tokens":[{"value":"must-not-survive"}]}' > "$APP_PATH/Contents/MacOS/resources/token-store.json"

WT_DAEMON_SOURCE="$DAEMON_SOURCE" "$SCRIPT_DIR/package-wails-network-extension.sh" --source-only "$APP_PATH" "$PRODUCTS_DIR"
test ! -e "$APP_PATH/Contents/MacOS/resources/token-store.json"
test -s "$APP_PATH/Contents/Resources/third-party-notices/xjasonlyu-tun2socks-MIT.txt"
test -x "$APP_PATH/Contents/Resources/direct-helper"
test -x "$APP_PATH/Contents/Resources/tun2socks"

cp "$APP_PATH/Contents/MacOS/WhiteTransport" "$TEMP_ROOT/linked-wails"
printf '%s\n' 'int main(void) { return 0; }' > "$TEMP_ROOT/unlinked-app.c"
xcrun clang "$TEMP_ROOT/unlinked-app.c" -o "$APP_PATH/Contents/MacOS/WhiteTransport"
if "$SCRIPT_DIR/verify-wails-bundle.sh" "$APP_PATH" >/dev/null 2>&1; then
  echo "verification unexpectedly accepted a Wails executable that does not import WhiteTransportMacOS" >&2
  exit 1
fi
mv "$TEMP_ROOT/linked-wails" "$APP_PATH/Contents/MacOS/WhiteTransport"

rm "$APP_PATH/Contents/Resources/third-party-notices/xjasonlyu-tun2socks-MIT.txt"
if "$SCRIPT_DIR/verify-wails-bundle.sh" "$APP_PATH" >/dev/null 2>&1; then
  echo "verification unexpectedly accepted a bundle without the MIT notice" >&2
  exit 1
fi
echo "Wails bundle topology regression passed"
