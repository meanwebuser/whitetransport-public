#!/usr/bin/env bash
set -euo pipefail

# Static Swift 6.3 contract guard.  This intentionally runs on Linux too; a
# Darwin compiler remains the acceptance proof, while these checks catch the
# source-level regressions that previously made that build fail.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MACOS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
PROBE="$MACOS_DIR/SMAppServiceProbe.swift"
HELPER="$MACOS_DIR/direct-helper/ProbeHelper.swift"
PACKAGE="$MACOS_DIR/Package.swift"
TEST="$MACOS_DIR/WhiteTransportPacketTunnelTests/SMAppServiceProbeTests.swift"

grep -Fq 'NSSet().adding(WTMacAuthorizationProbeRequest.self)' "$PROBE"
grep -Fq 'NSSet().adding(WTMacAuthorizationProbeResponse.self)' "$PROBE"
grep -Fq 'NSSet().adding(WTMacAuthorizationProbeRequest.self)' "$HELPER"
grep -Fq 'NSSet().adding(WTMacAuthorizationProbeResponse.self)' "$HELPER"
if grep -Fq 'setClasses([' "$PROBE" "$HELPER"; then
  echo "setClasses must receive the Set<AnyHashable> returned by NSSet.adding" >&2
  exit 1
fi
grep -Fq 'self.helperVersion = helperVersion' "$HELPER"
grep -Fq 'init(success: Bool, operation: String, helperVersion:' "$HELPER"
if grep -Fq 'connection.auditToken' "$HELPER"; then
  echo "NSXPCConnection has no auditToken Swift member" >&2
  exit 1
fi
grep -Fq 'processIdentifier' "$HELPER"
if grep -Fq 'private final class ProbeListenerDelegate' "$HELPER"; then
  echo "top-level delegate cannot expose a private type" >&2
  exit 1
fi
grep -Fq '"direct-helper"' "$PACKAGE"
grep -Fq '@testable import WhiteTransportMacOS' "$TEST"

echo "Swift 6.3 SMAppService probe contract: PASS"
