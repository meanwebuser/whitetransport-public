#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VERIFY_SCRIPT="$SCRIPT_DIR/verify-local.sh"

test -x "$VERIFY_SCRIPT"

# The local gate must exercise the runtime/frontend/resource boundaries while
# remaining usable on a Linux host with no Apple SDK, credentials, or network.
grep -Fq 'go test ./... -count=1' "$VERIFY_SCRIPT"
grep -Fq 'npm --prefix apps/native-gui/frontend run typecheck' "$VERIFY_SCRIPT"
grep -Fq 'npm --prefix apps/native-gui/frontend run build' "$VERIFY_SCRIPT"
grep -Fq 'whitetransport-client-web.json' "$VERIFY_SCRIPT"
grep -Fq 'test-smappservice-probe.sh' "$VERIFY_SCRIPT"
grep -Fq 'test-smappservice-swift63-contract.sh' "$VERIFY_SCRIPT"
grep -Fq 'test-credentialed-package-contract.sh' "$VERIFY_SCRIPT"
grep -Fq 'test-post-build-client-contract.sh' "$VERIFY_SCRIPT"
grep -Fq 'test-go-tool-resolution-contract.sh' "$VERIFY_SCRIPT"
grep -Fq 'test-direct-reset-macos-contract.sh' "$VERIFY_SCRIPT"

if grep -Eq 'xcrun|post-build-pack\.sh|scp .*token-store|WT_TOKEN_STORE_SOURCE|secrets/token-store\.json' "$VERIFY_SCRIPT"; then
  echo "local gate must not require macOS tooling or production secrets" >&2
  exit 1
fi

echo "native GUI local verification contract: OK"
