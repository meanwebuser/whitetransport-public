#!/usr/bin/env bash
# Linux-safe regression for recovering a remote result after SSH disconnect.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WRAPPER="$ROOT_DIR/apps/native-gui/scripts/mac-acceptance.sh"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/wt-mac-acceptance-recovery.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT
touch "$TEST_ROOT/known_hosts" "$TEST_ROOT/identity"
export WT_MAC_TARGET=mac-mini-2012
export WT_MAC_MINI_KNOWN_HOSTS_FILE="$TEST_ROOT/known_hosts"
export WT_MAC_MINI_IDENTITY_FILE="$TEST_ROOT/identity"

APP="$TEST_ROOT/WhiteTransport.app"
mkdir -p "$APP/Contents/MacOS/resources" "$TEST_ROOT/bin"
printf '#!/bin/sh\n' >"$APP/Contents/MacOS/WhiteTransport"
chmod 0755 "$APP/Contents/MacOS/WhiteTransport"
printf '#!/bin/sh\n' >"$APP/Contents/MacOS/whitetransportd"
printf '#!/bin/sh\n' >"$APP/Contents/MacOS/resources/sing-box"
printf '{"tokens":[],"bindings":[]}\n' >"$APP/Contents/MacOS/resources/token-store.json"
chmod 0755 "$APP/Contents/MacOS/whitetransportd" "$APP/Contents/MacOS/resources/sing-box"

cat >"$TEST_ROOT/bin/ssh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
command="${*: -1}"
printf '%s\n' "$command" >>"$FAKE_SSH_LOG"
if [[ "$*" == *"WT_MAC_PREFLIGHT_V1"* ]]; then
  printf 'WT_MAC_PREFLIGHT_OK\troomhacker\tmac-mini-roomhacker\tDarwin\tx86_64\t/Users/roomhacker\n'
elif [[ "$command" == *"uname -s"* ]]; then
  printf 'Darwin\n'
elif [[ "$command" == *"__WT_FAKE_INSTALL__"* ]]; then
  printf 'installed\n'
elif [[ "$command" == *"__WT_FAKE_RUN__"* ]]; then
  if [[ -n "${FAKE_ALWAYS_FAIL_RUN:-}" ]]; then
    exit 255
  fi
  if [[ ! -e "$FAKE_DROP_MARKER" ]]; then
    : >"$FAKE_DROP_MARKER"
    exit 255
  fi
  cat "$FAKE_RUN_JSON"
elif [[ "$command" == *"__WT_FAKE_CLEANUP__"* ]]; then
  cat "$FAKE_CLEANUP_JSON"
elif [[ "$command" == *"test -s"* && "$command" == *"cat"* ]]; then
  if [[ -n "${FAKE_ALWAYS_FAIL_RUN:-}" ]]; then
    exit 1
  fi
  cat "$FAKE_RUN_JSON"
else
  printf 'ok\n'
fi
EOF
chmod 0755 "$TEST_ROOT/bin/ssh"
cat >"$TEST_ROOT/bin/scp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$FAKE_SCP_LOG"
EOF
chmod 0755 "$TEST_ROOT/bin/scp"

printf '%s\n' '{"mode":"gui-launch-connect","proofBoundary":"system-route","passed":true}' >"$TEST_ROOT/run.json"
printf '%s\n' '{"processStopped":true,"sessionDisconnected":true}' >"$TEST_ROOT/cleanup.json"
export FAKE_DROP_MARKER="$TEST_ROOT/drop.marker" FAKE_RUN_JSON="$TEST_ROOT/run.json" FAKE_CLEANUP_JSON="$TEST_ROOT/cleanup.json"
export FAKE_SSH_LOG="$TEST_ROOT/ssh.log"
export FAKE_SCP_LOG="$TEST_ROOT/scp.log"

RESULT="$TEST_ROOT/result.json"
STATE="$TEST_ROOT/state.json"
WT_MAC_SSH_TARGET=test@mac WT_MAC_SSH_PORT=2222 \
WT_MAC_SSH_BIN="$TEST_ROOT/bin/ssh" WT_MAC_SCP_BIN="$TEST_ROOT/bin/scp" \
WT_MAC_RESULT="$RESULT" WT_MAC_STATE="$STATE" WT_MAC_REMOTE_PASSWORD_FILE=/tmp/wt-test-sudo-password \
MAC_SUDO=unit-test-secret \
WT_MAC_REMOTE_RESULT=/tmp/wt-recovery-result.json \
bash "$WRAPPER" --bundle "$APP" --node node-test

python3 - "$RESULT" <<'PY'
import json, pathlib, sys
result = json.loads(pathlib.Path(sys.argv[1]).read_text())
assert result["passed"] is True, result
assert result["remote"]["passed"] is True, result
assert result["cleanup"]["processStopped"] is True, result
PY
grep -Fq 'kill -0' "$FAKE_SSH_LOG"
grep -Fq 'kill -KILL' "$FAKE_SSH_LOG"
grep -Fq 'source ~/.bashrc' "$FAKE_SSH_LOG"
grep -Fq 'DYLD_FRAMEWORK_PATH' "$FAKE_SSH_LOG"
grep -Fq 'open -n' "$FAKE_SSH_LOG"
grep -Fq -- '--wt-test-sudo-file' "$FAKE_SSH_LOG"
grep -Fq 'rm -rf' "$FAKE_SSH_LOG"
grep -Fq '/tmp/wt-test-sudo-password' "$FAKE_SCP_LOG"
! grep -Fq 'unit-test-secret' "$FAKE_SCP_LOG"
echo "mac acceptance result-recovery contract: OK"

FAIL_RESULT="$TEST_ROOT/fail-result.json"
FAIL_STATE="$TEST_ROOT/fail-state.json"
: >"$FAKE_SSH_LOG"
set +e
FAKE_ALWAYS_FAIL_RUN=1 WT_MAC_RECOVERY_ATTEMPTS=1 WT_MAC_RECOVERY_DELAY_SECONDS=0 \
WT_MAC_SSH_TARGET=test@mac WT_MAC_SSH_PORT=2222 \
WT_MAC_SSH_BIN="$TEST_ROOT/bin/ssh" WT_MAC_SCP_BIN="$TEST_ROOT/bin/scp" \
WT_MAC_RESULT="$FAIL_RESULT" WT_MAC_STATE="$FAIL_STATE" \
bash "$WRAPPER" --bundle "$APP" --node node-test >/dev/null 2>&1
FAIL_RC=$?
set -e
[[ "$FAIL_RC" -ne 0 ]]
grep -Fq '__WT_FAKE_CLEANUP__' "$FAKE_SSH_LOG"
echo "mac acceptance cleanup-on-GUI-failure contract: OK"
