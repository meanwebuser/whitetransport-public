#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET="$SCRIPT_DIR/test-provider-wbstream-startup.sh"

[[ -x "$TARGET" ]] || { echo "missing executable WBStream startup lane: $TARGET" >&2; exit 1; }
grep -Fq 'WT_PROVIDER_STARTUP' "$TARGET" || { echo 'provider startup must be explicitly opt-in' >&2; exit 1; }
grep -Fq 'WT_WBSTREAM_STARTUP=1' "$TARGET" || { echo 'startup wrapper must select non-live startup mode' >&2; exit 1; }
grep -Fq 'provider-wbstream-startup' "$SCRIPT_DIR/test-provider-wbstream-smoke.sh" || { echo 'shared harness must emit a distinct startup result' >&2; exit 1; }

echo 'WBStream provider startup contract: OK'
