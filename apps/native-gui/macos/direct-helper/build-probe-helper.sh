#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <output-path>" >&2
  exit 64
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT="$1"
if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "macOS 13+ is required to build the SMAppService helper" >&2
  exit 69
fi

mkdir -p "$(dirname "$OUTPUT")"
swiftc -O -whole-module-optimization \
  -framework Foundation \
  -framework Security \
  -o "$OUTPUT" \
  "$SCRIPT_DIR/ProbeHelper.swift"
chmod 755 "$OUTPUT"

if [[ -z "${WT_SMAPP_SIGNING_IDENTITY:-}" ]]; then
  echo "WT_SMAPP_SIGNING_IDENTITY is required; refusing to package an unsigned privileged helper" >&2
  exit 69
fi
codesign --force --options runtime --timestamp --sign "$WT_SMAPP_SIGNING_IDENTITY" "$OUTPUT"
codesign --verify --strict "$OUTPUT"
