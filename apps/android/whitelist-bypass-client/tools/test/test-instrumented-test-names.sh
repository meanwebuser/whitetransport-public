#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
if rg -n 'fun `[^`]* [^`]*`' "$ROOT/app/src/androidTest" -g '*.kt'; then
  echo "instrumented test names must not contain spaces; D8 rejects them" >&2
  exit 1
fi
echo "Instrumented test-name contract passed"
