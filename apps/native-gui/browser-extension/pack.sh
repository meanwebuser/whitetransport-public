#!/usr/bin/env bash
set -euo pipefail
umask 077

source_dir="$(cd "$(dirname "$0")" && pwd)"
project="$(cd "$source_dir/../../.." && pwd)"
out="${1:?Pass a fresh directory under project .tmp}"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
test -z "$(find "$out" -mindepth 1 -maxdepth 1 -print -quit)"
version="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$source_dir/manifest.json")"
stage="$(mktemp -d "$out/.package.XXXXXXXX")"
trap 'rm -rf -- "$stage"' EXIT
export TMPDIR="$stage" GOTMPDIR="$stage"

install -d "$stage/WhiteTransport/lib"
for name in manifest.json popup.html popup.css popup.js service-worker.js content-script.js README.md; do
    install -m 0644 "$source_dir/$name" "$stage/WhiteTransport/$name"
done
for name in bridge-client.js export-format.js; do
    install -m 0644 "$source_dir/lib/$name" "$stage/WhiteTransport/lib/$name"
done
(
    cd "$stage"
    find WhiteTransport -type f -print | LC_ALL=C sort |
        zip -X -q "$out/WhiteTransport-Chrome-$version.zip" -@
)
for target in linux/amd64 darwin/amd64 darwin/arm64; do
    arch="${target#*/}"
    os="${target%/*}"
    label="${target//\//-}"
    label="${label/darwin/macos}"
    label="${label/amd64/x64}"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
        "${WT_GO_BIN:-/usr/local/go/bin/go}" -C "$project/apps/native-gui" build -trimpath \
        -o "$out/wt-browser-credential-bridge-$label" ./cmd/browser-credential-bridge/
done
(
    cd "$out"
    sha256sum WhiteTransport-Chrome-*.zip wt-browser-credential-bridge-* >SHA256SUMS
    sha256sum -c SHA256SUMS
)
