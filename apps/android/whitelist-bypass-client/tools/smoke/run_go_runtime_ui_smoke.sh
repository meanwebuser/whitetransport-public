#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

ADB="${ADB:-adb}"
DEVICE="${DEVICE:-${ANDROID_SERIAL:-}}"
APK="${APK:-app/build/outputs/apk/debug/app-debug.apk}"
PKG="${PKG:-bypass.whitelist}"
SKIP_INSTALL="${SKIP_INSTALL:-0}"
CONFIG_FILE="${WT_ANDROID_RUNTIME_CONFIG_FILE:-}"
DEVICE_CONFIG_FILE="${WT_ANDROID_RUNTIME_DEVICE_CONFIG_FILE:-/data/local/tmp/wt-runtime-config.json}"
WAIT_CONNECT_SECONDS="${WAIT_CONNECT_SECONDS:-75}"
TAP_X="${TAP_X:-540}"
TAP_Y="${TAP_Y:-650}"
LOG="${LOG:-/tmp/wt-android-go-runtime-ui-smoke-$(date +%Y%m%d_%H%M%S).log}"
UI_XML="/data/local/tmp/wt-ui-smoke.xml"

if [[ -z "$CONFIG_FILE" ]]; then
  echo "Set WT_ANDROID_RUNTIME_CONFIG_FILE to a generated config with token_store." >&2
  exit 2
fi

say() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "$LOG"; }

tap_connect() {
  if "${ADB_BASE[@]}" shell uiautomator dump "$UI_XML" >/dev/null 2>&1 && "${ADB_BASE[@]}" pull "$UI_XML" /tmp/wt-ui-smoke.xml >/dev/null 2>&1; then
    local bounds
    bounds="$(python3 - <<'PY'
import re
from pathlib import Path
xml = Path('/tmp/wt-ui-smoke.xml').read_text(errors='ignore')
for needle in ('Connect WhiteTransport', 'Connect'):
    m = re.search(r'(?:text|content-desc)="' + re.escape(needle) + r'"[^>]*bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"', xml)
    if m:
        x1, y1, x2, y2 = map(int, m.groups())
        print((x1 + x2) // 2, (y1 + y2) // 2)
        raise SystemExit(0)
raise SystemExit(1)
PY
)" || true
    if [[ -n "$bounds" ]]; then
      say "tapping connect from UI hierarchy at $bounds"
      "${ADB_BASE[@]}" shell input tap $bounds || true
      return
    fi
  fi
  say "tapping connect fallback at ${TAP_X},${TAP_Y}"
  "${ADB_BASE[@]}" shell input tap "$TAP_X" "$TAP_Y" || true
}

cleanup() {
  if [[ "${DEVICE_SELECTED:-0}" == "1" ]]; then
    "${ADB_BASE[@]}" shell rm -f "$DEVICE_CONFIG_FILE" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

say "checking adb device"
"$ADB" start-server >/dev/null 2>&1 || true
mapfile -t DEVICE_LIST < <("$ADB" devices | awk 'NR>1 && $2=="device" {print $1}')
if [[ "${#DEVICE_LIST[@]}" -eq 0 ]]; then
  say "FAIL: no online adb device/emulator"
  exit 2
fi
if [[ -z "$DEVICE" ]]; then
  if [[ "${#DEVICE_LIST[@]}" -gt 1 ]]; then
    say "FAIL: several adb devices online; set DEVICE=<serial>"
    exit 2
  fi
  DEVICE="${DEVICE_LIST[0]}"
fi
DEVICE_FOUND=0
for candidate in "${DEVICE_LIST[@]}"; do
  if [[ "$candidate" == "$DEVICE" ]]; then DEVICE_FOUND=1; break; fi
done
if [[ "$DEVICE_FOUND" != "1" ]]; then
  say "FAIL: selected adb device is not online: $DEVICE"
  exit 2
fi
export ANDROID_SERIAL="$DEVICE"
ADB_BASE=("$ADB" -s "$DEVICE")
DEVICE_SELECTED=1
say "device=$DEVICE"

if [[ "$SKIP_INSTALL" == "1" ]]; then
  say "skipping APK install"
else
  say "installing $APK"
  "${ADB_BASE[@]}" install -r "$APK" 2>&1 | tee -a "$LOG"
fi

say "provisioning app-private runtime config"
"${ADB_BASE[@]}" push "$CONFIG_FILE" "$DEVICE_CONFIG_FILE" >/dev/null
"${ADB_BASE[@]}" shell "cat '$DEVICE_CONFIG_FILE' | run-as '$PKG' sh -c 'mkdir -p files/wt-runtime && cat > files/wt-runtime/config.json'"
"${ADB_BASE[@]}" shell rm -f "$DEVICE_CONFIG_FILE" >/dev/null 2>&1 || true

say "launching app"
"${ADB_BASE[@]}" logcat -c || true
"${ADB_BASE[@]}" shell am force-stop "$PKG" || true
"${ADB_BASE[@]}" shell monkey -p "$PKG" -c android.intent.category.LAUNCHER 1 2>&1 | tee -a "$LOG"
sleep 5

tap_connect

say "waiting for native/gomobile UI connect marker"
FOUND=0
for _ in $(seq 1 "$WAIT_CONNECT_SECONDS"); do
  if "${ADB_BASE[@]}" logcat -d -v time | grep -E 'WT_RUNTIME_UI connected backend=(gomobile|native)' >>"$LOG"; then
    FOUND=1
    break
  fi
  sleep 1
done

if [[ "$FOUND" != "1" ]]; then
  say "FAIL: UI connect marker not found within ${WAIT_CONNECT_SECONDS}s"
  "${ADB_BASE[@]}" logcat -d -v time | grep -E 'WT_RUNTIME_UI|NativeRuntimeActivity|WtTransportPlugin|GoLog|whitetransport|bypass.whitelist' | tail -n 240 | tee -a "$LOG" || true
  exit 1
fi

say "OK log=$LOG"
