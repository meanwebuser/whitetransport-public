#!/usr/bin/env bash
set -euo pipefail

# Proves the daemon's Windows build remains free of Unix-only process APIs.
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT

export PATH="$PATH:/usr/local/go/bin"
cd "$repo_root/core/go"
GOOS=windows GOARCH=amd64 go build -trimpath -o "$build_dir/whitetransportd.exe" ./cmd/whitetransportd
