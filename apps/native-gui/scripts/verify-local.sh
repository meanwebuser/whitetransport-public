#!/usr/bin/env bash
set -euo pipefail

# Local-first native GUI gate. This lane intentionally stops before Wails,
# Apple SDK tools, signing, packaging, live daemons, and credentialed runtime
# checks.
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
GO_BIN="${WT_GO_BIN:-/usr/local/go/bin/go}"

if [[ ! -x "$GO_BIN" ]]; then
  GO_BIN="$(command -v go || true)"
fi
if [[ -z "$GO_BIN" || ! -x "$GO_BIN" ]]; then
  echo "missing Go tool (set WT_GO_BIN or install go)" >&2
  exit 127
fi
if ! command -v npm >/dev/null 2>&1; then
  echo "missing npm tool" >&2
  exit 127
fi
if ! command -v node >/dev/null 2>&1; then
  echo "missing node tool" >&2
  exit 127
fi

GO_VERSION="$($GO_BIN env GOVERSION)"
GO_VERSION="${GO_VERSION#go}"
if [[ "$(printf '%s\n' '1.26.1' "$GO_VERSION" | sort -V | head -n1)" != '1.26.1' ]]; then
  echo "Go ${GO_VERSION} is too old for the native GUI modules (requires >=1.26.1)" >&2
  exit 2
fi

run_step() {
  local title="$1"
  shift
  printf '==> %s\n' "$title"
  "$@"
}

cd "$ROOT_DIR"

# Command contract: go test ./... -count=1
run_step "native GUI Go tests" bash -c 'cd apps/native-gui && "$1" test ./... -count=1' _ "$GO_BIN"
run_step "native GUI frontend typecheck" npm --prefix apps/native-gui/frontend run typecheck
run_step "native GUI frontend build" npm --prefix apps/native-gui/frontend run build

run_step "shared client-web marker" node --input-type=module <<'NODE'
import fs from "node:fs";

const markerPath = "apps/native-gui/frontend/dist/whitetransport-client-web.json";
const indexPath = "apps/native-gui/frontend/dist/index.html";
const marker = JSON.parse(fs.readFileSync(markerPath, "utf8"));
if (marker.bundle !== "@whitetransport/client-web" || marker.schema !== 1) {
  throw new Error(`invalid shared bundle marker: ${JSON.stringify(marker)}`);
}
const shell = ["home", "endpoints", "settings"];
if (JSON.stringify(marker.shell) !== JSON.stringify(shell)) {
  throw new Error(`invalid shared shell marker: ${JSON.stringify(marker.shell)}`);
}
const index = fs.readFileSync(indexPath, "utf8");
if (!/assets\/index-[^"']+\.js/.test(index) || !/assets\/index-[^"']+\.css/.test(index)) {
  throw new Error(`frontend index has no built JS/CSS assets: ${indexPath}`);
}
console.log(`shared client-web marker: ${markerPath}`);
NODE

run_step "Linux-safe package/resource contracts" bash -c '
  set -euo pipefail
  bash apps/native-gui/macos/scripts/test-smappservice-probe.sh
  bash apps/native-gui/macos/scripts/test-smappservice-swift63-contract.sh
  bash apps/native-gui/macos/scripts/test-credentialed-package-contract.sh
  bash apps/native-gui/scripts/test-post-build-client-contract.sh
  bash apps/native-gui/scripts/test-mac-acceptance-contract.sh
  bash apps/native-gui/macos/scripts/test-go-tool-resolution-contract.sh
  bash apps/native-gui/macos/direct-helper/test-direct-reset-macos-contract.sh
'

printf '%s\n' "native GUI local verification gate: PASS"
