#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
if [[ -d "$HOME/Android/Sdk" ]]; then
  export ANDROID_HOME="${ANDROID_HOME:-$HOME/Android/Sdk}"
  export ANDROID_SDK_ROOT="${ANDROID_SDK_ROOT:-$HOME/Android/Sdk}"
  export PATH="$ANDROID_SDK_ROOT/platform-tools:$ANDROID_SDK_ROOT/emulator:$PATH"
fi

ADB="${ADB:-adb}"
GRADLE="${GRADLE:-./gradlew}"
DEVICE="${DEVICE:-${ANDROID_SERIAL:-}}"
WAIT_PACKAGE_SECONDS="${WAIT_PACKAGE_SECONDS:-120}"
TEST_CLASS="${TEST_CLASS:-bypass.whitelist.discovery.WtBusPrivateBusInstrumentedTest}"
APP_APK="${APP_APK:-app/build/outputs/apk/debug/app-debug.apk}"
TEST_APK="${TEST_APK:-app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk}"
PKG="bypass.whitelist"
TEST_PKG="bypass.whitelist.test"
RUNNER="androidx.test.runner.AndroidJUnitRunner"
LOG="${LOG:-/tmp/wt-android-instrumented-$(date +%Y%m%d_%H%M%S).log}"

say() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "$LOG"; }

say "checking adb devices"
"$ADB" start-server >/dev/null 2>&1 || true
mapfile -t DEVICE_LIST < <("$ADB" devices | awk 'NR>1 && $2=="device" {print $1}')
if [[ "${#DEVICE_LIST[@]}" -eq 0 ]]; then
  say "ERROR: no online adb device/emulator. Start emulator first or connect Android device."
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

say "waiting for Android package service"
READY=0
for _ in $(seq 1 "$WAIT_PACKAGE_SECONDS"); do
  if "${ADB_BASE[@]}" shell service list 2>/dev/null | grep -q '^.*package:'; then
    READY=1
    break
  fi
  sleep 1
done
if [[ "$READY" != "1" ]]; then
  say "ERROR: emulator/device is online but Android package service is not ready"
  "$ADB" devices -l | tee -a "$LOG" || true
  echo "sys.boot_completed=$("${ADB_BASE[@]}" shell getprop sys.boot_completed 2>/dev/null || true)" | tee -a "$LOG"
  echo "init.svc.bootanim=$("${ADB_BASE[@]}" shell getprop init.svc.bootanim 2>/dev/null || true)" | tee -a "$LOG"
  "${ADB_BASE[@]}" shell service list 2>/dev/null | head -n 80 | tee -a "$LOG" || true
  exit 3
fi

say "assembling debug app + androidTest"
"$GRADLE" --no-daemon --console=plain :app:assembleDebug :app:assembleDebugAndroidTest 2>&1 | tee -a "$LOG"

say "installing APKs"
"${ADB_BASE[@]}" install -r "$APP_APK" 2>&1 | tee -a "$LOG"
"${ADB_BASE[@]}" install -r "$TEST_APK" 2>&1 | tee -a "$LOG"

say "running instrumentation: $TEST_CLASS"
set +e
"${ADB_BASE[@]}" shell am instrument -w -r -e class "$TEST_CLASS" "$TEST_PKG/$RUNNER" 2>&1 | tee -a "$LOG"
RC=${PIPESTATUS[0]}
set -e
if [[ $RC -ne 0 ]]; then
  say "FAILED rc=$RC log=$LOG"
  exit "$RC"
fi
say "OK log=$LOG"
