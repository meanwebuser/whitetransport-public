#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <WhiteTransport.app>" >&2
  exit 64
fi

APP_PATH="$1"
HELPER="$APP_PATH/Contents/Library/LaunchDaemons/com.meanwebuser.whitetransport.net-helper"
PLIST="$APP_PATH/Contents/Library/LaunchDaemons/com.meanwebuser.whitetransport.net-helper.plist"
test -d "$APP_PATH"
test -f "$PLIST"
test -x "$HELPER" || {
  echo "source-only unsigned: signed SMAppService helper is not packaged" >&2
  exit 69
}

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "source-only unsigned: codesign verification requires macOS" >&2
  exit 69
fi

command -v codesign >/dev/null
codesign --verify --strict --deep "$APP_PATH"
codesign --verify --strict "$HELPER"
app_team="$(codesign -dv --verbose=4 "$APP_PATH" 2>&1 | sed -n 's/^TeamIdentifier=//p' | head -1)"
helper_team="$(codesign -dv --verbose=4 "$HELPER" 2>&1 | sed -n 's/^TeamIdentifier=//p' | head -1)"
if [[ -z "$app_team" || -z "$helper_team" || "$app_team" != "$helper_team" ]]; then
  echo "signed helper/app TeamIdentifier mismatch" >&2
  exit 1
fi
echo "SMAppService bundle signature: PASS ($app_team)"
