#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
output="${1:-$repo_root/apps/ios/whitelist-bypass-proxy/WTCore.xcframework}"
go_bin="${WT_GO_BIN:-go}"
gomobile_bin="${WT_GOMOBILE_BIN:-gomobile}"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "WTCore iOS framework must be built on macOS" >&2
  exit 2
fi

if [[ -e "$output" ]]; then
  echo "Refusing to overwrite existing output: $output" >&2
  exit 2
fi

"$go_bin" version >/dev/null
"$gomobile_bin" version >/dev/null
mkdir -p "$(dirname "$output")"
(
  cd "$repo_root/core/go"
  "$gomobile_bin" bind -target=ios -o "$output" ./mobile
)
