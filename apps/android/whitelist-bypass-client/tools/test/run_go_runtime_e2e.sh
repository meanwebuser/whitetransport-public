#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
REPO_ROOT="$(cd "$ROOT/../../.." && pwd)"
cd "$ROOT"

if [[ -d "$HOME/Android/Sdk" ]]; then
  export ANDROID_HOME="${ANDROID_HOME:-$HOME/Android/Sdk}"
  export ANDROID_SDK_ROOT="${ANDROID_SDK_ROOT:-$HOME/Android/Sdk}"
  export PATH="$ANDROID_SDK_ROOT/platform-tools:$ANDROID_SDK_ROOT/emulator:$PATH"
fi

ADB="${ADB:-adb}"
GRADLE="${GRADLE:-./gradlew}"
DEVICE="${DEVICE:-${ANDROID_SERIAL:-}}"
ANDROID_USER_ID="${WT_ANDROID_USER_ID:-}"
CONFIG_JSON="${WT_ANDROID_RUNTIME_CONFIG_JSON:-}"
CONFIG_FILE="${WT_ANDROID_RUNTIME_CONFIG_FILE:-}"
DEVICE_CONFIG_FILE="${WT_ANDROID_RUNTIME_DEVICE_CONFIG_FILE:-/data/local/tmp/wt-runtime-config.json}"
APP_APK="${WT_ANDROID_APP_APK:-}"
NODE_ID="${WT_NODE_ID:-}"
EXPECTED_EGRESS_CARRIER="${WT_EXPECTED_EGRESS_CARRIER:-}"
EGRESS_ENDPOINT_ID="${WT_EGRESS_ENDPOINT_ID:-}"
PROBE_HOST="${WT_PROBE_HOST:-}"
PROBE_PORT="${WT_PROBE_PORT:-}"
PROBE_PATH="${WT_PROBE_PATH:-}"
PROBE_EXPECTED="${WT_PROBE_EXPECTED:-}"
BUILD_MOBILE_AAR="${WT_BUILD_MOBILE_AAR:-0}"
TEST_TIMEOUT_MS="${WT_ANDROID_TEST_TIMEOUT_MS:-150000}"
INSTRUMENTATION_TIMEOUT_SECONDS="${WT_ANDROID_INSTRUMENTATION_TIMEOUT_SECONDS:-180}"
RUNTIME_AAR="$ROOT/app/libs/wt-runtime.aar"
LOG="${LOG:-$REPO_ROOT/.tmp/android-go-runtime-e2e/wt-android-go-runtime-e2e-$(date +%Y%m%d_%H%M%S).log}"
RESULT_LOG="${INSTRUMENTATION_RESULT_LOG:-${LOG}.instrumentation}"
RESULT_CHECKER="$ROOT/tools/test/check_instrumentation_result.py"
PUSHED_DEVICE_CONFIG=0

if [[ ! "$TEST_TIMEOUT_MS" =~ ^[1-9][0-9]*$ || ! "$INSTRUMENTATION_TIMEOUT_SECONDS" =~ ^[1-9][0-9]*$ ]]; then
  echo "WT_ANDROID_TEST_TIMEOUT_MS and WT_ANDROID_INSTRUMENTATION_TIMEOUT_SECONDS must be positive integers." >&2
  exit 2
fi
if ! command -v timeout >/dev/null 2>&1; then
  echo "GNU timeout is required to bound Android instrumentation." >&2
  exit 2
fi
mkdir -p "$(dirname "$LOG")" "$(dirname "$RESULT_LOG")"

if [[ -z "$CONFIG_JSON" && -n "$CONFIG_FILE" ]]; then
  CONFIG_JSON="$(python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1]))))' "$CONFIG_FILE")"
fi

if [[ -z "$CONFIG_JSON" ]]; then
  echo "Set WT_ANDROID_RUNTIME_CONFIG_JSON or WT_ANDROID_RUNTIME_CONFIG_FILE." >&2
  exit 2
fi

say() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "$LOG"; }
quote_android_shell_arg() {
  local value="$1"
  value="${value//\'/\'\\\'\'}"
  printf "'%s'" "$value"
}
remote_shell_join() {
  local output="" arg quoted
  for arg in "$@"; do
    quoted="$(quote_android_shell_arg "$arg")"
    output="${output:+$output }$quoted"
  done
  printf '%s' "$output"
}
cleanup() {
  if [[ "$PUSHED_DEVICE_CONFIG" == "1" ]]; then
    "${ADB_BASE[@]}" shell rm -f "$DEVICE_CONFIG_FILE" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

say "checking adb devices"
"$ADB" start-server >/dev/null 2>&1 || true
mapfile -t DEVICE_LIST < <("$ADB" devices | awk 'NR>1 && $2=="device" {print $1}')
if [[ "${#DEVICE_LIST[@]}" -eq 0 ]]; then
  say "ERROR: no online adb device/emulator"
  exit 2
fi
if [[ -z "$DEVICE" ]]; then
  if [[ "${#DEVICE_LIST[@]}" -gt 1 ]]; then
    say "ERROR: several adb devices online; set DEVICE=<serial>"
    printf '%s\n' "${DEVICE_LIST[@]}" | tee -a "$LOG"
    exit 2
  fi
  DEVICE="${DEVICE_LIST[0]}"
fi
DEVICE_FOUND=0
for candidate in "${DEVICE_LIST[@]}"; do
  if [[ "$candidate" == "$DEVICE" ]]; then DEVICE_FOUND=1; break; fi
done
if [[ "$DEVICE_FOUND" != "1" ]]; then
  say "ERROR: selected adb device is not online: $DEVICE"
  exit 2
fi
export ANDROID_SERIAL="$DEVICE"
ADB_BASE=("$ADB" -s "$DEVICE")
if [[ -z "$ANDROID_USER_ID" ]]; then
  ANDROID_USER_ID="$("${ADB_BASE[@]}" shell am get-current-user 2>/dev/null | tr -d '\r')"
fi
if [[ ! "$ANDROID_USER_ID" =~ ^[0-9]+$ ]]; then
  say "ERROR: Android current user is not a numeric ID: ${ANDROID_USER_ID:-missing}"
  exit 2
fi
say "device=$DEVICE user=$ANDROID_USER_ID"

if [[ "$BUILD_MOBILE_AAR" == "1" ]]; then
  say "building current Go runtime AAR"
  ANDROID_HOME="$ANDROID_HOME" ANDROID_SDK_ROOT="$ANDROID_SDK_ROOT" \
    "$REPO_ROOT/ops/build/build-mobile-runtime-android.sh" 2>&1 | tee -a "$LOG"
fi
if [[ ! -s "$RUNTIME_AAR" ]]; then
  say "ERROR: Go runtime AAR missing: $RUNTIME_AAR. Run the canonical server44 build sync or set WT_BUILD_MOBILE_AAR=1 on a host with a current Go/gomobile toolchain."
  exit 2
fi

say "assembling debug app + androidTest"
ALLOW_EMPTY_MOBILE_SECRETS=1 ALLOW_NO_AUTOUPDATE=1 WT_USE_GO_RUNTIME_AAR=1 \
  "$GRADLE" --no-daemon --console=plain :app:assembleDebug :app:assembleDebugAndroidTest 2>&1 | tee -a "$LOG"

say "installing APKs"
if [[ -n "$APP_APK" ]]; then
  if [[ ! -s "$APP_APK" ]]; then
    say "ERROR: WT_ANDROID_APP_APK does not exist or is empty: $APP_APK"
    exit 2
  fi
  say "installing acceptance app APK=$APP_APK"
  "${ADB_BASE[@]}" install -r "$APP_APK" 2>&1 | tee -a "$LOG"
else
  "${ADB_BASE[@]}" install -r app/build/outputs/apk/debug/app-debug.apk 2>&1 | tee -a "$LOG"
fi
"${ADB_BASE[@]}" install -r app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk 2>&1 | tee -a "$LOG"

ARGS=(
  -e class bypass.whitelist.runtime.GoRuntimeInstrumentedTest
  -e testTimeoutMs "$TEST_TIMEOUT_MS"
)
if [[ -n "$CONFIG_FILE" ]]; then
  say "pushing runtime config to device"
  "${ADB_BASE[@]}" push "$CONFIG_FILE" "$DEVICE_CONFIG_FILE" >/dev/null
  PUSHED_DEVICE_CONFIG=1
  "${ADB_BASE[@]}" shell chmod 0644 "$DEVICE_CONFIG_FILE" || true
  ARGS+=(-e runtimeConfigFile "$DEVICE_CONFIG_FILE")
elif [[ -n "$CONFIG_JSON" ]]; then
  ARGS+=(-e runtimeConfigJson "$CONFIG_JSON")
fi
if [[ -n "$NODE_ID" ]]; then ARGS+=(-e nodeId "$NODE_ID"); fi
if [[ -n "$EXPECTED_EGRESS_CARRIER" ]]; then
  ARGS+=(-e expectedEgressCarrier "$EXPECTED_EGRESS_CARRIER")
fi
if [[ -n "$EGRESS_ENDPOINT_ID" ]]; then
  ARGS+=(-e egressEndpointId "$EGRESS_ENDPOINT_ID")
fi
if [[ -n "$PROBE_HOST" ]]; then ARGS+=(-e probeHost "$PROBE_HOST"); fi
if [[ -n "$PROBE_PORT" ]]; then ARGS+=(-e probePort "$PROBE_PORT"); fi
if [[ -n "$PROBE_PATH" ]]; then ARGS+=(-e probePath "$PROBE_PATH"); fi
if [[ -n "$PROBE_EXPECTED" ]]; then ARGS+=(-e probeExpected "$PROBE_EXPECTED"); fi

say "running Go runtime instrumentation (testTimeoutMs=$TEST_TIMEOUT_MS commandTimeoutSeconds=$INSTRUMENTATION_TIMEOUT_SECONDS expectedCarrier=${EXPECTED_EGRESS_CARRIER:-adaptive} endpoint=${EGRESS_ENDPOINT_ID:-auto})"
: >"$RESULT_LOG"
INSTRUMENT_COMMAND="$(remote_shell_join am instrument --user "$ANDROID_USER_ID" -w -r "${ARGS[@]}" bypass.whitelist.test/androidx.test.runner.AndroidJUnitRunner)"
set +e
timeout --foreground --signal=TERM --kill-after=10s "${INSTRUMENTATION_TIMEOUT_SECONDS}s" \
  "${ADB_BASE[@]}" shell "$INSTRUMENT_COMMAND" \
  2>&1 | tee -a "$LOG" "$RESULT_LOG"
RC=${PIPESTATUS[0]}
set -e
if [[ $RC -eq 0 ]]; then
  set +e
  python3 "$RESULT_CHECKER" "$RESULT_LOG" 2>&1 | tee -a "$LOG"
  RESULT_RC=${PIPESTATUS[0]}
  set -e
  if [[ $RESULT_RC -ne 0 ]]; then RC=1; fi
fi
if [[ $RC -ne 0 ]]; then
  LAST_STAGE="$(sed -n 's/^INSTRUMENTATION_STATUS: wt_stage=//p' "$RESULT_LOG" | tail -n 1)"
  if [[ $RC -eq 124 ]]; then
    say "ERROR: instrumentation timed out after ${INSTRUMENTATION_TIMEOUT_SECONDS}s last_stage=${LAST_STAGE:-unknown}"
    say "capturing timeout diagnostics"
    "${ADB_BASE[@]}" shell pidof bypass.whitelist bypass.whitelist.test 2>&1 | tee -a "$LOG" || true
    "${ADB_BASE[@]}" shell dumpsys activity processes 2>&1 | tail -n 120 | tee -a "$LOG" || true
    "${ADB_BASE[@]}" logcat -d -t 250 -s WT-GoRuntimeE2E:I GoLog:I AndroidRuntime:E 2>&1 | tee -a "$LOG" || true
    "${ADB_BASE[@]}" shell am force-stop bypass.whitelist.test >/dev/null 2>&1 || true
    "${ADB_BASE[@]}" shell am force-stop bypass.whitelist >/dev/null 2>&1 || true
  fi
  say "FAILED rc=$RC last_stage=${LAST_STAGE:-unknown} log=$LOG"
  exit "$RC"
fi
say "OK log=$LOG"
