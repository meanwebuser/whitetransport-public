#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
SOURCE="$ROOT/apps/native-gui/macos/room-auth-helper/RoomAuthHelper.swift"
OUTPUT="${1:-$ROOT/apps/native-gui/macos/room-auth-helper/RoomAuthHelper}"

command -v swiftc >/dev/null 2>&1 || { echo 'swiftc is required' >&2; exit 2; }
mkdir -p "$(dirname "$OUTPUT")"
swiftc -parse-as-library -framework AppKit -framework WebKit "$SOURCE" -o "$OUTPUT"
echo "$OUTPUT"
