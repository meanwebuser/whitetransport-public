#!/usr/bin/env bash
set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
verifier="$script_dir/verify-windows-offhost-acceptance.sh"
fixture_dir=$(mktemp -d)
trap 'rm -rf "$fixture_dir"' EXIT
fixture="$fixture_dir/acceptance.json"

jq -n '{
  schemaRevision: 1,
  proofBoundary: "windows-gui-offhost-system-tun",
  passed: true,
  topology: {sameHost: false},
  gui: {passed: true, systemRouteIp: "212.192.31.128"},
  directNoProxy: {expectedIp: "212.192.31.128", ip: "212.192.31.128", exitCode: 0},
  during: {whiteTransportAdapter: [{}], fullTunnelRoutes: [{}, {}], listeners: [{}, {}]},
  cleanup: {adapters: [], fullTunnelRoutes: [], routesIPv6: [], processes: [], listeners: []},
  remote: {matchedLogBytes: 1}
}' > "$fixture"

test -x "$verifier" || { echo "missing executable verifier: $verifier" >&2; exit 1; }
bash "$verifier" "$fixture"
