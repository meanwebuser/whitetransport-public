#!/usr/bin/env bash
# Verify that the installed-TUN instrumentation lane keeps its UID rule active
# long enough for independent host-side payload and route probes.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DEVICE="${DEVICE:-${ANDROID_SERIAL:-}}"
ADB="${ADB:-adb}"
XRAY="${XRAY:-/usr/local/bin/xray}"
HOLD_SECONDS="${HOLD_SECONDS:-12}"
TEST_CLASS="bypass.whitelist.RuntimeLaunchInstrumentedTest#foregroundTunnelPublishesActiveAndCleansUpWithInstalledSplitPackage"
TEST_COMPONENT="bypass.whitelist.test/androidx.test.runner.AndroidJUnitRunner"

if [[ -z "$DEVICE" ]]; then
  printf 'DEVICE or ANDROID_SERIAL is required.\n' >&2
  exit 2
fi

ADB_BASE=("$ADB" -s "$DEVICE")
WORK="$(mktemp -d "${TMPDIR:-/tmp}/wt-system-vpn-hold.XXXXXX")"
XRAY_PID=""
INSTRUMENTATION_PID=""

cleanup() {
  if [[ -n "$INSTRUMENTATION_PID" ]] && kill -0 "$INSTRUMENTATION_PID" 2>/dev/null; then
    kill "$INSTRUMENTATION_PID" 2>/dev/null || true
    wait "$INSTRUMENTATION_PID" 2>/dev/null || true
  fi
  "${ADB_BASE[@]}" shell am force-stop bypass.whitelist >/dev/null 2>&1 || true
  "${ADB_BASE[@]}" reverse --remove tcp:1085 >/dev/null 2>&1 || true
  if [[ -n "$XRAY_PID" ]] && kill -0 "$XRAY_PID" 2>/dev/null; then
    kill "$XRAY_PID" 2>/dev/null || true
    wait "$XRAY_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

python3 - "$WORK/xray.json" <<'PY'
import json
import sys
from pathlib import Path

Path(sys.argv[1]).write_text(json.dumps({
    "log": {"loglevel": "warning"},
    "inbounds": [{
        "tag": "android-proof-socks",
        "listen": "127.0.0.1",
        "port": 1085,
        "protocol": "socks",
        "settings": {"auth": "noauth", "udp": False},
    }],
    "outbounds": [{"tag": "direct", "protocol": "freedom"}],
}) + "\n", encoding="utf-8")
PY

"$XRAY" run -config "$WORK/xray.json" >"$WORK/xray.log" 2>&1 &
XRAY_PID=$!
for _ in $(seq 1 50); do
  if (echo >/dev/tcp/127.0.0.1/1085) >/dev/null 2>&1; then break; fi
  sleep 0.1
done
"${ADB_BASE[@]}" reverse tcp:1085 tcp:1085 >/dev/null

"${ADB_BASE[@]}" shell am force-stop bypass.whitelist >/dev/null 2>&1 || true
"${ADB_BASE[@]}" shell am instrument -w -r \
  -e class "$TEST_CLASS" -e requireTun true -e holdTunSeconds "$HOLD_SECONDS" \
  "$TEST_COMPONENT" >"$WORK/instrumentation.log" 2>&1 &
INSTRUMENTATION_PID=$!

rule_seen_ms=""
rule_gone_ms=""
deadline=$((SECONDS + 30))
while (( SECONDS < deadline )); do
  now_ms="$(date +%s%3N)"
  if "${ADB_BASE[@]}" shell ip rule show 2>/dev/null | grep -q 'uidrange 10254-10254'; then
    rule_seen_ms="${rule_seen_ms:-$now_ms}"
  elif [[ -n "$rule_seen_ms" ]]; then
    rule_gone_ms="$now_ms"
    break
  fi
  if ! kill -0 "$INSTRUMENTATION_PID" 2>/dev/null && [[ -z "$rule_seen_ms" ]]; then
    break
  fi
  sleep 0.05
done

set +e
wait "$INSTRUMENTATION_PID"
instrumentation_rc=$?
set -e
INSTRUMENTATION_PID=""

if [[ -z "$rule_seen_ms" ]]; then
  sed -n '1,240p' "$WORK/instrumentation.log" >&2
  printf 'Chrome UID rule was never observed.\n' >&2
  exit 1
fi
if [[ -z "$rule_gone_ms" ]]; then
  rule_gone_ms="$(date +%s%3N)"
fi
held_ms=$((rule_gone_ms - rule_seen_ms))
minimum_ms=$(((HOLD_SECONDS - 2) * 1000))
printf 'uid_rule_held_ms=%s requested_seconds=%s instrumentation_rc=%s\n' "$held_ms" "$HOLD_SECONDS" "$instrumentation_rc"
if (( instrumentation_rc != 0 )); then
  sed -n '1,240p' "$WORK/instrumentation.log" >&2
  exit "$instrumentation_rc"
fi
if (( held_ms < minimum_ms )); then
  printf 'Chrome UID rule disappeared before the external-probe hold elapsed (%s < %s ms).\n' "$held_ms" "$minimum_ms" >&2
  exit 1
fi
