#!/usr/bin/env bash
# Root acceptance wrapper for the macOS direct-utun reset diagnostic.
# It builds the harness in a temporary directory and is the only wrapper that
# opts into route/utun mutation through -accept-macos.
set -Eeuo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "direct-utun reset acceptance requires macOS" >&2
    exit 2
fi
if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
    echo "run this acceptance wrapper through sudo; it is the only root-gated lane" >&2
    exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$SCRIPT_DIR/test-harness"
HELPER_BIN="${WT_DIRECT_HELPER_BIN:-}"
TUN2SOCKS_BIN="${WT_TUN2SOCKS_BIN:-}"
CURL_BIN="${WT_CURL_BIN:-/usr/bin/curl}"

if [[ -z "$HELPER_BIN" || -z "$TUN2SOCKS_BIN" ]]; then
    echo "set WT_DIRECT_HELPER_BIN and WT_TUN2SOCKS_BIN to exact installed binaries" >&2
    exit 2
fi
for executable in "$HELPER_BIN" "$TUN2SOCKS_BIN" "$CURL_BIN"; do
    [[ -x "$executable" ]] || { echo "not executable: $executable" >&2; exit 2; }
done

RUN_DIR="$(mktemp -d "${TMPDIR:-/tmp}/wt-direct-reset-harness.XXXXXX")"
cleanup() { rm -rf "$RUN_DIR"; }
trap cleanup EXIT

GO_BIN="${WT_GO_BIN:-/usr/local/go/bin/go}"
[[ -x "$GO_BIN" ]] || GO_BIN="$(command -v go)"
(cd "$HARNESS_DIR" && "$GO_BIN" build -trimpath -o "$RUN_DIR/direct-reset-harness" .)
exec "$RUN_DIR/direct-reset-harness" \
    -accept-macos \
    -tls-probe \
    -helper "$HELPER_BIN" \
    -tun2socks "$TUN2SOCKS_BIN" \
    -curl "$CURL_BIN"
