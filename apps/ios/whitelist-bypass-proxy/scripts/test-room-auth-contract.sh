#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
source="$root/whitelist-bypass-proxy/WBStreamRoomAuth.swift"
plugin="$root/whitelist-bypass-proxy/WtTransportPlugin.swift"

test -f "$source"
rg -F 'WKWebsiteDataStore.nonPersistent()' "$source"
rg -F 'https://stream.wb.ru/login' "$source"
rg -F 'x_wbaas_token' "$source"
rg -F 'wb_auth_auth_slice' "$source"
rg -F 'kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly' "$source"
rg -F 'SecItemAdd' "$source"
rg -F 'beginRoomAuth' "$plugin"
rg -F 'getRoomAuthStatus' "$plugin"
