#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../.." && pwd)"
ADB="${ADB:-adb}"
DEVICE="${DEVICE:-${ANDROID_SERIAL:-}}"
PKG="${PKG:-bypass.whitelist}"
ACTIVITY="${ACTIVITY:-.CapacitorMainActivity}"
APK="${APK:-$REPO_ROOT/artifacts/server44/android/WhiteTransport-dev-release.apk}"
SKIP_INSTALL="${SKIP_INSTALL:-1}"
WAIT_CONNECT_SECONDS="${WAIT_CONNECT_SECONDS:-90}"
WAIT_DISCONNECT_SECONDS="${WAIT_DISCONNECT_SECONDS:-30}"
UI_DUMP_TIMEOUT_SECONDS="${UI_DUMP_TIMEOUT_SECONDS:-5}"
RUN_ID="${RUN_ID:-android-ui-$(date +%Y%m%dT%H%M%S)}"
RESULT="${RESULT:-$REPO_ROOT/output/android-auto-debug/$RUN_ID.json}"
LOCK_DIR="${RESULT}.lock"
UI_PROBE="${UI_PROBE:-$(dirname "${BASH_SOURCE[0]}")/android_auto_debug_ui.py}"

mkdir -p "$(dirname "$RESULT")"
if ! mkdir "$LOCK_DIR" 2>/dev/null; then
  echo "Android auto-debug is already running for result: $RESULT" >&2
  exit 2
fi
TRACE_FILE="$(mktemp)"
LOGCAT_PID=""
ADB_BASE=()
PREVIOUS_STAY_AWAKE=""
STAY_AWAKE_CHANGED=false
cleanup() {
  if [[ -n "$LOGCAT_PID" ]]; then kill "$LOGCAT_PID" 2>/dev/null || true; fi
  if (( ${#ADB_BASE[@]} > 0 )); then
    "${ADB_BASE[@]}" shell am force-stop "$PKG" >/dev/null 2>&1 || true
    if [[ "$STAY_AWAKE_CHANGED" == true && "$PREVIOUS_STAY_AWAKE" =~ ^[0-9]+$ ]]; then
      "${ADB_BASE[@]}" shell settings put global stay_on_while_plugged_in "$PREVIOUS_STAY_AWAKE" >/dev/null 2>&1 || true
    fi
  fi
  rm -f "$TRACE_FILE"
  rmdir "$LOCK_DIR" 2>/dev/null || true
}
trap cleanup EXIT

STAGE="starting"
PASSED="false"
UI_STATUS="Unknown"
ERROR_MESSAGE=""
NATIVE_LOGS=""
STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

write_result() {
  mkdir -p "$(dirname "$RESULT")"
  WT_DEBUG_RESULT="$RESULT" WT_DEBUG_RUN_ID="$RUN_ID" WT_DEBUG_DEVICE="$DEVICE" \
    WT_DEBUG_PACKAGE="$PKG" WT_DEBUG_STAGE="$STAGE" WT_DEBUG_PASSED="$PASSED" \
    WT_DEBUG_UI_STATUS="$UI_STATUS" WT_DEBUG_ERROR="$ERROR_MESSAGE" \
    WT_DEBUG_STARTED_AT="$STARTED_AT" WT_DEBUG_LOGS="$NATIVE_LOGS" \
    python3 - <<'PY'
import json
import os
import re
from datetime import datetime, timezone
from pathlib import Path

def clean(value: str) -> str:
    value = value[:1000]
    value = re.sub(r"vk1\.[A-Za-z0-9._-]+", "[REDACTED_VK_TOKEN]", value)
    value = re.sub(r"eyJ[A-Za-z0-9._-]{20,}", "[REDACTED_JWT]", value)
    value = re.sub(r"(?i)(token|cookie|authorization)=\S+", r"\1=[REDACTED]", value)
    return value

path = Path(os.environ["WT_DEBUG_RESULT"])
payload = {
    "schemaVersion": 1,
    "mode": "android-ui-auto-debug",
    "runId": os.environ["WT_DEBUG_RUN_ID"],
    "device": clean(os.environ["WT_DEBUG_DEVICE"]),
    "package": os.environ["WT_DEBUG_PACKAGE"],
    "stage": os.environ["WT_DEBUG_STAGE"],
    "passed": os.environ["WT_DEBUG_PASSED"] == "true",
    "uiStatus": os.environ["WT_DEBUG_UI_STATUS"],
    "error": clean(os.environ["WT_DEBUG_ERROR"]),
    "startedAt": os.environ["WT_DEBUG_STARTED_AT"],
    "updatedAt": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
    "nativeMarkers": [clean(line) for line in os.environ["WT_DEBUG_LOGS"].splitlines()[-40:]],
}
temporary = path.with_suffix(path.suffix + ".tmp")
temporary.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
temporary.chmod(0o600)
os.replace(temporary, path)
PY
}

fail() {
  ERROR_MESSAGE="$1"
  STAGE="${2:-failed}"
  PASSED="false"
  collect_logs
  write_result
  printf 'Android auto-debug failed: %s\nResult: %s\n' "$ERROR_MESSAGE" "$RESULT" >&2
  exit 1
}

collect_logs() {
  NATIVE_LOGS="$(tail -n 40 "$TRACE_FILE" 2>/dev/null || true)"
}

read_ui() {
  local device_xml="/data/local/tmp/wt-auto-debug-ui.xml"
  if ! timeout "$UI_DUMP_TIMEOUT_SECONDS" "${ADB_BASE[@]}" shell uiautomator dump "$device_xml" >/dev/null 2>&1; then
    "${ADB_BASE[@]}" shell pkill uiautomator >/dev/null 2>&1 || true
    return 0
  fi
  timeout 3 "${ADB_BASE[@]}" exec-out cat "$device_xml" 2>/dev/null || true
}

accept_system_vpn_dialog() {
  local snapshot center
  snapshot="$(read_ui)"
  center="$(python3 -c '
import re
import sys

xml = sys.stdin.read()
accepted = re.compile(r"^(?:ОК|OK|Allow|Разрешить|Да|Yes)$", re.I)
for match in re.finditer(r"<node\b[^>]*\btext=\"([^\"]*)\"[^>]*\bbounds=\"\[(\d+),(\d+)\]\[(\d+),(\d+)\]\"", xml):
    if not accepted.fullmatch(match.group(1).strip()):
        continue
    left, top, right, bottom = map(int, match.groups()[1:])
    print((left + right) // 2, (top + bottom) // 2)
    break
  ' <<<"$snapshot"
)"
  if [[ -z "$center" ]]; then
    return 1
  fi
  read -r tap_x tap_y <<<"$center"
  "${ADB_BASE[@]}" shell input tap "$tap_x" "$tap_y" >/dev/null 2>&1 || return 1
  sleep 2
  return 0
}

find_connect_center() {
  python3 "$UI_PROBE" connect
}

find_disconnect_center() {
  python3 "$UI_PROBE" disconnect
}

has_connect_control() {
  python3 "$UI_PROBE" has-connect
}

is_connected() {
  python3 "$UI_PROBE" connected
}

is_disconnected() {
  python3 "$UI_PROBE" disconnected
}

disconnect_after_proof() {
  local center tap_x tap_y deadline snapshot
  center="$(printf '%s' "$UI_XML" | find_disconnect_center)" || return 1
  read -r tap_x tap_y <<<"$center"
  "${ADB_BASE[@]}" shell input tap "$tap_x" "$tap_y" >/dev/null || return 1
  deadline=$((SECONDS + WAIT_DISCONNECT_SECONDS))
  while (( SECONDS < deadline )); do
    snapshot="$(read_ui)"
    if printf '%s' "$snapshot" | is_disconnected; then
      return 0
    fi
    sleep 1
  done
  return 1
}

read_ui_error() {
  python3 -c 'import html,re,sys; texts=[html.unescape(x) for x in re.findall(r"text=\"([^\"]*)\"", sys.stdin.read())]; print(next((texts[i+1] for i,x in enumerate(texts[:-1]) if x=="Connection failed" and texts[i+1]), ""))'
}

device_is_unlocked() {
  local window_state power_state
  window_state="$("${ADB_BASE[@]}" shell dumpsys window 2>/dev/null || true)"
  if grep -Eiq 'mKeyguardShowing=true|mShowingLockscreen=true|mDreamingLockscreen=true|isKeyguardShowing=true' <<<"$window_state"; then
    return 1
  fi

  power_state="$("${ADB_BASE[@]}" shell dumpsys power 2>/dev/null || true)"
  if grep -Eiq 'mWakefulness=(Dozing|Asleep)' <<<"$power_state"; then
    return 1
  fi
  return 0
}

"$ADB" start-server >/dev/null 2>&1 || true
mapfile -t DEVICES < <("$ADB" devices | awk 'NR>1 && $2=="device" {print $1}')
if [[ -z "$DEVICE" ]]; then
  [[ "${#DEVICES[@]}" -eq 1 ]] || { echo "Set DEVICE when zero or multiple Android devices are online" >&2; exit 2; }
  DEVICE="${DEVICES[0]}"
fi
ADB_BASE=("$ADB" -s "$DEVICE")
PREVIOUS_STAY_AWAKE="$("${ADB_BASE[@]}" shell settings get global stay_on_while_plugged_in 2>/dev/null | tr -d '\r' || true)"
"${ADB_BASE[@]}" shell svc power stayon true >/dev/null 2>&1 || true
STAY_AWAKE_CHANGED=true
# Wake and dismiss the password-free controlled test device before checking
# lock state. Keyevent 82 opens Samsung's notification shade and is not a
# valid unlock action here.
"${ADB_BASE[@]}" shell input keyevent 224 >/dev/null 2>&1 || true
"${ADB_BASE[@]}" shell wm dismiss-keyguard >/dev/null 2>&1 || true
"${ADB_BASE[@]}" shell input tap 360 800 >/dev/null 2>&1 || true
"${ADB_BASE[@]}" shell input swipe 360 1300 360 400 350 >/dev/null 2>&1 || true
sleep 1
accept_system_vpn_dialog || true
write_result

STAGE="device_check"
write_result
device_is_unlocked || fail "device_locked" "device_check"

if [[ "$SKIP_INSTALL" != "1" ]]; then
  [[ -f "$APK" ]] || fail "APK not found: $APK"
  STAGE="installing"
  write_result
  "${ADB_BASE[@]}" install -r "$APK" >/dev/null || fail "APK install failed"
fi

STAGE="launching"
write_result
"${ADB_BASE[@]}" logcat -c >/dev/null 2>&1 || true
"${ADB_BASE[@]}" logcat -v brief -s WtTransportPlugin CapacitorVpnCoordinator NativeRuntimeActivity TunnelVPN >"$TRACE_FILE" 2>/dev/null &
LOGCAT_PID=$!
"${ADB_BASE[@]}" shell input keyevent 3 >/dev/null 2>&1 || true
"${ADB_BASE[@]}" shell input keyevent 4 >/dev/null 2>&1 || true
"${ADB_BASE[@]}" shell am force-stop "$PKG" >/dev/null 2>&1 || true
# Samsung's activity manager can return error -102 for an asynchronous launch
# while the activity is still starting; wait for the completion status before
# probing the UI so the runner does not report a false missing-control failure.
"${ADB_BASE[@]}" shell am start -W -n "$PKG/$ACTIVITY" >/dev/null || fail "launcher activity failed"

UI_XML=""
READY_DEADLINE=$((SECONDS + 30))
while (( SECONDS < READY_DEADLINE )); do
  UI_XML="$(read_ui)"
  accept_system_vpn_dialog || true
  printf '%s' "$UI_XML" | has_connect_control && break
  sleep 1
done
printf '%s' "$UI_XML" | has_connect_control || fail "Connect WhiteTransport control not found"

STAGE="connecting"
write_result
CENTER="$(printf '%s' "$UI_XML" | find_connect_center)" || fail "connect control has no usable bounds"
read -r TAP_X TAP_Y <<<"$CENTER"
"${ADB_BASE[@]}" shell input tap "$TAP_X" "$TAP_Y" >/dev/null || fail "connect tap failed"

# A debug APK has its own Android UID and can legitimately trigger a fresh
# system VPN consent dialog even when the release APK was already approved.
for _ in $(seq 1 30); do
  if grep -Eq 'WT_RUNTIME_UI (connected|failed) backend=' "$TRACE_FILE" 2>/dev/null; then
    break
  fi
  accept_system_vpn_dialog && break
  sleep 1
done

NATIVE_DEADLINE=$((SECONDS + WAIT_CONNECT_SECONDS))
while (( SECONDS < NATIVE_DEADLINE )); do
  if grep -Eq 'WT_RUNTIME_UI (connected|failed) backend=' "$TRACE_FILE" 2>/dev/null; then
    break
  fi
  sleep 1
done
if grep -q 'WT_RUNTIME_UI failed backend=' "$TRACE_FILE" 2>/dev/null; then
  ERROR_MESSAGE="$(grep 'WT_RUNTIME_UI failed backend=' "$TRACE_FILE" | tail -n 1)"
  fail "$ERROR_MESSAGE"
fi
if ! grep -q 'WT_RUNTIME_UI connected backend=' "$TRACE_FILE" 2>/dev/null; then
  UI_XML="$(read_ui)"
  ERROR_MESSAGE="$(printf '%s' "$UI_XML" | read_ui_error)"
  fail "${ERROR_MESSAGE:-native connect marker was not reached within ${WAIT_CONNECT_SECONDS}s}"
fi

"${ADB_BASE[@]}" shell am start -n "$PKG/$ACTIVITY" >/dev/null || fail "could not restore the app before the UI snapshot"
sleep 3
UI_XML="$(read_ui)"
if printf '%s' "$UI_XML" | is_connected; then
  PASSED="true"
  UI_STATUS="Connected"
  STAGE="complete"
  disconnect_after_proof || fail "Connected was proved but cleanup disconnect did not complete within ${WAIT_DISCONNECT_SECONDS}s"
  collect_logs
  write_result
  printf 'Android auto-debug passed\nResult: %s\n' "$RESULT"
  exit 0
fi
if [[ "$UI_XML" == *'text="Connection failed"'* ]]; then
  ERROR_MESSAGE="$(printf '%s' "$UI_XML" | read_ui_error)"
  fail "${ERROR_MESSAGE:-connection failed without a UI error message}"
fi

UI_STATUS="Disconnected"
fail "native connected but one bounded UI snapshot did not show Connected"
