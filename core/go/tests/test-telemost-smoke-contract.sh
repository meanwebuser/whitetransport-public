#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
script="$repo_root/core/go/tests/test-telemost.sh"

if ! grep -Fq 'WT_TELEMOST_JOIN_LINK' "$script"; then
  echo "Telemost live smoke must require an explicit existing join link" >&2
  exit 1
fi

if [[ "$(grep -Fc '"join_link": os.environ["WT_TELEMOST_JOIN_LINK"]' "$script")" -ne 2 ]]; then
  echo "Telemost live smoke must pass the join link to both provider roles" >&2
  exit 1
fi

if [[ "$(grep -Fc 'import json, os, sys' "$script")" -lt 2 ]]; then
  echo "Telemost live smoke must import os before reading its required environment" >&2
  exit 1
fi

if [[ "$(grep -Fc -- '-timeout 20s' "$script")" -ne 3 ]]; then
  echo "Telemost VP8 smoke must allow the provider-specific 20-second payload budget" >&2
  exit 1
fi
