#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="${1:-$SCRIPT_DIR/build/universal}"
GO_BINARY="${GO_BINARY:-$(command -v go)}"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/wt-tun2socks.XXXXXX")"
trap 'rm -rf "$WORK_DIR"' EXIT

mkdir -p "$OUTPUT_DIR"
for arch in arm64 amd64; do
  arch_dir="$WORK_DIR/$arch"
  mkdir -p "$arch_dir"
  (
    cd "$SCRIPT_DIR"
    CGO_ENABLED=1 GOOS=darwin GOARCH="$arch" \
      CGO_CFLAGS="-mmacosx-version-min=12.0" CGO_LDFLAGS="-mmacosx-version-min=12.0" \
      "$GO_BINARY" build \
      -trimpath -buildmode=c-archive \
      -o "$arch_dir/libWhiteTransportTun2Socks.a" \
      .
  )
done

lipo -create \
  "$WORK_DIR/arm64/libWhiteTransportTun2Socks.a" \
  "$WORK_DIR/amd64/libWhiteTransportTun2Socks.a" \
  -output "$OUTPUT_DIR/libWhiteTransportTun2Socks.a"
cp "$WORK_DIR/arm64/libWhiteTransportTun2Socks.h" "$OUTPUT_DIR/WhiteTransportTun2Socks.h"
cp "$SCRIPT_DIR/module.modulemap" "$OUTPUT_DIR/module.modulemap"

lipo "$OUTPUT_DIR/libWhiteTransportTun2Socks.a" -verify_arch arm64 x86_64
grep -q 'WTStartTun2Socks' "$OUTPUT_DIR/WhiteTransportTun2Socks.h"
grep -q 'WTStopTun2Socks' "$OUTPUT_DIR/WhiteTransportTun2Socks.h"
