#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACKAGE_SCRIPT="$SCRIPT_DIR/package-wails-network-extension.sh"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/wt-credentialed-package.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT

APP_PATH="$TEST_ROOT/WhiteTransport.app"
PRODUCTS_DIR="$TEST_ROOT/products"
FAKE_BIN="$TEST_ROOT/fake-bin"
DAEMON_SOURCE="$TEST_ROOT/whitetransportd"
TOKEN_SOURCE="$TEST_ROOT/token-store.json"
SINGBOX_SOURCE="$TEST_ROOT/sing-box"
BOOTSTRAP_SECRET_SOURCE="$TEST_ROOT/bootstrap.secret"
GREPCAP="$TEST_ROOT/grep.log"
mkdir -p "$APP_PATH/Contents/MacOS/resources" \
  "$PRODUCTS_DIR/WhiteTransportMacOS.framework/Versions/A" \
  "$PRODUCTS_DIR/WhiteTransportPacketTunnel.appex/Contents/MacOS" "$FAKE_BIN"

printf 'wails\n' > "$APP_PATH/Contents/MacOS/WhiteTransport"
printf 'framework\n' > "$PRODUCTS_DIR/WhiteTransportMacOS.framework/Versions/A/WhiteTransportMacOS"
printf 'appex\n' > "$PRODUCTS_DIR/WhiteTransportPacketTunnel.appex/Contents/MacOS/WhiteTransportPacketTunnel"
printf 'bundle-id\n' > "$PRODUCTS_DIR/WhiteTransportPacketTunnel.appex/Contents/Info.plist"
chmod 755 "$APP_PATH/Contents/MacOS/WhiteTransport" \
  "$PRODUCTS_DIR/WhiteTransportPacketTunnel.appex/Contents/MacOS/WhiteTransportPacketTunnel"
printf 'daemon\n' > "$DAEMON_SOURCE"
cat >"$TOKEN_SOURCE" <<'JSON'
{"tokens":[
  {"id":"vk-client-fixture","platform":"vk","kind":"api_key","lifecycle":"embedded","status":"active","value":"synthetic-client-token"},
  {"id":"server-fixture","platform":"admin","kind":"api_key","lifecycle":"embedded","status":"active","value":"synthetic-server-token"}
],"bindings":[
  {"token_id":"vk-client-fixture","platform":"vk","connection_type":"messages","channel_id":"fixture-peer","role":"discovery","priority":10,"enabled":true},
  {"token_id":"server-fixture","platform":"admin","connection_type":"messages","channel_id":"fixture","role":"admin","priority":10,"enabled":true}
]}
JSON
printf 'sing-box\n' > "$SINGBOX_SOURCE"
chmod 755 "$DAEMON_SOURCE" "$SINGBOX_SOURCE"
printf 'fixture-bootstrap-secret\n' >"$BOOTSTRAP_SECRET_SOURCE"

cat > "$FAKE_BIN/plutil" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == "-extract" ]]; then
  printf 'com.meanwebuser.whitetransport.packet-tunnel\n'
fi
EOF
cat > "$FAKE_BIN/nm" <<'EOF'
#!/usr/bin/env bash
target="${*: -1}"
if [[ "$target" == *WhiteTransportPacketTunnel* ]]; then
  printf '%s\n' _WTStartTun2Socks _WTStopTun2Socks
elif [[ "$target" == *WhiteTransportMacOS.framework* ]]; then
  printf '%s\n' _WTSystemVPNPermission _WTSystemVPNStart _WTSystemVPNStop _WTSystemVPNStatus _WTSystemVPNLogs _WTSystemVPNFreeCString
fi
EOF
cat > "$FAKE_BIN/otool" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' '@rpath/WhiteTransportMacOS.framework/Versions/A/WhiteTransportMacOS'
EOF
chmod 755 "$FAKE_BIN/plutil" "$FAKE_BIN/nm" "$FAKE_BIN/otool"
cat > "$FAKE_BIN/grep" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$WT_GREPCAP"
exec /usr/bin/grep "$@"
EOF
chmod 755 "$FAKE_BIN/grep"

if ! output="$(PATH="$FAKE_BIN:$PATH" WT_GREPCAP="$GREPCAP" WT_DIRECT_HELPER_SOURCE="$DAEMON_SOURCE" WT_TUN2SOCKS_SOURCE="$SINGBOX_SOURCE" \
  WT_BOOTSTRAP_KEY_V2=1 WT_BOOTSTRAP_SECRET_FILE="$BOOTSTRAP_SECRET_SOURCE" \
  "$PACKAGE_SCRIPT" --credentialed "$APP_PATH" "$PRODUCTS_DIR" "$DAEMON_SOURCE" "$TOKEN_SOURCE" "$SINGBOX_SOURCE" 2>&1)"; then
  printf '%s\n' "$output" >&2
  cat "$GREPCAP" >&2 || true
  exit 1
fi
if grep -Fq 'synthetic-server-token' <<<"$output"; then
  echo "credentialed package output leaked token-store content" >&2
  exit 1
fi
cmp -s "$DAEMON_SOURCE" "$APP_PATH/Contents/MacOS/whitetransportd"
grep -Fq 'vk-client-fixture' "$APP_PATH/Contents/MacOS/resources/token-store.json"
if grep -Fq 'server-fixture' "$APP_PATH/Contents/MacOS/resources/token-store.json"; then
  echo "credentialed package leaked server TokenStore principal" >&2
  exit 1
fi
cmp -s "$SINGBOX_SOURCE" "$APP_PATH/Contents/MacOS/resources/sing-box"
test -s "$APP_PATH/Contents/Resources/daemon.json"
python3 - "$APP_PATH/Contents/Resources/daemon.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    config = json.load(handle)
if config.get("role") != "client":
    raise SystemExit("credentialed package daemon config must use role=client")
token_store = config.get("token_store")
if not isinstance(token_store, dict) or not token_store.get("tokens") or not token_store.get("bindings"):
    raise SystemExit("credentialed package daemon config must embed client TokenStore")
if "token_store_path" in config:
    raise SystemExit("credentialed package daemon config must not use token_store_path")
PY
grep -Fq 'bootstrap_secret' "$APP_PATH/Contents/Resources/daemon.json"
file_mode() {
  if [[ "$(uname -s)" == Darwin ]]; then
    stat -f '%Lp' "$1"
  else
    stat -c '%a' "$1"
  fi
}
[[ "$(file_mode "$APP_PATH/Contents/MacOS/whitetransportd")" == 755 ]]
[[ "$(file_mode "$APP_PATH/Contents/MacOS/resources/token-store.json")" == 600 ]]
[[ "$(file_mode "$APP_PATH/Contents/MacOS/resources/sing-box")" == 755 ]]

if PATH="$FAKE_BIN:$PATH" WT_DIRECT_HELPER_SOURCE="$DAEMON_SOURCE" WT_TUN2SOCKS_SOURCE="$SINGBOX_SOURCE" \
  WT_BOOTSTRAP_KEY_V2=1 "$PACKAGE_SCRIPT" --credentialed "$TEST_ROOT/MissingSecret.app" "$PRODUCTS_DIR" "$DAEMON_SOURCE" "$TOKEN_SOURCE" "$SINGBOX_SOURCE" >/dev/null 2>&1; then
  echo "credentialed package accepted v2 without an explicit bootstrap secret" >&2
  exit 1
fi

# Source-only mode must remove credentialed artifacts instead of preserving
# them when reusing an existing Wails bundle directory.
PATH="$FAKE_BIN:$PATH" WT_DIRECT_HELPER_SOURCE="$DAEMON_SOURCE" WT_TUN2SOCKS_SOURCE="$SINGBOX_SOURCE" \
  "$PACKAGE_SCRIPT" --source-only "$APP_PATH" "$PRODUCTS_DIR" >/dev/null 2>&1
test -x "$APP_PATH/Contents/MacOS/whitetransportd"
test ! -e "$APP_PATH/Contents/MacOS/resources/token-store.json"
test ! -e "$APP_PATH/Contents/Resources/daemon.json"
test ! -e "$APP_PATH/Contents/MacOS/resources/sing-box"

echo "credentialed Wails package copy/mode contract: OK"
