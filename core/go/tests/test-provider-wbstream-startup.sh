#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ "${WT_PROVIDER_STARTUP:-0}" != "1" ]]; then
    echo '[!] Provider startup is opt-in; set WT_PROVIDER_STARTUP=1' >&2
    exit 2
fi

WT_WBSTREAM_STARTUP=1 exec "$SCRIPT_DIR/test-provider-wbstream-smoke.sh"
