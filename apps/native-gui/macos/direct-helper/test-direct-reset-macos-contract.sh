#!/usr/bin/env bash
set -euo pipefail

# Linux-safe source contract for the root-gated macOS acceptance wrapper. The
# wrapper must make the >=64 KiB HTTPS probe part of its canonical invocation,
# so acceptance evidence cannot silently degrade to the plaintext-only probe.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WRAPPER="$SCRIPT_DIR/test-direct-reset-macos.sh"

test -x "$WRAPPER"

exec_block="$(sed -n '/^exec "\$RUN_DIR\/direct-reset-harness"/,$p' "$WRAPPER")"
grep -Fq -- '    -accept-macos \' <<<"$exec_block"
grep -Fq -- '    -tls-probe \' <<<"$exec_block"

echo "direct-reset macOS wrapper TLS probe contract: OK"
