#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUNNER="$ROOT/tools/test/run_android_auto_debug.sh"

# The Samsung test device rejects the asynchronous activity launch with error
# -102; the runner must wait for the launch completion before probing the UI.
grep -Fq 'shell am start -W -n "$PKG/$ACTIVITY"' "$RUNNER"
