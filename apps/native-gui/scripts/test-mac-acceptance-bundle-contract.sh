#!/usr/bin/env bash
# Linux-safe contract for rejecting Electron artifacts before remote execution.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WRAPPER="${WRAPPER:-$ROOT_DIR/apps/native-gui/scripts/mac-acceptance.sh}"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/wt-mac-bundle-contract.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT
touch "$TEST_ROOT/known_hosts" "$TEST_ROOT/identity"
export WT_MAC_TARGET=mac-mini-2012
export WT_MAC_MINI_KNOWN_HOSTS_FILE="$TEST_ROOT/known_hosts"
export WT_MAC_MINI_IDENTITY_FILE="$TEST_ROOT/identity"

APP="$TEST_ROOT/Electron.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
printf '#!/bin/sh\n' >"$APP/Contents/MacOS/WhiteTransport"
printf 'electron fixture\n' >"$APP/Contents/Resources/app.asar"
chmod 0755 "$APP/Contents/MacOS/WhiteTransport"

FAKE_SSH="$TEST_ROOT/ssh"
cat >"$FAKE_SSH" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
: >"${WT_MAC_BUNDLE_SSH_CALLED:?}"
exit 1
SH
chmod 0755 "$FAKE_SSH"

RESULT="$TEST_ROOT/result.json"
if WT_MAC_SSH_TARGET=test@mac WT_MAC_SSH_BIN="$FAKE_SSH" \
  WT_MAC_BUNDLE_SSH_CALLED="$TEST_ROOT/ssh-called" WT_MAC_RESULT="$RESULT" \
  bash "$WRAPPER" --bundle "$APP" --node node-test; then
  echo "Electron-shaped bundle was unexpectedly accepted" >&2
  exit 1
fi

python3 - "$RESULT" <<'PY'
import json
import pathlib
import sys

result = json.loads(pathlib.Path(sys.argv[1]).read_text())
assert result["passed"] is False, result
assert "native Wails bundle required" in result["error"], result
PY
if [[ -e "$TEST_ROOT/ssh-called" ]]; then
  echo "bundle preflight ran remote SSH before rejecting Electron artifact" >&2
  exit 1
fi
echo "mac acceptance bundle contract: OK"

WAILS_APP="$TEST_ROOT/WhiteTransport.app"
mkdir -p "$WAILS_APP/Contents/MacOS/resources"
printf '#!/bin/sh\n' >"$WAILS_APP/Contents/MacOS/WhiteTransport"
printf '#!/bin/sh\n' >"$WAILS_APP/Contents/MacOS/whitetransportd"
printf 'token-store fixture\n' >"$WAILS_APP/Contents/MacOS/resources/token-store.json"
printf '#!/bin/sh\n' >"$WAILS_APP/Contents/MacOS/resources/sing-box"
chmod 0755 "$WAILS_APP/Contents/MacOS/WhiteTransport" "$WAILS_APP/Contents/MacOS/whitetransportd" "$WAILS_APP/Contents/MacOS/resources/sing-box"

WAILS_RESULT="$TEST_ROOT/wails-result.json"
WAILS_CALLED="$TEST_ROOT/wails-ssh-called"
if WT_MAC_SSH_TARGET=test@mac WT_MAC_SSH_BIN="$FAKE_SSH" \
  WT_MAC_BUNDLE_SSH_CALLED="$WAILS_CALLED" WT_MAC_RESULT="$WAILS_RESULT" \
  bash "$WRAPPER" --bundle "$WAILS_APP" --node node-test; then
  echo "Wails-shaped bundle unexpectedly completed against failing SSH" >&2
  exit 1
fi

python3 - "$WAILS_RESULT" <<'PY'
import json
import pathlib
import sys

result = json.loads(pathlib.Path(sys.argv[1]).read_text())
assert result["passed"] is False, result
assert "Mac is unavailable" in result["error"], result
PY
test -e "$WAILS_CALLED"
echo "mac acceptance native Wails bundle layout: OK"
