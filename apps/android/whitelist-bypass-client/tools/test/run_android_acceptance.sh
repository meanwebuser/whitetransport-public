#!/usr/bin/env bash
# Run one installed-APK Android acceptance attempt and retain only redacted evidence.
set -euo pipefail
umask 077

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PKG="${PKG:-bypass.whitelist}"
ACTIVITY="${ACTIVITY:-.CapacitorMainActivity}"
ADB="${ADB:-adb}"
AGENT_DEVICE="${AGENT_DEVICE:-agent-device}"
CURL="${CURL:-curl}"
FAST_RUNNER="${FAST_RUNNER:-$ROOT/tools/test/run_android_auto_debug.sh}"
SLOW_RUNNER="${SLOW_RUNNER:-$ROOT/tools/test/run_go_runtime_e2e.sh}"

APK=""
DEVICE=""
RESULT_DIR=""
TELEMETRY_URL=""
HOST_LOG_PID=""
TELEMETRY_PID=""
STAGE="arguments"
ERROR_MESSAGE=""
AGENT_DEVICE_PASSED=false
ADB_UI_PASSED=false
FAST_UI_PASSED=false
GO_RUNTIME_PASSED=false
TELEMETRY_STATUS="skipped"
TELEMETRY_INTERVAL_SECONDS="${TELEMETRY_INTERVAL_SECONDS:-5}"
ADB_LOGCAT_TIMEOUT_SECONDS="${ADB_LOGCAT_TIMEOUT_SECONDS:-10}"
ADB_UI_FOREGROUND_RETRIES="${ADB_UI_FOREGROUND_RETRIES:-8}"
AGENT_SESSION="${AGENT_DEVICE_SESSION:-android-acceptance}"
PREVIOUS_STAY_AWAKE=""
STAY_AWAKE_CHANGED=false

usage() {
  cat <<'EOF'
Usage: run_android_acceptance.sh --apk <exact.apk> --device <adb-serial> --result-dir <dir> [--telemetry-url <https-url>]

The Go runtime lane receives its existing WT_ANDROID_RUNTIME_CONFIG_FILE or
WT_ANDROID_RUNTIME_CONFIG_JSON input from the environment. The wrapper never
stores that input in evidence or sends it to telemetry.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --apk) APK="${2:-}"; shift 2 ;;
    --device) DEVICE="${2:-}"; shift 2 ;;
    --result-dir) RESULT_DIR="${2:-}"; shift 2 ;;
    --telemetry-url) TELEMETRY_URL="${2:-}"; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) printf 'Unknown argument: %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ -z "$APK" || -z "$DEVICE" || -z "$RESULT_DIR" ]]; then
  printf '%s\n' '--apk, --device, and --result-dir are required.' >&2
  usage >&2
  exit 2
fi
if [[ ! -f "$APK" ]]; then
  printf 'APK not found: %s\n' "$APK" >&2
  exit 2
fi

mkdir -p "$RESULT_DIR"
RESULT_DIR="$(cd "$RESULT_DIR" && pwd)"
APK="$(cd "$(dirname "$APK")" && pwd)/$(basename "$APK")"
RESULT="$RESULT_DIR/acceptance-result.json"
FAST_RESULT="$RESULT_DIR/fast-ui-result.json"
GO_LOG="$RESULT_DIR/go-runtime.log"
INSTRUMENTATION_LOG="$RESULT_DIR/instrumentation.log"
HOST_LOG="$RESULT_DIR/host-logcat.txt"
PHONE_LOG="$RESULT_DIR/phone-app-log.txt"
AGENT_OPEN="$RESULT_DIR/agent-device-open.txt"
AGENT_SNAPSHOT="$RESULT_DIR/agent-device-snapshot.txt"
AGENT_SCREENSHOT="$RESULT_DIR/agent-device.png"
VPN_CONSENT="$RESULT_DIR/vpn-consent.txt"
VPN_CONSENT_AFTER="$RESULT_DIR/vpn-consent-after.txt"
FAST_AFTER="$RESULT_DIR/fast-ui-after.txt"
FAST_SCREENSHOT="$RESULT_DIR/fast-ui-after.png"
HOST_LOG_RAW="$(mktemp)"
PHONE_LOG_RAW="$(mktemp)"
AGENT_OPEN_RAW="$(mktemp)"
AGENT_SNAPSHOT_RAW="$(mktemp)"
ADB_UI_SNAPSHOT_PATH="/data/local/tmp/wt-android-acceptance-ui.xml"
UI_SNAPSHOT_BACKEND="agent-device"
VPN_CONSENT_RAW="$(mktemp)"
VPN_CONSENT_AFTER_RAW="$(mktemp)"
FAST_AFTER_RAW="$(mktemp)"
FAST_RESULT_RAW="$(mktemp)"
GO_LOG_RAW="$(mktemp)"
INSTRUMENTATION_LOG_RAW="$(mktemp)"
EMBEDDED_CONFIG="$(mktemp)"
EMBEDDED_CONFIG_READY=false
VPN_CONSENT_ACCEPTED=false
STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ADB_BASE=("$ADB" -s "$DEVICE")

redact_text() {
  local source="$1" destination="$2"
  python3 - "$source" "$destination" <<'PY'
import re
import sys
from pathlib import Path

source, destination = map(Path, sys.argv[1:])
text = source.read_text(encoding="utf-8", errors="replace")
text = re.sub(r"(?i)(token|cookie|authorization|proxy-authorization|x-[a-z0-9-]*header)\s*[:=]\s*[^\s,;]+", "[REDACTED]", text)
text = re.sub(r"vk1\.[A-Za-z0-9._-]+", "[REDACTED_VK_TOKEN]", text)
text = re.sub(r"eyJ[A-Za-z0-9._-]{20,}", "[REDACTED_JWT]", text)
destination.write_text(text, encoding="utf-8")
destination.chmod(0o600)
PY
}

redact_json() {
  local source="$1" destination="$2"
  python3 - "$source" "$destination" <<'PY'
import json
import re
import sys
from pathlib import Path

source, destination = map(Path, sys.argv[1:])
blocked = re.compile(r"token|cookie|header|authorization|credential|secret|password", re.I)

def clean(value):
    if isinstance(value, dict):
        return {key: clean(item) for key, item in value.items() if not blocked.search(key)}
    if isinstance(value, list):
        return [clean(item) for item in value]
    if isinstance(value, str):
        return re.sub(r"(?i)(token|cookie|authorization|header)\s*[:=]\s*\S+", "[REDACTED]", value)
    return value

try:
    payload = clean(json.loads(source.read_text(encoding="utf-8")))
    destination.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
except (OSError, json.JSONDecodeError):
    destination.write_text("[REDACTED_INVALID_JSON]\n", encoding="utf-8")
destination.chmod(0o600)
PY
}

write_result() {
  WT_ACCEPTANCE_RESULT="$RESULT" WT_ACCEPTANCE_APK="$APK" WT_ACCEPTANCE_DEVICE="$DEVICE" \
    WT_ACCEPTANCE_STAGE="$STAGE" WT_ACCEPTANCE_ERROR="$ERROR_MESSAGE" \
    WT_ACCEPTANCE_STARTED_AT="$STARTED_AT" WT_ACCEPTANCE_AGENT="$AGENT_DEVICE_PASSED" WT_ACCEPTANCE_ADB_UI="$ADB_UI_PASSED" WT_ACCEPTANCE_UI_SNAPSHOT_BACKEND="$UI_SNAPSHOT_BACKEND" \
    WT_ACCEPTANCE_FAST="$FAST_UI_PASSED" WT_ACCEPTANCE_GO="$GO_RUNTIME_PASSED" \
    WT_ACCEPTANCE_VPN_CONSENT="$VPN_CONSENT_ACCEPTED" \
    WT_ACCEPTANCE_TELEMETRY="$TELEMETRY_STATUS" python3 - <<'PY'
import hashlib
import json
import os
import re
from datetime import datetime, timezone
from pathlib import Path

blocked = re.compile(r"token|cookie|header|authorization|credential|secret|password", re.I)

def clean(value: str) -> str:
    value = blocked.sub("[REDACTED]", value)
    value = re.sub(r"vk1\.[A-Za-z0-9._-]+", "[REDACTED_VK_TOKEN]", value)
    value = re.sub(r"eyJ[A-Za-z0-9._-]{20,}", "[REDACTED_JWT]", value)
    return value[:1000]

apk = Path(os.environ["WT_ACCEPTANCE_APK"])
lanes = {
    "agentDevice": os.environ["WT_ACCEPTANCE_AGENT"] == "true",
    "adbLaunchFallback": os.environ["WT_ACCEPTANCE_ADB_UI"] == "true",
    "fastUi": os.environ["WT_ACCEPTANCE_FAST"] == "true",
    "goRuntime": os.environ["WT_ACCEPTANCE_GO"] == "true",
}
payload = {
    "schemaVersion": 1,
    "mode": "android-installed-apk-acceptance",
    "device": clean(os.environ["WT_ACCEPTANCE_DEVICE"]),
    "apk": {"name": apk.name, "sha256": hashlib.sha256(apk.read_bytes()).hexdigest()},
    "stage": clean(os.environ["WT_ACCEPTANCE_STAGE"]),
    "passed": (lanes["agentDevice"] or lanes["adbLaunchFallback"]) and lanes["fastUi"] and lanes["goRuntime"],
    "lanes": lanes,
    "vpnConsentAccepted": os.environ["WT_ACCEPTANCE_VPN_CONSENT"] == "true",
    "uiLaunch": {
        "passed": lanes["agentDevice"] or lanes["adbLaunchFallback"],
        "method": os.environ["WT_ACCEPTANCE_UI_SNAPSHOT_BACKEND"],
        "interactiveControlsProven": lanes["agentDevice"],
        "vpnConsentHandled": os.environ["WT_ACCEPTANCE_VPN_CONSENT"] == "true",
    },
    "error": clean(os.environ["WT_ACCEPTANCE_ERROR"]),
    "startedAt": os.environ["WT_ACCEPTANCE_STARTED_AT"],
    "updatedAt": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
    "telemetry": {"status": os.environ["WT_ACCEPTANCE_TELEMETRY"]},
}
path = Path(os.environ["WT_ACCEPTANCE_RESULT"])
temporary = path.with_suffix(path.suffix + ".tmp")
temporary.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
temporary.chmod(0o600)
os.replace(temporary, path)
PY
}

finish() {
  local rc="$?"
  if [[ -n "$HOST_LOG_PID" ]]; then kill "$HOST_LOG_PID" 2>/dev/null || true; fi
  if [[ -n "$TELEMETRY_PID" ]]; then kill "$TELEMETRY_PID" 2>/dev/null || true; fi
  if ! "${ADB_BASE[@]}" shell run-as "$PKG" cat cache/relay.log >"$PHONE_LOG_RAW" 2>&1 || [[ ! -s "$PHONE_LOG_RAW" ]]; then
    timeout "$ADB_LOGCAT_TIMEOUT_SECONDS" "${ADB_BASE[@]}" logcat -d -v threadtime >"$PHONE_LOG_RAW" 2>&1 || true
  fi
  redact_text "$HOST_LOG_RAW" "$HOST_LOG"
  redact_text "$PHONE_LOG_RAW" "$PHONE_LOG"
  redact_text "$AGENT_OPEN_RAW" "$AGENT_OPEN"
  redact_text "$AGENT_SNAPSHOT_RAW" "$AGENT_SNAPSHOT"
  redact_text "$VPN_CONSENT_RAW" "$VPN_CONSENT"
  redact_text "$VPN_CONSENT_AFTER_RAW" "$VPN_CONSENT_AFTER"
  redact_text "$FAST_AFTER_RAW" "$FAST_AFTER"
  redact_json "$FAST_RESULT_RAW" "$FAST_RESULT"
  redact_text "$GO_LOG_RAW" "$GO_LOG"
  redact_text "$INSTRUMENTATION_LOG_RAW" "$INSTRUMENTATION_LOG"
  ANDROID_SERIAL="$DEVICE" AGENT_DEVICE_SESSION="$AGENT_SESSION" \
    "$AGENT_DEVICE" close "$PKG" --platform android >/dev/null 2>&1 || true
  if [[ $rc -ne 0 && -z "$ERROR_MESSAGE" ]]; then ERROR_MESSAGE="stage_failed"; fi
  write_result
  if [[ -n "$TELEMETRY_URL" ]]; then
    if "$CURL" --fail --silent --show-error --request POST --header 'Content-Type: application/json' --data-binary "@$RESULT" "$TELEMETRY_URL" >/dev/null 2>&1; then
      TELEMETRY_STATUS="uploaded"
    else
      TELEMETRY_STATUS="failed"
    fi
    write_result
  fi
  if [[ "$STAY_AWAKE_CHANGED" == true && "$PREVIOUS_STAY_AWAKE" =~ ^[0-9]+$ ]]; then
    "${ADB_BASE[@]}" shell settings put global stay_on_while_plugged_in "$PREVIOUS_STAY_AWAKE" >/dev/null 2>&1 || true
  fi
  rm -f "$HOST_LOG_RAW" "$PHONE_LOG_RAW" "$AGENT_OPEN_RAW" "$AGENT_SNAPSHOT_RAW" "$VPN_CONSENT_RAW" "$VPN_CONSENT_AFTER_RAW" "$FAST_AFTER_RAW" "$FAST_RESULT_RAW" "$GO_LOG_RAW" "$INSTRUMENTATION_LOG_RAW" "$EMBEDDED_CONFIG"
  exit "$rc"
}

telemetry_loop() {
  while true; do
    TELEMETRY_STATUS="streaming"
    write_result
    "$CURL" --fail --silent --show-error --request POST \
      --header 'Content-Type: application/json' --data-binary "@$RESULT" "$TELEMETRY_URL" >/dev/null 2>&1 || true
    sleep "$TELEMETRY_INTERVAL_SECONDS"
  done
}

extract_vpn_consent_ref() {
  python3 - "$1" "${2:-}" <<'PY'
import re
import sys
from pathlib import Path

text = Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace")
allow_button_only = sys.argv[2] == "allow-button-only"
if not allow_button_only and not re.search(r"(?i)(vpn|virtual private|connection request|создани[ея].{0,20}vpn|подключени[ея].{0,20}vpn)", text):
    raise SystemExit(0)
accepted = {"да", "ок", "ok", "yes", "allow", "разрешить"}
for line in text.splitlines():
    match = re.search(r'(@e\d+)\s+\[(?:button|togglebutton)\]\s+"([^"]+)"', line)
    if not match:
        continue
    label = match.group(2).strip().casefold()
    if label in accepted or label.startswith("allow") or label.startswith("разрешить"):
        print(match.group(1))
        break
PY
}

vpn_consent_prompt_present() {
  python3 - "$1" <<'PY'
import re
import sys
from pathlib import Path

text = Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace")
if re.search(r"(?i)(connection request|разрешени[ея] на настройку vpn|разрешайте это только|разрешить\?)", text):
    raise SystemExit(0)
raise SystemExit(1)
PY
}

accept_vpn_consent_from() {
  local source="$1" consent_ref
  consent_ref="$(extract_vpn_consent_ref "$source" allow-button-only)"
  if [[ -z "$consent_ref" ]]; then
    if vpn_consent_prompt_present "$source"; then
      fail "vpn_consent_control_not_found"
    fi
    return 1
  fi
  ANDROID_SERIAL="$DEVICE" AGENT_DEVICE_SESSION="$AGENT_SESSION" \
    "$AGENT_DEVICE" press "$consent_ref" --platform android --settle >"$VPN_CONSENT_AFTER_RAW" 2>&1 || fail "vpn_consent_press_failed"
  VPN_CONSENT_ACCEPTED=true
  return 0
}

agent_device_accessibility_gap() {
  # Agent-device reports two equivalent fail-closed WebView accessibility
  # diagnostics across versions; both permit only the non-interactive ADB
  # foreground/package proof below.
  grep -Eq 'Android snapshot helper returned (no accessibility nodes|insufficient foreground app content)' "$1"
}

capture_ui_snapshot() {
  if ANDROID_SERIAL="$DEVICE" AGENT_DEVICE_SESSION="$AGENT_SESSION" \
    "$AGENT_DEVICE" snapshot -i --platform android >"$AGENT_SNAPSHOT_RAW" 2>&1; then
    UI_SNAPSHOT_BACKEND="agent-device"
    return 0
  fi
  if ! agent_device_accessibility_gap "$AGENT_SNAPSHOT_RAW"; then
    fail "agent_device_snapshot_failed"
  fi

  # Some physical WebViews expose no accessibility nodes to agent-device even
  # though Android itself still exposes the app package. This is launch proof,
  # not a fabricated accessible-button proof; connect stays in instrumentation.
  local window_state=""
  local foreground_ready=false
  for _ in $(seq 1 "$ADB_UI_FOREGROUND_RETRIES"); do
    window_state="$("${ADB_BASE[@]}" shell dumpsys window 2>/dev/null || true)"
    if grep -Eq "mCurrentFocus=.*${PKG}/" <<<"$window_state"; then
      foreground_ready=true
      break
    fi
    sleep 1
  done
  [[ "$foreground_ready" == true ]] || fail "adb_ui_foreground_not_visible"
  "${ADB_BASE[@]}" shell uiautomator dump "$ADB_UI_SNAPSHOT_PATH" >/dev/null 2>&1 || fail "adb_ui_snapshot_failed"
  "${ADB_BASE[@]}" exec-out cat "$ADB_UI_SNAPSHOT_PATH" >"$AGENT_SNAPSHOT_RAW" 2>&1 || fail "adb_ui_snapshot_read_failed"
  grep -Fq "package=\"$PKG\"" "$AGENT_SNAPSHOT_RAW" || fail "adb_ui_package_not_visible"
  UI_SNAPSHOT_BACKEND="adb-uiautomator"
  ADB_UI_PASSED=true
}

capture_ui_screenshot() {
  local destination="$1"
  if [[ "$UI_SNAPSHOT_BACKEND" == "agent-device" ]]; then
    ANDROID_SERIAL="$DEVICE" AGENT_DEVICE_SESSION="$AGENT_SESSION" \
      "$AGENT_DEVICE" screenshot "$destination" --platform android >/dev/null 2>&1 || fail "agent_device_screenshot_failed"
    return 0
  fi
  "${ADB_BASE[@]}" exec-out screencap -p >"$destination" 2>/dev/null || fail "adb_ui_screenshot_failed"
  [[ -s "$destination" ]] || fail "adb_ui_screenshot_empty"
}

run_agent_fast_ui() {
  local connect_ref
  capture_ui_snapshot
  connect_ref="$(sed -n 's/.*\(@e[0-9][0-9]*\) \[togglebutton\] "\(Подключиться\|Connect\)".*/\1/p' "$AGENT_SNAPSHOT_RAW" | head -n 1)"
  if [[ "$UI_SNAPSHOT_BACKEND" == "adb-uiautomator" ]]; then
    connect_ref="adb-uiautomator-package-visible"
  fi
  if [[ -z "$connect_ref" ]]; then
    # Android may show VPN consent immediately after launch, before the app UI.
    accept_vpn_consent_from "$AGENT_SNAPSHOT_RAW" || true
    capture_ui_snapshot
    connect_ref="$(sed -n 's/.*\(@e[0-9][0-9]*\) \[togglebutton\] "\(Подключиться\|Connect\)".*/\1/p' "$AGENT_SNAPSHOT_RAW" | head -n 1)"
  fi
  [[ -n "$connect_ref" ]] || fail "fast_ui_connect_control_not_found"
  # This lane only verifies the launch screen and accepts system VPN consent.
  # It must not connect: doing so withdraws the node advertisement and races
  # the lifecycle-aware Go runner that follows.
  if [[ "$UI_SNAPSHOT_BACKEND" == "agent-device" ]]; then
    accept_vpn_consent_from "$AGENT_SNAPSHOT_RAW" || true
  fi
  if [[ "$VPN_CONSENT_ACCEPTED" == true ]]; then
    capture_ui_snapshot
    cp "$AGENT_SNAPSHOT_RAW" "$VPN_CONSENT_AFTER_RAW"
    if [[ -n "$(extract_vpn_consent_ref "$VPN_CONSENT_AFTER_RAW")" ]]; then
      fail "vpn_consent_dialog_remaining"
    fi
    cp "$VPN_CONSENT_AFTER_RAW" "$FAST_AFTER_RAW"
  elif [[ "$UI_SNAPSHOT_BACKEND" == "agent-device" ]]; then
    cp "$AGENT_SNAPSHOT_RAW" "$FAST_AFTER_RAW"
    # No dialog means Android already granted VpnService.prepare() for this
    # package. The later lifecycle-aware runner still must connect/disconnect;
    # this flag only prevents a pre-granted device from being misclassified.
    VPN_CONSENT_ACCEPTED=true
  else
    cp "$AGENT_SNAPSHOT_RAW" "$FAST_AFTER_RAW"
  fi
  capture_ui_screenshot "$FAST_SCREENSHOT"
  # Ensure the later instrumentation lane starts with a clean app process.
  "${ADB_BASE[@]}" shell am force-stop "$PKG" >/dev/null 2>&1 || fail "fast_ui_cleanup_failed"
  python3 - "$FAST_RESULT_RAW" "$connect_ref" <<'PY'
import json
import sys
from pathlib import Path

Path(sys.argv[1]).write_text(json.dumps({
    "schemaVersion": 1,
    "mode": "android-agent-device-fast-ui",
    "passed": True,
    "action": "inspect-and-consent",
    "ref": sys.argv[2],
}, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
PY
}
trap finish EXIT

fail() {
  ERROR_MESSAGE="$1"
  exit 1
}

if [[ -n "$TELEMETRY_URL" ]]; then
  telemetry_loop &
  TELEMETRY_PID=$!
fi

STAGE="device_check"
"$ADB" start-server >/dev/null 2>&1 || fail "adb_start_failed"
"${ADB_BASE[@]}" get-state >/dev/null 2>&1 || fail "device_not_online"
PREVIOUS_STAY_AWAKE="$("${ADB_BASE[@]}" shell settings get global stay_on_while_plugged_in 2>/dev/null | tr -d '\r' || true)"
"${ADB_BASE[@]}" shell svc power stayon true >/dev/null 2>&1 || fail "stay_awake_setup_failed"
STAY_AWAKE_CHANGED=true
## Do not toggle KEYCODE_POWER here: on an already awake phone it would lock it.
## These inputs intentionally never enter a PIN/pattern/password; they only
## wake the display and dismiss a swipe-only lockscreen before the UI lane.
"${ADB_BASE[@]}" shell input keyevent 224 >/dev/null 2>&1 || true
"${ADB_BASE[@]}" shell input keyevent 3 >/dev/null 2>&1 || true
"${ADB_BASE[@]}" shell input swipe 360 1400 360 400 300 >/dev/null 2>&1 || true
"${ADB_BASE[@]}" shell input swipe 360 1400 360 400 300 >/dev/null 2>&1 || true

STAGE="clean_install"
"${ADB_BASE[@]}" uninstall "$PKG" >/dev/null 2>&1 || true
"${ADB_BASE[@]}" install -r "$APK" >/dev/null || fail "apk_install_failed"

STAGE="host_logcat"
"${ADB_BASE[@]}" logcat -v threadtime >"$HOST_LOG_RAW" 2>&1 &
HOST_LOG_PID=$!

STAGE="agent_device"
ANDROID_SERIAL="$DEVICE" AGENT_DEVICE_SESSION="$AGENT_SESSION" \
  "$AGENT_DEVICE" close --session default --platform android >/dev/null 2>&1 || true
ANDROID_SERIAL="$DEVICE" AGENT_DEVICE_SESSION="$AGENT_SESSION" \
  "$AGENT_DEVICE" close --session "$AGENT_SESSION" --platform android >/dev/null 2>&1 || true
ANDROID_SERIAL="$DEVICE" AGENT_DEVICE_SESSION="$AGENT_SESSION" \
  "$AGENT_DEVICE" open "$PKG" --platform android --relaunch --no-record --force >"$AGENT_OPEN_RAW" 2>&1 || {
    stale_session="$(sed -n 's/.*session "\([^"]*\)".*/\1/p' "$AGENT_OPEN_RAW" | head -n 1)"
    if [[ -n "$stale_session" ]]; then
      ANDROID_SERIAL="$DEVICE" AGENT_DEVICE_SESSION="$AGENT_SESSION" \
        "$AGENT_DEVICE" close --session "$stale_session" --platform android >/dev/null 2>&1 || true
      ANDROID_SERIAL="$DEVICE" AGENT_DEVICE_SESSION="$AGENT_SESSION" \
        "$AGENT_DEVICE" open "$PKG" --platform android --relaunch --no-record --force >>"$AGENT_OPEN_RAW" 2>&1 || fail "agent_device_open_failed"
    else
      fail "agent_device_open_failed"
    fi
  }
# Keep the WebView host foreground: the legacy Fragment activity is retained
# for rollback only and does not exercise the Capacitor transport bridge.
"${ADB_BASE[@]}" shell am start -W -n "$PKG/$ACTIVITY" >/dev/null 2>&1 || fail "app_launch_failed"
capture_ui_snapshot
capture_ui_screenshot "$AGENT_SCREENSHOT"
if [[ "$UI_SNAPSHOT_BACKEND" == "agent-device" ]]; then
  AGENT_DEVICE_PASSED=true
fi

STAGE="fast_ui"
run_agent_fast_ui
FAST_UI_PASSED=true

STAGE="go_runtime"
if [[ -z "${WT_ANDROID_RUNTIME_CONFIG_FILE:-}" && -z "${WT_ANDROID_RUNTIME_CONFIG_JSON:-}" ]]; then
  python3 - "$APK" "$EMBEDDED_CONFIG" <<'PY' || fail "embedded_runtime_config_missing"
import sys
import zipfile
from pathlib import Path

apk, destination = map(Path, sys.argv[1:])
with zipfile.ZipFile(apk) as archive:
    data = archive.read("assets/wt-runtime-config.json")
destination.write_bytes(data)
destination.chmod(0o600)
PY
  EMBEDDED_CONFIG_READY=true
fi
DEVICE="$DEVICE" ANDROID_SERIAL="$DEVICE" \
  WT_ANDROID_RUNTIME_CONFIG_FILE="${WT_ANDROID_RUNTIME_CONFIG_FILE:-$EMBEDDED_CONFIG}" \
  LOG="$GO_LOG_RAW" INSTRUMENTATION_RESULT_LOG="$INSTRUMENTATION_LOG_RAW" \
  "$SLOW_RUNNER" > /dev/null 2>&1 || fail "go_runtime_failed"
GO_RUNTIME_PASSED=true
STAGE="complete"
