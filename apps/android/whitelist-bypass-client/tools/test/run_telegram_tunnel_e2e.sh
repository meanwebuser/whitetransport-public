#!/usr/bin/env bash
set -euo pipefail

APP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
REPO_ROOT="$(cd "$APP_DIR/../../.." && pwd)"
CORE_DIR="$REPO_ROOT/core/go"
LOG="${LOG:-/tmp/wt-telegram-tunnel-e2e-$(date +%Y%m%d_%H%M%S).log}"
REPORT="${REPORT:-/tmp/wt-telegram-tunnel-e2e-report-$(date +%Y%m%d_%H%M%S).txt}"
TARGET_URL="${TARGET_URL:-https://t.me/Kuplinov_Telegram/1032}"
PLANNER_HOST_URL="${PLANNER_HOST_URL:-http://127.0.0.1:17680}"
PLANNER_REVERSE_PORT="${PLANNER_REVERSE_PORT:-17680}"
PLANNER_DEVICE_URL="${PLANNER_DEVICE_URL:-http://127.0.0.1:${PLANNER_REVERSE_PORT}}"
PLANNER_CONFIG="${PLANNER_CONFIG:-$CORE_DIR/config.example.json}"
TEST_CLASS="bypass.whitelist.internet.TelegramThroughTunnelE2EInstrumentedTest"
PKG="${PKG:-bypass.whitelist}"

say() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "$LOG"; }

find_android_sdk() {
  if [[ -n "${ANDROID_HOME:-}" && -d "$ANDROID_HOME" ]]; then printf '%s\n' "$ANDROID_HOME"; return 0; fi
  if [[ -n "${ANDROID_SDK_ROOT:-}" && -d "$ANDROID_SDK_ROOT" ]]; then printf '%s\n' "$ANDROID_SDK_ROOT"; return 0; fi
  for candidate in "$HOME/Android/Sdk" /opt/android-sdk /usr/lib/android-sdk; do
    if [[ -d "$candidate" ]]; then printf '%s\n' "$candidate"; return 0; fi
  done
  return 1
}

SDK_DIR="$(find_android_sdk || true)"
if [[ -z "$SDK_DIR" ]]; then
  say "ERROR: Android SDK not found. Set ANDROID_HOME or ANDROID_SDK_ROOT."
  exit 2
fi
export ANDROID_HOME="$SDK_DIR"
export ANDROID_SDK_ROOT="$SDK_DIR"
export PATH="$ANDROID_SDK_ROOT/platform-tools:$ANDROID_SDK_ROOT/emulator:$PATH"
export ALLOW_EMPTY_MOBILE_SECRETS="${ALLOW_EMPTY_MOBILE_SECRETS:-1}"
export ALLOW_NO_AUTOUPDATE="${ALLOW_NO_AUTOUPDATE:-1}"

ADB="${ADB:-adb}"
GRADLE="${GRADLE:-./gradlew}"
DEVICE="${DEVICE:-${ANDROID_SERIAL:-}}"

cleanup() {
  if [[ "${PLANNER_REVERSE_CREATED:-0}" == "1" ]]; then
    "${ADB_BASE[@]}" reverse --remove "tcp:$PLANNER_REVERSE_PORT" >/dev/null 2>&1 || true
  fi
  if [[ -n "${PLANNER_PID:-}" ]]; then
    kill "$PLANNER_PID" >/dev/null 2>&1 || true
    wait "$PLANNER_PID" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

say "log=$LOG"
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
say "device=$DEVICE"

say "mapping device planner endpoint with adb reverse tcp:$PLANNER_REVERSE_PORT"
"${ADB_BASE[@]}" reverse "tcp:$PLANNER_REVERSE_PORT" "tcp:$PLANNER_REVERSE_PORT"
PLANNER_REVERSE_CREATED=1

say "starting Go planner at $PLANNER_HOST_URL"
(
  cd "$CORE_DIR"
  env GOCACHE="${GOCACHE:-/tmp/wt-gocache}" go run ./cmd/whitetransportd --config "$PLANNER_CONFIG" --serve
) >>"$LOG" 2>&1 &
PLANNER_PID=$!

for _ in $(seq 1 40); do
  if curl -fsS "$PLANNER_HOST_URL/health" >>"$LOG" 2>&1; then
    say "planner ready"
    break
  fi
  sleep 1
done
if ! curl -fsS "$PLANNER_HOST_URL/v1/plan?traffic=control&payload_bytes=4096" >>"$LOG" 2>&1; then
  say "ERROR: planner did not become ready at $PLANNER_HOST_URL"
  exit 3
fi

say "running Telegram tunnel E2E target=$TARGET_URL plannerDeviceUrl=$PLANNER_DEVICE_URL"
set +e
(
  cd "$APP_DIR"
  "$GRADLE" --no-daemon --console=plain connectedDebugAndroidTest \
    -Pandroid.testInstrumentationRunnerArguments.class="$TEST_CLASS" \
    -Pandroid.testInstrumentationRunnerArguments.targetUrl="$TARGET_URL" \
    -Pandroid.testInstrumentationRunnerArguments.plannerApiUrl="$PLANNER_DEVICE_URL"
) 2>&1 | tee -a "$LOG"
TEST_RC=${PIPESTATUS[0]}
set -e

say "pulling E2E report to $REPORT"
if "${ADB_BASE[@]}" shell run-as "$PKG" cat files/telegram-through-tunnel-e2e.txt >"$REPORT" 2>>"$LOG"; then
  {
    echo "--- telegram-through-tunnel-e2e.txt ---"
    cat "$REPORT"
    echo "--- end telegram-through-tunnel-e2e.txt ---"
  } | tee -a "$LOG"
else
  say "WARN: could not pull telegram-through-tunnel-e2e.txt from $PKG"
fi

if [[ "$TEST_RC" -ne 0 ]]; then
  say "FAILED rc=$TEST_RC log=$LOG report=$REPORT"
  exit "$TEST_RC"
fi
say "OK log=$LOG"
