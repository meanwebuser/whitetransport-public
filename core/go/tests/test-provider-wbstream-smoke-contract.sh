#!/usr/bin/env bash
# Regression contract for live WBStream provider readiness.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SMOKE="$SCRIPT_DIR/test-provider-wbstream-smoke.sh"

require() {
  grep -Fq "$1" "$SMOKE" || {
    echo "missing WBStream smoke contract: $1" >&2
    exit 1
  }
}

require 'wait_for_active()'
require 'wait_for_active "$CLIENT_LOG" "vp8 tunnel writer started"'
require 'run_probe_until_success()'
require 'run_probe_until_success "WB raw data"'
require 'run_probe_until_success "WB custom TCP packet"'
require 'run_probe_until_success "WB HTTP"'
require 'carrier.setdefault("file_mailbox", {})["dir"] = mailbox'
require 'assert mailbox_path == mailbox'
require 'WT_WBSTREAM_LIVE'
require 'wb["tunnel_mode"] = "video"'
if grep -Fq 'WT_WBSTREAM_FORCE_VIDEO' "$SMOKE"; then
  echo 'WBStream VP8 smoke must not depend on a diagnostic force-video override' >&2
  exit 1
fi
require 'TEST_TMP_ROOT="${WT_TEST_TMP_ROOT:-$REPO_ROOT/.tmp}"'
require 'RUN_DIR="$(mktemp -d "$TEST_TMP_ROOT/wt-provider-wbstream-smoke.XXXXXX")"'
require 'reserve_port()'
require 'for launch_attempt in 1 2 3'
require 'BIND_COLLISION_RETRY_PROVED=true'
require 'wait_for_node()'
require 'kill -0 "$CLIENT_PID"'
require '"test":"provider-wbstream-smoke"'
require '"cleanup"'
require '/v1/session/disconnect'
require 'DISCONNECT_CONFIRMED=true'
require 'get("state") == "disconnected"'
require 'connected session ended without confirmed disconnect'
require 'PROVIDER_ORIGIN_LOG_DIR="$RUN_DIR"'
grep -Fq 'PROVIDER_ORIGIN_LOG_DIR' "$SCRIPT_DIR/lib/provider-test-origin.sh" || {
  echo 'provider origin helper must support a test-owned log directory' >&2
  exit 1
}

echo "WBStream smoke readiness contract: OK"
