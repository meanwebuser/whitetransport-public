#!/usr/bin/env bash
set -Eeuo pipefail

artifact=${1:-}
if [[ -z "$artifact" || ! -f "$artifact" ]]; then
  echo "usage: $0 /absolute/path/to/acceptance.json" >&2
  exit 2
fi

jq -e '
  .schemaRevision == 1 and
  .proofBoundary == "windows-gui-offhost-system-tun" and
  .passed == true and
  .topology.sameHost == false and
  .gui.passed == true and
  .gui.systemRouteIp == .directNoProxy.expectedIp and
  .directNoProxy.ip == .directNoProxy.expectedIp and
  .directNoProxy.exitCode == 0 and
  (.during.whiteTransportAdapter | length) == 1 and
  (.during.fullTunnelRoutes | length) == 2 and
  (.during.listeners | length) > 0 and
  (.cleanup.adapters | length) == 0 and
  (.cleanup.fullTunnelRoutes | length) == 0 and
  (.cleanup.routesIPv6 | length) == 0 and
  (.cleanup.processes | length) == 0 and
  (.cleanup.listeners | length) == 0 and
  (.remote.matchedLogBytes | tonumber) > 0
' "$artifact" >/dev/null

echo "windows off-host acceptance artifact: PASS ($artifact)"
