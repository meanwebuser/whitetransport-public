#!/usr/bin/env bash
# Linux-safe contract for the single Android acceptance wrapper.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../.." && pwd)"
WRAPPER="$ROOT/apps/android/whitelist-bypass-client/tools/test/run_android_acceptance.sh"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/wt-android-acceptance.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT

APK="$TEST_ROOT/WhiteTransport-test.apk"
RESULT_DIR="$TEST_ROOT/evidence"
FAKE_BIN="$TEST_ROOT/bin"
mkdir -p "$FAKE_BIN"
printf 'exact test apk\n' >"$APK"

cat >"$FAKE_BIN/adb" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s|%s\n' "$*" "${SKIP_INSTALL:-}" >>"$FAKE_ADB_LOG"
args=("$@")
if [[ "${args[0]:-}" == "-s" ]]; then args=("${args[@]:2}"); fi
case "${args[0]:-}" in
  uninstall|install|start-server) exit 0 ;;
  exec-out)
    if [[ "${args[1]:-}" == "cat" ]]; then
      printf '<hierarchy><node package="bypass.whitelist" bounds="[0,0][720,1600]"/></hierarchy>\n'
    elif [[ "${args[1]:-}" == "screencap" ]]; then
      printf 'fake screenshot\n'
    fi
    exit 0
    ;;
  logcat)
    if [[ " ${args[*]} " == *" -d "* ]]; then
      if [[ "${FAKE_HANG_LOGCAT:-false}" == "true" ]]; then
        while true; do sleep 1; done
      fi
      printf 'app token=must-not-persist cookie=must-not-persist Authorization: must-not-persist\n'
      exit 0
    fi
    printf 'host token=must-not-persist cookie=must-not-persist X-Header: must-not-persist\n'
    while true; do sleep 1; done
    ;;
  shell)
    if [[ " ${args[*]} " == *" dumpsys window "* ]]; then
      window_count=0
      if [[ -f "$FAKE_WINDOW_COUNT" ]]; then
        window_count="$(cat "$FAKE_WINDOW_COUNT")"
      fi
      window_count=$((window_count + 1))
      printf '%s\n' "$window_count" >"$FAKE_WINDOW_COUNT"
      if [[ "$window_count" -le "${FAKE_WINDOW_TRANSIENT_FAILURES:-0}" ]]; then
        exit 0
      fi
      printf 'mCurrentFocus=Window{test u0 bypass.whitelist/bypass.whitelist.MainActivity}\n'
    fi
    exit 0
    ;;
esac
EOF
chmod 0755 "$FAKE_BIN/adb"

cat >"$FAKE_BIN/agent-device" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$FAKE_AGENT_DEVICE_LOG"
case "${1:-}" in
  open)
    count="$(grep -c '^open' "$FAKE_AGENT_DEVICE_LOG" 2>/dev/null || true)"
    if [[ "${FAKE_STALE_SESSION:-0}" == "1" && "$count" -eq 1 ]]; then
      printf 'Error (DEVICE_IN_USE): Device is already in use by session "cwd:stale:default"\n'
      exit 1
    fi
    ;;
  press)
    if [[ "${2:-}" == "@e7" ]]; then
      : >"$FAKE_CONSENT_SHOWN"
      printf 'VPN connection request\n@e8 [button] "ОК"\n'
    elif [[ "${2:-}" == "@e8" ]]; then
      : >"$FAKE_CONSENT_ACCEPTED"
      printf 'Tapped %s\n' "${2:-unknown}"
    else
      printf 'Tapped %s\n' "${2:-unknown}"
    fi
    ;;
  snapshot)
    if [[ "${FAKE_AGENT_SNAPSHOT_FAIL:-0}" == "1" ]]; then
      printf 'Error (COMMAND_FAILED): Android snapshot helper returned insufficient foreground app content\n' >&2
      exit 1
    fi
    if [[ "${FAKE_INITIAL_CONSENT:-0}" == "1" && ! -f "$FAKE_CONSENT_ACCEPTED" ]]; then
      printf 'VPN connection request\n@e8 [button] "ОК"\n'
    elif [[ -f "$FAKE_CONSENT_SHOWN" && ! -f "$FAKE_CONSENT_ACCEPTED" ]]; then
      printf 'VPN connection request\n@e8 [button] "ОК"\n'
    else
      printf '@e7 [togglebutton] "Подключиться"\n'
    fi
    printf 'UI token=must-not-persist cookie=must-not-persist\n'
    ;;
  screenshot) printf 'fake screenshot\n' >"${2:?screenshot path missing}" ;;
esac
EOF
chmod 0755 "$FAKE_BIN/agent-device"

cat >"$FAKE_BIN/fast-runner" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'device=%s android_serial=%s skip_install=%s result=%s\n' "${DEVICE:-}" "${ANDROID_SERIAL:-}" "${SKIP_INSTALL:-}" "${RESULT:-}" >>"$FAKE_FAST_LOG"
printf '{"passed":true,"token":"must-not-persist"}\n' >"$RESULT"
EOF
chmod 0755 "$FAKE_BIN/fast-runner"

cat >"$FAKE_BIN/slow-runner" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'device=%s android_serial=%s app_apk=%s config=%s log=%s instrumentation=%s\n' "${DEVICE:-}" "${ANDROID_SERIAL:-}" "${WT_ANDROID_APP_APK:-}" "${WT_ANDROID_RUNTIME_CONFIG_FILE:-}" "${LOG:-}" "${INSTRUMENTATION_RESULT_LOG:-}" >>"$FAKE_SLOW_LOG"
printf 'slow-runner\n' >>"$FAKE_AGENT_DEVICE_LOG"
printf 'runtime cookie=must-not-persist\n' >"$LOG"
printf 'instrumentation Authorization: must-not-persist\n' >"$INSTRUMENTATION_RESULT_LOG"
EOF
chmod 0755 "$FAKE_BIN/slow-runner"

cat >"$FAKE_BIN/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$FAKE_CURL_LOG"
file="${*: -2:1}"
cp "${file#@}" "$FAKE_UPLOADED_TELEMETRY"
EOF
chmod 0755 "$FAKE_BIN/curl"

export FAKE_ADB_LOG="$TEST_ROOT/adb.log" FAKE_AGENT_DEVICE_LOG="$TEST_ROOT/agent-device.log"
export FAKE_WINDOW_COUNT="$TEST_ROOT/window-count" FAKE_WINDOW_TRANSIENT_FAILURES=2
export FAKE_FAST_LOG="$TEST_ROOT/fast.log" FAKE_SLOW_LOG="$TEST_ROOT/slow.log"
export FAKE_CURL_LOG="$TEST_ROOT/curl.log" FAKE_UPLOADED_TELEMETRY="$TEST_ROOT/uploaded.json"
export FAKE_HANG_LOGCAT=true
export FAKE_STALE_SESSION=1
export FAKE_AGENT_SNAPSHOT_FAIL=1
export FAKE_CONSENT_SHOWN="$TEST_ROOT/consent-shown" FAKE_CONSENT_ACCEPTED="$TEST_ROOT/consent-accepted"
export FAKE_INITIAL_CONSENT=1
RUNTIME_CONFIG="$TEST_ROOT/runtime-config.json"
printf '{"token_store":{"tokens":[],"bindings":[]},"carrier_configs":[]}\n' >"$RUNTIME_CONFIG"

ADB="$FAKE_BIN/adb" AGENT_DEVICE="$FAKE_BIN/agent-device" CURL="$FAKE_BIN/curl" \
FAST_RUNNER="$FAKE_BIN/fast-runner" SLOW_RUNNER="$FAKE_BIN/slow-runner" \
WT_ANDROID_RUNTIME_CONFIG_FILE="$RUNTIME_CONFIG" \
TELEMETRY_INTERVAL_SECONDS=0.1 ADB_LOGCAT_TIMEOUT_SECONDS=1 \
timeout 8 bash "$WRAPPER" --apk "$APK" --device emulator-5554 --result-dir "$RESULT_DIR" --telemetry-url https://telemetry.invalid/android

# The acceptance runner must keep the Capacitor WebView foreground. Starting
# the retired legacy activity makes the later Go/UI evidence belong to a
# different product surface.
grep -Fq 'shell am start -W -n bypass.whitelist/.CapacitorMainActivity|' "$FAKE_ADB_LOG"

python3 - "$RESULT_DIR" "$APK" <<'PY'
import hashlib
import json
from pathlib import Path
import sys

result_dir = Path(sys.argv[1])
apk = Path(sys.argv[2])
result = json.loads((result_dir / "acceptance-result.json").read_text())
assert result["passed"] is True, result
assert result["device"] == "emulator-5554", result
assert result["apk"]["sha256"] == hashlib.sha256(apk.read_bytes()).hexdigest(), result
assert result["lanes"] == {"agentDevice": False, "adbLaunchFallback": True, "fastUi": True, "goRuntime": True}, result
assert result["vpnConsentAccepted"] is False, result
assert result["uiLaunch"] == {
    "passed": True,
    "method": "adb-uiautomator",
    "interactiveControlsProven": False,
    "vpnConsentHandled": False,
}, result
assert result["telemetry"]["status"] == "uploaded", result
for name in ("host-logcat.txt", "phone-app-log.txt", "agent-device-snapshot.txt", "agent-device.png", "vpn-consent.txt", "vpn-consent-after.txt", "fast-ui-result.json", "go-runtime.log", "instrumentation.log"):
    assert (result_dir / name).is_file(), name
for path in result_dir.iterdir():
    if path.is_file():
        text = path.read_text(errors="ignore").lower()
        assert "must-not-persist" not in text, (path, text)
        assert "token" not in text, (path, text)
        assert "cookie" not in text, (path, text)
        assert "authorization" not in text, (path, text)
        assert "x-header" not in text, (path, text)
PY

grep -Fq 'uninstall bypass.whitelist' "$FAKE_ADB_LOG"
grep -Fq 'install -r ' "$FAKE_ADB_LOG"
if grep -Fq 'shell am start -W -n bypass.whitelist/.MainActivity' "$FAKE_ADB_LOG"; then
  echo "legacy MainActivity must not be used for Capacitor acceptance" >&2
  exit 1
fi
grep -Fq 'svc power stayon true' "$FAKE_ADB_LOG"
grep -Fq 'input keyevent 224' "$FAKE_ADB_LOG"
grep -Fq 'input keyevent 3' "$FAKE_ADB_LOG"
test "$(grep -Fc 'input swipe 360 1400 360 400 300' "$FAKE_ADB_LOG")" -ge 2
grep -Fq 'shell uiautomator dump /data/local/tmp/wt-android-acceptance-ui.xml' "$FAKE_ADB_LOG"
grep -Fq 'exec-out cat /data/local/tmp/wt-android-acceptance-ui.xml' "$FAKE_ADB_LOG"
grep -Fq 'close --session default --platform android' "$FAKE_AGENT_DEVICE_LOG"
grep -Fq 'close --session android-acceptance --platform android' "$FAKE_AGENT_DEVICE_LOG"
grep -Fq 'close --session cwd:stale:default --platform android' "$FAKE_AGENT_DEVICE_LOG"
grep -Fq 'open bypass.whitelist --platform android --relaunch --no-record --force' "$FAKE_AGENT_DEVICE_LOG"
grep -Fq 'snapshot -i --platform android' "$FAKE_AGENT_DEVICE_LOG"
if grep -Fq 'press adb-uiautomator-package-visible' "$FAKE_AGENT_DEVICE_LOG"; then
  echo 'ADB fallback must not press an inaccessible UI control' >&2
  exit 1
fi
grep -Fq "device=emulator-5554 android_serial=emulator-5554 app_apk= config=$RUNTIME_CONFIG" "$FAKE_SLOW_LOG"
grep -Fq 'acceptance-result.json' "$FAKE_CURL_LOG"
test "$(wc -l <"$FAKE_CURL_LOG")" -ge 2
echo "android acceptance wrapper contract: OK"
