#!/usr/bin/env bash
# Contract regression for the credentialed WBStream smoke harness.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SMOKE_SCRIPT="$SCRIPT_DIR/test-provider-wbstream-smoke.sh"

grep -Fq 'WT_CLIENT_BOOTSTRAP_FILES' "$SMOKE_SCRIPT" || {
  echo 'WBStream smoke must opt into explicit client bootstrap projection' >&2
  exit 1
}
grep -Fq 'wbstream client binding' "$SMOKE_SCRIPT" || {
  echo 'WBStream smoke must validate the projected client TokenStore binding' >&2
  exit 1
}

echo 'WBStream smoke client projection contract: OK'
