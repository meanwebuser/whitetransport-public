#!/usr/bin/env bash
# Linux-safe contract test for the resumable Mac package acceptance wrapper.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WRAPPER="$ROOT_DIR/apps/native-gui/scripts/mac-acceptance.sh"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/wt-mac-acceptance.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT
touch "$TEST_ROOT/known_hosts" "$TEST_ROOT/identity"
export WT_MAC_TARGET=mac-mini-2012
export WT_MAC_MINI_KNOWN_HOSTS_FILE="$TEST_ROOT/known_hosts"
export WT_MAC_MINI_IDENTITY_FILE="$TEST_ROOT/identity"

APP="$TEST_ROOT/WhiteTransport.app"
mkdir -p "$APP/Contents/MacOS/resources"
printf '#!/bin/sh\n' >"$APP/Contents/MacOS/WhiteTransport"
chmod 0755 "$APP/Contents/MacOS/WhiteTransport"
printf '#!/bin/sh\n' >"$APP/Contents/MacOS/whitetransportd"
printf '#!/bin/sh\n' >"$APP/Contents/MacOS/resources/sing-box"
printf '{"tokens":[],"bindings":[]}\n' >"$APP/Contents/MacOS/resources/token-store.json"
chmod 0755 "$APP/Contents/MacOS/whitetransportd" "$APP/Contents/MacOS/resources/sing-box"

FAKE_BIN="$TEST_ROOT/bin"
mkdir -p "$FAKE_BIN"
cat >"$FAKE_BIN/ssh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$FAKE_SSH_LOG"
command="$*"
  case "$command" in
  *"WT_MAC_PREFLIGHT_V1"*) printf 'WT_MAC_PREFLIGHT_OK\troomhacker\tmac-mini-roomhacker\tDarwin\tx86_64\t/Users/roomhacker\n' ;;
  *"uname -s"*) printf 'Darwin\n' ;;
  *"__WT_FAKE_ROUTE_SETUP__"*) printf '%s\n' '{"mode":"only","domains":["api.ipify.org","ifconfig.me"],"destinations":["192.0.2.10/32","192.0.2.11/32"],"domainCount":2,"destinationCount":2}' ;;
  *"__WT_FAKE_ROUTE_SNAPSHOT_after__"*) printf '%s\n' '{"policy":{},"helperConfig":{},"routes":[{"target":"1.1.1.1","interface":"en0"}],"tunnelRouteLines":[],"fullTunnelRouteLines":[],"processes":[]}' ;;
  *"__WT_FAKE_ROUTE_SNAPSHOT_"*)
    if [[ "${FAKE_BAD_ROUTES:-0}" == 1 ]]; then
      printf '%s\n' '{"policy":{"mode":"only","destinations":["192.0.2.10/32","192.0.2.11/32"]},"helperConfig":{"mode":"only","only_cidrs":["192.0.2.10/32","192.0.2.11/32"]},"routes":[{"target":"192.0.2.10/32","interface":"en0"},{"target":"192.0.2.11/32","interface":"en0"},{"target":"1.1.1.1","interface":"en0"}],"tunnelRouteLines":[],"fullTunnelRouteLines":[],"processes":["123 direct-helper"]}'
    else
      printf '%s\n' '{"policy":{"mode":"only","destinations":["192.0.2.10/32","192.0.2.11/32"]},"helperConfig":{"mode":"only","only_cidrs":["192.0.2.10/32","192.0.2.11/32"]},"routes":[{"target":"192.0.2.10/32","interface":"utun7"},{"target":"192.0.2.11/32","interface":"utun7"},{"target":"1.1.1.1","interface":"en0"}],"tunnelRouteLines":["192.0.2.10/32 198.18.0.1 utun7"],"fullTunnelRouteLines":[],"processes":["123 direct-helper"]}'
    fi
    ;;
  *"route -n get"*) printf '%s\n' '{"policy":{"mode":"only","destinations":["192.0.2.10/32","192.0.2.11/32"]},"routes":[{"target":"192.0.2.10/32","interface":"utun7"},{"target":"192.0.2.11/32","interface":"utun7"},{"target":"1.1.1.1","interface":"en0"}],"tunnelRouteLines":["192.0.2.10/32 198.18.0.1 utun7"],"processes":["123 direct-helper"]}' ;;
  *"__WT_FAKE_INSTALL__"*) printf 'installed\n' ;;
  *"__WT_FAKE_RUN__"*) cat "${FAKE_REMOTE_RUN_JSON:?}"; printf '\n' ;;
  *"__WT_FAKE_CLEANUP__"*) cat "${FAKE_REMOTE_CLEANUP_JSON:?}" ;;
  *) printf 'ok\n' ;;
esac
EOF
chmod 0755 "$FAKE_BIN/ssh"
cat >"$FAKE_BIN/scp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$FAKE_SCP_LOG"
EOF
chmod 0755 "$FAKE_BIN/scp"

RESULT="$TEST_ROOT/result.json"
STATE="$TEST_ROOT/state.json"
export FAKE_SSH_LOG="$TEST_ROOT/ssh.log" FAKE_SCP_LOG="$TEST_ROOT/scp.log"
FAKE_REMOTE_RUN_JSON="$TEST_ROOT/passed.json"
FAKE_REMOTE_CLEANUP_JSON="$TEST_ROOT/cleanup.json"
printf '%s\n' '{"mode":"gui-launch-connect","proofBoundary":"split-system-route","passed":true,"cleanup":{"disconnected":true}}' >"$FAKE_REMOTE_RUN_JSON"
printf '%s\n' '{"processStopped":true,"disconnected":true}' >"$FAKE_REMOTE_CLEANUP_JSON"
export FAKE_REMOTE_RUN_JSON FAKE_REMOTE_CLEANUP_JSON
WT_MAC_SSH_TARGET=test@mac WT_MAC_SSH_PORT=2222 \
WT_MAC_SSH_BIN="$FAKE_BIN/ssh" WT_MAC_SCP_BIN="$FAKE_BIN/scp" \
WT_MAC_TEST_PRE_PROBE_HOLD_SECONDS=7 WT_MAC_TEST_HOLD_SECONDS=11 \
WT_MAC_TEST_ROUTE_SNAPSHOT_DELAY_SECONDS=0 \
WT_MAC_TEST_ONLY_DOMAINS=api.ipify.org,ifconfig.me \
WT_MAC_TEST_CONFIG_DIR=/tmp/whitetransport-mac-test-config-contract \
WT_GUI_TEST_SYSTEM_ROUTE_URL=https://api.ipify.org \
WT_GUI_TEST_SYSTEM_ROUTE_EXPECTED_IP=198.51.100.42 \
WT_MAC_RESULT="$RESULT" WT_MAC_STATE="$STATE" WT_MAC_FAKE_REMOTE=1 \
bash "$WRAPPER" --bundle "$APP" --node node-test

python3 - "$RESULT" "$STATE" <<'PY'
import json, pathlib, sys
result = json.loads(pathlib.Path(sys.argv[1]).read_text())
state = json.loads(pathlib.Path(sys.argv[2]).read_text())
assert result["passed"] is True, result
assert result["candidateBundle"] == "WhiteTransport.app", result
assert result["proofBoundary"] == "split-system-route", result
assert result["cleanup"]["disconnected"] is True, result
assert result["routePolicy"]["mode"] == "only", result
assert set(result["routeSnapshots"]) == {"before", "during", "after"}, result
assert state["phase"] == "complete", state
PY

before=$(wc -l <"$FAKE_SCP_LOG")
WT_MAC_SSH_TARGET=test@mac WT_MAC_SSH_PORT=2222 \
WT_MAC_SSH_BIN="$FAKE_BIN/ssh" WT_MAC_SCP_BIN="$FAKE_BIN/scp" \
WT_MAC_RESULT="$RESULT" WT_MAC_STATE="$STATE" WT_MAC_FAKE_REMOTE=1 \
bash "$WRAPPER" --resume --bundle "$APP" --node node-test
after=$(wc -l <"$FAKE_SCP_LOG")
test "$before" -eq "$after"
grep -Fq 'WT_GUI_TEST_PRE_PROBE_HOLD_SECONDS' "$FAKE_SSH_LOG"
grep -Fq 'WT_GUI_TEST_HOLD_SECONDS' "$FAKE_SSH_LOG"
grep -Fq '__WT_FAKE_ROUTE_SETUP__' "$FAKE_SSH_LOG"
grep -Fq 'api.ipify.org,ifconfig.me' "$FAKE_SSH_LOG"
grep -Fq 'WT_NATIVE_GUI_CONFIG_DIR' "$FAKE_SSH_LOG"
grep -Fq 'WT_DIRECT_HELPER_CONFIG' "$FAKE_SSH_LOG"
sed -n '/^route_snapshot_command()/,/^save_route_snapshot()/p' "$WRAPPER" | grep -Fq 'def route_for'
sed -n '/^route_snapshot_command()/,/^save_route_snapshot()/p' "$WRAPPER" | grep -Fq 'netstat'
sed -n '/^route_snapshot_command()/,/^save_route_snapshot()/p' "$WRAPPER" | grep -Fq 'import ipaddress'
# Cleanup must audit the staged app tree, not only the short-lived launcher PID.
grep -Fq 'pgrep -f' "$WRAPPER"

printf '%s\n' '{"mode":"gui-launch-connect","proofBoundary":"system-route","passed":true,"systemRouteProbePassed":true}' >"$FAKE_REMOTE_RUN_JSON"
BAD_ROUTE_RESULT="$TEST_ROOT/bad-route-result.json"
BAD_ROUTE_STATE="$TEST_ROOT/bad-route-state.json"
if FAKE_BAD_ROUTES=1 WT_MAC_SSH_TARGET=test@mac WT_MAC_SSH_PORT=2222 \
  WT_MAC_SSH_BIN="$FAKE_BIN/ssh" WT_MAC_SCP_BIN="$FAKE_BIN/scp" \
  WT_MAC_TEST_PRE_PROBE_HOLD_SECONDS=1 WT_MAC_TEST_HOLD_SECONDS=1 \
  WT_MAC_TEST_ROUTE_SNAPSHOT_DELAY_SECONDS=0 WT_MAC_TEST_ONLY_DOMAINS=api.ipify.org,ifconfig.me \
  WT_MAC_TEST_CONFIG_DIR=/tmp/whitetransport-mac-test-config-bad-route \
  WT_GUI_TEST_SYSTEM_ROUTE_URL=https://api.ipify.org WT_GUI_TEST_SYSTEM_ROUTE_EXPECTED_IP=198.51.100.42 \
  WT_MAC_RESULT="$BAD_ROUTE_RESULT" WT_MAC_STATE="$BAD_ROUTE_STATE" WT_MAC_FAKE_REMOTE=1 \
  bash "$WRAPPER" --bundle "$APP" --node node-test; then
  echo "wrapper accepted a non-utun selected route" >&2
  exit 1
fi
python3 - "$BAD_ROUTE_RESULT" <<'PY'
import json, pathlib, sys
result = json.loads(pathlib.Path(sys.argv[1]).read_text())
assert result["passed"] is False, result
assert "selected destination is not routed through utun" in result["error"], result
PY

printf '%s\n' '{"mode":"gui-launch-connect","proofBoundary":"split-system-route","passed":false,"error":"system route probe failed"}' >"$FAKE_REMOTE_RUN_JSON"
BAD_RESULT="$TEST_ROOT/bad-result.json"
BAD_STATE="$TEST_ROOT/bad-state.json"
if WT_MAC_SSH_TARGET=test@mac WT_MAC_SSH_PORT=2222 \
  WT_MAC_SSH_BIN="$FAKE_BIN/ssh" WT_MAC_SCP_BIN="$FAKE_BIN/scp" \
  WT_MAC_RESULT="$BAD_RESULT" WT_MAC_STATE="$BAD_STATE" WT_MAC_FAKE_REMOTE=1 \
  bash "$WRAPPER" --bundle "$APP" --node node-test; then
  echo "wrapper accepted remote passed=false result" >&2
  exit 1
fi
python3 - "$BAD_RESULT" <<'PY'
import json, pathlib, sys
result = json.loads(pathlib.Path(sys.argv[1]).read_text())
assert result["passed"] is False, result
assert "remote GUI result reported passed=false" in result["error"], result
PY

printf '%s\n' '{"mode":"gui-launch-connect","proofBoundary":"split-system-route","passed":true}' >"$FAKE_REMOTE_RUN_JSON"
printf '%s\n' '{"processStopped":false,"disconnected":false}' >"$FAKE_REMOTE_CLEANUP_JSON"
INCOMPLETE_RESULT="$TEST_ROOT/incomplete-result.json"
INCOMPLETE_STATE="$TEST_ROOT/incomplete-state.json"
if WT_MAC_SSH_TARGET=test@mac WT_MAC_SSH_PORT=2222 \
  WT_MAC_SSH_BIN="$FAKE_BIN/ssh" WT_MAC_SCP_BIN="$FAKE_BIN/scp" \
  WT_MAC_RESULT="$INCOMPLETE_RESULT" WT_MAC_STATE="$INCOMPLETE_STATE" WT_MAC_FAKE_REMOTE=1 \
  bash "$WRAPPER" --bundle "$APP" --node node-test; then
  echo "wrapper accepted incomplete cleanup proof" >&2
  exit 1
fi
python3 - "$INCOMPLETE_RESULT" <<'PY'
import json, pathlib, sys
result = json.loads(pathlib.Path(sys.argv[1]).read_text())
assert result["passed"] is False, result
assert "remote cleanup proof was incomplete" in result["error"], result
PY

grep -Fq '__WT_FAKE_INSTALL__' "$FAKE_SSH_LOG"
grep -Fq '__WT_FAKE_RUN__' "$FAKE_SSH_LOG"
grep -Fq '__WT_FAKE_CLEANUP__' "$FAKE_SSH_LOG"
echo "mac acceptance wrapper contract: OK"
