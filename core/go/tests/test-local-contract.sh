#!/usr/bin/env bash
# Acceptance wrapper for the deterministic local integration lane.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT="$(mktemp /tmp/wt-local-contract.XXXXXX.log)"
trap 'rm -f "$OUTPUT"' EXIT

WT_TEST_FORCE_BIND_COLLISION_ONCE=1 "$SCRIPT_DIR/test-local.sh" | tee "$OUTPUT"
python3 - "$OUTPUT" <<'PY'
import json
import sys

lines = open(sys.argv[1], encoding="utf-8").read().splitlines()
result = None
for line in reversed(lines):
    try:
        candidate = json.loads(line)
    except json.JSONDecodeError:
        continue
    if candidate.get("test") == "desktop-local-fast":
        result = candidate
        break
if result is None:
    raise SystemExit("missing structured desktop-local-fast result")

assert result["exit"] == 0, result
assert result["daemonCount"] == 2, result
assert result["productionCredentialsUsed"] is False, result
assert result["tokenStorePresent"] is False, result
assert result["syntheticTestCredentialUsed"] is False, result
assert result["credentialScope"] == "none", result
assert result["bootstrapSecretConfigured"] is True, result
assert result["bootstrapSecretScope"] == "deterministic-local-fixture", result
assert result["providerEndpointsConfigured"] is False, result
assert result["configIsolationVerified"] is True, result
assert result["socksStrict"] is True, result
assert result["ipProofScope"] == "loopback-local", result
assert result["directIp"] == "127.0.0.1", result
assert result["socksIp"] == "127.0.0.1", result
assert result["selectedNodeId"] == "local-failover-node", result
assert result["socksHost"] == "127.0.0.1", result
assert 0 < result["socksPort"] < 65536, result
assert result["healthOk"] is True, result
assert result["statusVerified"] is True, result
assert result["carriersVerified"] is True, result
assert result["clientNetworkNamespaceIsolated"] is True, result
assert result["directOriginReachableFromClient"] is False, result
assert result["socksOriginReachableFromClient"] is True, result
assert result["noDirectFallbackProved"] is True, result
assert result["bindCollisionRetryProved"] is True, result
assert len(result["binarySha256"]) == 64, result
assert len(result["gitCommit"]) == 40, result
assert isinstance(result["gitDirty"], bool), result
assert result["primaryRoute"] == "local.egress.primary", result
assert result["backupRoute"] == "local.egress.backup", result
assert result["primaryFailureObserved"] is True, result
assert "tunnelReader write error" in result["primaryFailureEvidence"], result
assert "local.egress.primary" in result["primaryFailureEvidence"], result
assert "/primary/primary.jsonl" in result["primaryFailureEvidence"], result
assert result["primaryNonceValid"] is True, result
assert result["backupNonceValid"] is True, result
assert result["primaryFrameArtifacts"] > 0, result
assert result["backupFrameArtifacts"] > 0, result
assert result["cleanup"] == {
    "nodePidExited": True,
    "clientPidExited": True,
    "listenersReleased": True,
    "artifactsRemoved": True,
    "networkNamespaceReleased": True,
}, result
assert result["phaseErrors"] == [], result
PY
