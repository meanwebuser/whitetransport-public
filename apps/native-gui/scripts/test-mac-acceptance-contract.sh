#!/usr/bin/env bash
# Linux-safe contract test for the resumable Mac package acceptance wrapper.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WRAPPER="$ROOT_DIR/apps/native-gui/scripts/mac-acceptance.sh"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/wt-mac-acceptance.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT

APP="$TEST_ROOT/WhiteTransport.app"
mkdir -p "$APP/Contents/MacOS"
printf '#!/bin/sh\n' >"$APP/Contents/MacOS/WhiteTransport"
chmod 0755 "$APP/Contents/MacOS/WhiteTransport"

FAKE_BIN="$TEST_ROOT/bin"
mkdir -p "$FAKE_BIN"
cat >"$FAKE_BIN/ssh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$FAKE_SSH_LOG"
command=${*: -1}
case "$command" in
  *"uname -s"*) printf 'Darwin\n' ;;
  *"__WT_FAKE_INSTALL__"*) printf 'installed\n' ;;
  *"__WT_FAKE_RUN__"*) cat "${FAKE_REMOTE_RUN_JSON:?}"; printf '\n' ;;
  *"__WT_FAKE_CLEANUP__"*) cat "${FAKE_REMOTE_CLEANUP_JSON:?}"; printf '\n' ;;
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
assert state["phase"] == "complete", state
PY

before=$(wc -l <"$FAKE_SCP_LOG")
WT_MAC_SSH_TARGET=test@mac WT_MAC_SSH_PORT=2222 \
WT_MAC_SSH_BIN="$FAKE_BIN/ssh" WT_MAC_SCP_BIN="$FAKE_BIN/scp" \
WT_MAC_RESULT="$RESULT" WT_MAC_STATE="$STATE" WT_MAC_FAKE_REMOTE=1 \
bash "$WRAPPER" --resume --bundle "$APP" --node node-test
after=$(wc -l <"$FAKE_SCP_LOG")
test "$before" -eq "$after"

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
