#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SMOKE="$SCRIPT_DIR/test-vkcall.sh"

require() {
  grep -Fq "$1" "$SMOKE" || { echo "missing VK Call smoke contract: $1" >&2; exit 1; }
}

require 'WT_VKCALL_JOIN_LINK'
require 'WT_VKCALL_PEER_ID'
require 'run_probe_until_success "VK Call raw data"'
require 'run_probe_until_success "VK Call custom TCP packet"'
require 'run_probe_until_success "VK Call HTTP"'
require 'start_provider_test_origin "vkcall-smoke"'
require 'VK Call smoke requires distinct node/client TokenStore token IDs'

echo "VK Call smoke contract: OK"
