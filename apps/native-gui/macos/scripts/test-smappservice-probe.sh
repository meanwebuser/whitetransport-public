#!/usr/bin/env bash
set -euo pipefail

# Linux-safe topology verifier for the first macOS authorization milestone.
# It checks only immutable bundle/source contracts; it never invokes launchctl,
# SMAppService, sudo, or a helper binary.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MACOS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
HELPER_DIR="$MACOS_DIR/direct-helper"
PLIST="$HELPER_DIR/com.meanwebuser.whitetransport.net-helper.plist"

test -f "$PLIST"
test -f "$HELPER_DIR/ProbeHelper.swift"
test -f "$MACOS_DIR/SMAppServiceProbe.swift"
test -f "$MACOS_DIR/WhiteTransportPacketTunnelTests/SMAppServiceProbeTests.swift"

grep -Fq '<key>MachServices</key>' "$PLIST"
grep -Fq 'com.meanwebuser.whitetransport.net-helper' "$PLIST"
grep -Fq '<key>BundleProgram</key>' "$PLIST"
grep -Fq 'Contents/Library/LaunchDaemons/com.meanwebuser.whitetransport.net-helper' "$PLIST"
if grep -Fq '/Library/PrivilegedHelperTools' "$PLIST"; then
  echo "plist contains a forbidden absolute privileged-helper path" >&2
  exit 1
fi
grep -Fq 'SMAppService.daemon' "$MACOS_DIR/SMAppServiceProbe.swift"
grep -Fq 'authorizeMutation' "$MACOS_DIR/SMAppServiceProbe.swift"
grep -Fq 'health' "$MACOS_DIR/SMAppServiceProbe.swift"
grep -Fq 'processIdentifier' "$HELPER_DIR/ProbeHelper.swift"
grep -Fq 'SecRequirementCreateWithString' "$HELPER_DIR/ProbeHelper.swift"
grep -Fq 'anchor apple generic' "$HELPER_DIR/ProbeHelper.swift"
grep -Fq 'certificate leaf[subject.OU]' "$HELPER_DIR/ProbeHelper.swift"
grep -Fq 'kSecCodeInfoTeamIdentifier' "$HELPER_DIR/ProbeHelper.swift"
grep -Fq 'authorizeMutation' "$HELPER_DIR/ProbeHelper.swift"
grep -Fq 'SMAppService' "$MACOS_DIR/WhiteTransportPacketTunnelTests/SMAppServiceProbeTests.swift"

# Registration is an explicit developer action, never a Wails startup side
# effect. The startup body ends before the probe method is declared.
startup_body="$(sed -n '/func (a \*App) startup/,/^}/p' "$MACOS_DIR/../app.go")"
if grep -Fq 'ProbeMacAuthorization' <<<"$startup_body"; then
  echo "SMAppService probe must not auto-register at Wails startup" >&2
  exit 1
fi

grep -Fq 'codesign --verify' "$MACOS_DIR/scripts/verify-smappservice-bundle.sh"
grep -Fq 'test -x "$HELPER"' "$MACOS_DIR/scripts/verify-smappservice-bundle.sh"
grep -Fq 'TeamIdentifier' "$MACOS_DIR/scripts/verify-smappservice-bundle.sh"
grep -Fq 'source-only unsigned' "$MACOS_DIR/scripts/verify-smappservice-bundle.sh"
grep -Fq 'WT_SMAPP_SIGNING_IDENTITY' "$HELPER_DIR/build-probe-helper.sh"
grep -Fq 'codesign' "$HELPER_DIR/build-probe-helper.sh"

echo "SMAppService/XPC probe topology: PASS"
