#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../scripts/lib/resolve-darwin-architecture.sh
source "$ROOT_DIR/../scripts/lib/resolve-darwin-architecture.sh"
WT_MACOS_GOARCH="$(wt_resolve_darwin_goarch)"
INSTALL_DIR="${WT_DIRECT_HELPER_INSTALL_DIR:-$HOME/Library/Application Support/WhiteTransport/bin}"
CONFIG_DIR="${WT_DIRECT_HELPER_CONFIG_DIR:-$HOME/Library/Application Support/WhiteTransport/direct-helper}"
BUNDLE_RESOURCES_DIR="${WT_DIRECT_HELPER_RESOURCES_DIR:-}"
if [[ -z "$BUNDLE_RESOURCES_DIR" && -n "${WT_DIRECT_HELPER_APP:-}" ]]; then
  BUNDLE_RESOURCES_DIR="$WT_DIRECT_HELPER_APP/Contents/Resources"
fi

mkdir -p "$INSTALL_DIR" "$CONFIG_DIR"
export PATH="${PATH:-}:/usr/local/go/bin"
GO_BIN="${WT_GO_BIN:-go}"
SIDECAR_BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/wt-direct-install.XXXXXX")"
trap 'rm -rf "$SIDECAR_BUILD_DIR"' EXIT

copy_or_build() {
  local name="$1"
  local source="${2:-}"
  if [[ -z "$source" && -n "$BUNDLE_RESOURCES_DIR" && -f "$BUNDLE_RESOURCES_DIR/$name" ]]; then
    source="$BUNDLE_RESOURCES_DIR/$name"
  fi
  if [[ -z "$source" ]]; then
    case "$name" in
      direct-helper)
        (cd "$ROOT_DIR" && GOOS=darwin GOARCH="$WT_MACOS_GOARCH" CGO_ENABLED=0 "$GO_BIN" build -trimpath -o "$SIDECAR_BUILD_DIR/$name" .)
        ;;
      tun2socks)
        (cd "$ROOT_DIR/../../../../third_party/tun2socks/upstream" && GOOS=darwin GOARCH="$WT_MACOS_GOARCH" CGO_ENABLED=0 "$GO_BIN" build -trimpath -ldflags='-s -w' -o "$SIDECAR_BUILD_DIR/$name" .)
        ;;
    esac
    source="$SIDECAR_BUILD_DIR/$name"
  fi
  test -f "$source"
  cp "$source" "$INSTALL_DIR/$name"
  chmod 755 "$INSTALL_DIR/$name"
  test -x "$INSTALL_DIR/$name"
}

copy_or_build direct-helper "${WT_DIRECT_HELPER_SOURCE:-}"
copy_or_build tun2socks "${WT_TUN2SOCKS_SOURCE:-}"
chmod 755 "$INSTALL_DIR/direct-helper"
if [[ ! -e "$CONFIG_DIR/config.json" ]]; then
  cp "$ROOT_DIR/config.example.json" "$CONFIG_DIR/config.json"
  chmod 600 "$CONFIG_DIR/config.json"
fi
printf 'installed: %s\nconfig: %s\nrun: %q test --config %q\n' \
  "$INSTALL_DIR/direct-helper" "$CONFIG_DIR/config.json" \
  "$INSTALL_DIR/direct-helper" "$CONFIG_DIR/config.json"
