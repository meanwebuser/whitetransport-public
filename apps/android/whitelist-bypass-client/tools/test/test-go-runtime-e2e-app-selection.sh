#!/usr/bin/env bash
# Contract for running instrumentation against the exact acceptance APK.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../.." && pwd)"
SCRIPT="$ROOT/apps/android/whitelist-bypass-client/tools/test/run_go_runtime_e2e.sh"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/wt-go-e2e-app.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT
BIN="$TEST_ROOT/bin"
mkdir -p "$BIN"

cat >"$BIN/adb" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$FAKE_ADB_LOG"
args=($(printf '%s' "$*"))
if [[ "${1:-}" == "devices" ]]; then
  printf 'List of devices attached\nemulator-5554\tdevice\n'
elif [[ " $* " == *"am instrument"* ]]; then
  printf 'OK (1 test)\nINSTRUMENTATION_CODE: 0\n'
fi
EOF
chmod 0755 "$BIN/adb"

cat >"$BIN/gradle" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
mkdir -p app/build/outputs/apk/debug app/build/outputs/apk/androidTest/debug
printf 'debug\n' >app/build/outputs/apk/debug/app-debug.apk
printf 'test\n' >app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk
EOF
chmod 0755 "$BIN/gradle"

APK="$TEST_ROOT/acceptance.apk"
CONFIG="$TEST_ROOT/config.json"
printf 'release apk\n' >"$APK"
printf '{"token_store":{},"carrier_configs":[]}\n' >"$CONFIG"
export FAKE_ADB_LOG="$TEST_ROOT/adb.log"

ADB="$BIN/adb" GRADLE="$BIN/gradle" DEVICE=emulator-5554 \
  WT_ANDROID_APP_APK="$APK" WT_ANDROID_RUNTIME_CONFIG_FILE="$CONFIG" \
  WT_ANDROID_TEST_TIMEOUT_MS=1000 WT_ANDROID_INSTRUMENTATION_TIMEOUT_SECONDS=10 \
  LOG="$TEST_ROOT/e2e.log" INSTRUMENTATION_RESULT_LOG="$TEST_ROOT/result.log" \
  bash "$SCRIPT"

grep -Fq "install -r $APK" "$FAKE_ADB_LOG"
grep -Fq 'install -r app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk' "$FAKE_ADB_LOG"
echo "go runtime e2e app selection contract: OK"
