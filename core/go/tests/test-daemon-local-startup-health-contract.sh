#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET="$SCRIPT_DIR/test-daemon-local-startup-health.sh"

if [[ ! -x "$TARGET" ]]; then
    echo "missing executable startup-health harness: $TARGET" >&2
    exit 1
fi

output="$($TARGET)"
result="$(printf '%s\n' "$output" | tail -n 1)"
RESULT_JSON="$result" python3 - <<'PY'
import json, os

result = json.loads(os.environ["RESULT_JSON"])
assert result["test"] == "daemon-local-startup-health"
assert result["proofLevel"] == "startup-health"
assert result["daemonCount"] == 1
assert result["productionCredentialsUsed"] is False
assert result["tokenStorePresent"] is False
assert result["syntheticTestCredentialUsed"] is False
assert result["credentialScope"] == "none"
assert result["bootstrapSecretConfigured"] is True
assert result["bootstrapSecretScope"] == "deterministic-local-fixture"
assert result["providerEndpointsConfigured"] is False
assert result["configIsolationVerified"] is True
assert result["healthOk"] is True
assert result["statusVerified"] is True
assert result["carriersVerified"] is True
assert result["expectedCarrierIds"] == [
    "local.control",
    "local.egress.backup",
    "local.egress.primary",
]
assert result["minimumObservedCarrierIds"] == ["local.control"]
assert result["observedCarrierIds"] == result["expectedCarrierIds"]
assert result["configuredCarrierTypes"] == ["file.mailbox"]
assert "configuredCarrierIds" not in result
assert result["apiHost"] == "127.0.0.1"
assert 0 < result["apiPort"] < 65536
assert result["socksHost"] == "127.0.0.1"
assert 0 < result["socksPort"] < 65536
assert result["cleanup"] == {
    "pidExited": True,
    "listenersReleased": True,
    "artifactsRemoved": True,
}
assert result["exit"] == 0
assert result["phaseErrors"] == []
PY

printf '%s\n' "$output"
echo "Daemon local startup-health contract: OK"
