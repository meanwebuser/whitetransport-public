#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
REGISTRY="$ROOT/app/src/main/assets/capacitor.plugins.json"

[[ -f "$REGISTRY" ]] || {
  echo "missing Capacitor plugin registry: $REGISTRY" >&2
  exit 1
}

python3 - "$REGISTRY" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
value = json.loads(path.read_text())
if not isinstance(value, list):
    raise SystemExit("capacitor.plugins.json must contain a JSON array")
PY

echo "Capacitor plugin registry contract passed: $REGISTRY"
