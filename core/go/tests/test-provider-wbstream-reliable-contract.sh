#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
smoke="$script_dir/test-provider-wbstream-smoke.sh"

grep -Fq 'WT_WBSTREAM_RELIABLE' "$smoke" || {
  echo 'reliable WBStream smoke must have an explicit opt-in' >&2
  exit 1
}
grep -Fq 'WT_TOKEN_STORE_PATH="$TOKEN_STORE"' "$smoke" || {
  echo 'reliable WBStream smoke must inject from an isolated token store' >&2
  exit 1
}
grep -Fq 'wb["tunnel_mode"] = "video"' "$smoke" || {
  echo 'reliable WBStream smoke must inject video + reliable adapter settings' >&2
  exit 1
}
grep -Fq 'wb["reliable"] = True' "$smoke" || {
  echo 'reliable WBStream smoke must inject video + reliable adapter settings' >&2
  exit 1
}
if grep -Fq 'wb_carrier["whitelist_bypass"]' "$smoke"; then
  echo 'reliable WBStream smoke must use the typed wbstream JSON block' >&2
  exit 1
fi
grep -Fq 'WBStream reliable MultiTrack KCP preflight passed' "$smoke" || {
  echo 'reliable WBStream smoke must prove its temporary configuration before startup' >&2
  exit 1
}
grep -Fq 'wait_for_active "$CLIENT_LOG" "kcptunnel: init"' "$smoke" || {
  echo 'reliable WBStream smoke must prove the runtime selected MultiTrack KCP' >&2
  exit 1
}
grep -Fq 'wait_for_active "$NODE_LOG" "kcptunnel: init"' "$smoke" || {
  echo 'reliable WBStream smoke must prove both peers selected MultiTrack KCP' >&2
  exit 1
}
grep -Fq 'RELIABLE_RUNTIME_CONFIRMED=true' "$smoke" || {
  echo 'reliable WBStream result must derive from runtime telemetry' >&2
  exit 1
}

echo 'WBStream reliable smoke contract: OK'
