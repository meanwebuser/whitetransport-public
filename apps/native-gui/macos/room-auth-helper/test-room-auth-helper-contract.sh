#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
SOURCE="$ROOT/apps/native-gui/macos/room-auth-helper/RoomAuthHelper.swift"

require() {
  rg -Fq "$1" "$SOURCE" || { echo "missing RoomAuthHelper contract: $1" >&2; exit 1; }
}

require 'WKWebsiteDataStore.nonPersistent()'
require 'https://stream.wb.ru/login'
require 'stream.wb.ru'
require 'wb.ru'
require 'wildberries.ru'
require 'x_wbaas_token'
require 'wb_auth_auth_slice'
require 'FileHandle.standardOutput.write'
require 'decisionHandler(.cancel)'
rg -Fq -- '-parse-as-library' "$ROOT/apps/native-gui/macos/room-auth-helper/build-room-auth-helper.sh" || {
  echo 'missing RoomAuthHelper Swift 6 build mode' >&2
  exit 1
}
rg -Fq 'ROOM_AUTH_HELPER_SOURCE' "$ROOT/apps/native-gui/macos/scripts/package-wails-network-extension.sh" || {
  echo 'missing RoomAuthHelper package resource contract' >&2
  exit 1
}

echo 'RoomAuthHelper contract: OK'
