#!/usr/bin/env bash
# Build a self-contained APK, clean-install it, and run Android acceptance lanes.
set -euo pipefail
umask 077

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../.." && pwd)"
BUILD_SCRIPT="${BUILD_SCRIPT:-$ROOT/secrets/android-build.sh}"
ACCEPTANCE_RUNNER="${ACCEPTANCE_RUNNER:-$ROOT/apps/android/whitelist-bypass-client/tools/test/run_android_acceptance.sh}"
FAST_RUNNER="${FAST_RUNNER:-$ROOT/apps/android/whitelist-bypass-client/tools/test/run_android_auto_debug.sh}"
APK="${APK:-$ROOT/artifacts/android/WhiteTransport-dev-release.apk}"
TEST_APK="${WT_ANDROID_TEST_APK:-$ROOT/apps/android/whitelist-bypass-client/app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk}"
DEVICE="${DEVICE:-${ANDROID_SERIAL:-}}"
RESULT_DIR="${RESULT_DIR:-$ROOT/output/android-acceptance/$(date -u +%Y%m%d-%H%M%S)}"
TELEMETRY_URL="${WT_ANDROID_TELEMETRY_URL:-}"
ADB="${ADB:-adb}"
GIT="${GIT:-git}"

usage() {
  cat <<'EOF'
Usage: android_acceptance.sh [--device <adb-serial>] [--result-dir <dir>]
       [--telemetry-url <https-url>] [--no-build]

The default command builds the self-contained APK from the current checkout,
uninstalls the old package, installs the exact new APK, runs agent-device UI
smoke, then the bounded Go runtime/VPN lane, and writes redacted evidence.
A passing result requires explicit WT_NODE_ID and WT_PROBE_EXPECTED values.
This command does not claim device-wide system-TUN or split-routing proof.
EOF
}

BUILD=true
while [[ $# -gt 0 ]]; do
  case "$1" in
    --device) DEVICE="${2:-}"; shift 2 ;;
    --result-dir) RESULT_DIR="${2:-}"; shift 2 ;;
    --telemetry-url) TELEMETRY_URL="${2:-}"; shift 2 ;;
    --no-build) BUILD=false; shift ;;
    --help|-h) usage; exit 0 ;;
    *) printf 'Unknown argument: %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ ! -f "$BUILD_SCRIPT" || ! -f "$ACCEPTANCE_RUNNER" || ! -f "$FAST_RUNNER" ]]; then
  printf 'Build, acceptance, or fast-runner script is missing.\n' >&2
  exit 2
fi

mkdir -p "$RESULT_DIR"
RESULT_DIR="$(cd "$RESULT_DIR" && pwd)"
BUILD_RAW="$(mktemp)"
BUILD_LOG="$RESULT_DIR/build.log"
FAST_RAW="$(mktemp)"
FAST_LOG="$RESULT_DIR/fast-runner.log"
FAST_RESULT="$RESULT_DIR/fast-runner-result.json"
RESULT="$RESULT_DIR/acceptance-result.json"
INSTRUMENTATION_LOG="$RESULT_DIR/instrumentation.log"
trap 'rm -f "$BUILD_RAW" "$FAST_RAW"' EXIT

GIT_SHA="$($GIT -C "$ROOT" rev-parse HEAD 2>/dev/null)" || {
  printf 'Cannot resolve source git SHA.\n' >&2
  exit 2
}
GIT_DIRTY=false
if [[ -n "$($GIT -C "$ROOT" status --porcelain --untracked-files=normal 2>/dev/null)" ]]; then
  GIT_DIRTY=true
fi

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

write_failure() {
  WT_RESULT="$RESULT_DIR/acceptance-result.json" WT_STAGE="$1" python3 - <<'PY'
import json
import os
from datetime import datetime, timezone
from pathlib import Path

path = Path(os.environ["WT_RESULT"])
path.write_text(json.dumps({
    "schemaVersion": 1,
    "mode": "android-installed-apk-acceptance",
    "passed": False,
    "stage": os.environ["WT_STAGE"],
    "error": "build_failed",
    "startedAt": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
    "telemetry": {"status": "skipped"},
}, indent=2) + "\n", encoding="utf-8")
path.chmod(0o600)
PY
}

base_validation_error() {
  python3 - "$RESULT" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
try:
    payload = json.loads(path.read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError):
    print("acceptance_result_missing_or_invalid")
    raise SystemExit(0)

if payload.get("passed") is not True:
    print(payload.get("error") or "acceptance_runner_failed")
elif payload.get("vpnConsentAccepted") is not True:
    print("vpn_consent_required")
elif any(payload.get("lanes", {}).get(name) is not True for name in ("agentDevice", "fastUi", "goRuntime")):
    print("acceptance_lane_incomplete")
PY
}

finalize_result() {
  WT_RESULT="$RESULT" WT_FAST_RESULT="$FAST_RESULT" WT_INSTRUMENTATION_LOG="$INSTRUMENTATION_LOG" \
    WT_APP_APK="$APK" WT_TEST_APK="$TEST_APK" WT_GIT_SHA="$GIT_SHA" WT_GIT_DIRTY="$GIT_DIRTY" \
    WT_BASE_ERROR="$1" WT_FAST_RC="$2" python3 - <<'PY'
import hashlib
import json
import os
import re
import zipfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


def sha256_bytes(data: bytes) -> str:
    """Return the stable SHA-256 identity for one evidence input."""

    return hashlib.sha256(data).hexdigest()


def artifact(path: Path) -> dict[str, str]:
    """Return fail-closed file identity, or an empty hash when absent."""

    if not path.is_file():
        return {"name": path.name, "sha256": ""}
    return {"name": path.name, "sha256": sha256_bytes(path.read_bytes())}


def runtime_config_identity(app_apk: Path) -> dict[str, str]:
    """Hash the exact file, inline JSON, or APK-embedded runtime config."""

    config_file = os.environ.get("WT_ANDROID_RUNTIME_CONFIG_FILE", "")
    if config_file:
        path = Path(config_file)
        return {"source": "file", "sha256": sha256_bytes(path.read_bytes()) if path.is_file() else ""}
    config_json = os.environ.get("WT_ANDROID_RUNTIME_CONFIG_JSON", "")
    if config_json:
        return {"source": "inline-json", "sha256": sha256_bytes(config_json.encode("utf-8"))}
    try:
        with zipfile.ZipFile(app_apk) as archive:
            data = archive.read("assets/wt-runtime-config.json")
    except (OSError, KeyError, zipfile.BadZipFile):
        return {"source": "apk-embedded", "sha256": ""}
    return {"source": "apk-embedded", "sha256": sha256_bytes(data)}


def read_json(path: Path) -> dict[str, Any]:
    """Read a JSON object without allowing malformed evidence to pass."""

    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}
    return value if isinstance(value, dict) else {}


def has_ordered_stages(stages: list[str], expected: tuple[str, ...]) -> bool:
    """Return true when each required instrumentation stage occurs in order."""

    position = 0
    for stage in stages:
        if stage == expected[position]:
            position += 1
            if position == len(expected):
                return True
    return False


result_path = Path(os.environ["WT_RESULT"])
result = read_json(result_path)
fast_result = read_json(Path(os.environ["WT_FAST_RESULT"]))
app_apk = Path(os.environ["WT_APP_APK"])
test_apk = Path(os.environ["WT_TEST_APK"])
config = runtime_config_identity(app_apk)
base_error = os.environ.get("WT_BASE_ERROR", "")
fast_invoked = os.environ.get("WT_FAST_RC") != "125"
fast_ok = (
    os.environ.get("WT_FAST_RC") == "0"
    and fast_result.get("passed") is True
    and fast_result.get("stage") == "complete"
    and fast_result.get("uiStatus") == "Connected"
    and not fast_result.get("error")
)

try:
    instrumentation = Path(os.environ["WT_INSTRUMENTATION_LOG"]).read_text(encoding="utf-8", errors="replace")
except OSError:
    instrumentation = ""
stages = re.findall(r"^INSTRUMENTATION_STATUS:\s*wt_stage=(\S+)\s*$", instrumentation, re.MULTILINE)
runtime_sequence_ok = has_ordered_stages(stages, ("socks.payload", "runtime.disconnect", "complete"))
junit_ok = re.search(r"^OK \([1-9][0-9]* tests?\)$", instrumentation, re.MULTILINE) is not None
result_code_ok = re.search(r"^INSTRUMENTATION_CODE:\s*(?:-1|0)\s*$", instrumentation, re.MULTILINE) is not None
marker = os.environ.get("WT_PROBE_EXPECTED", "")
payload_ok = runtime_sequence_ok and junit_ok and result_code_ok and bool(marker)
node_id = os.environ.get("WT_NODE_ID", "").strip()

app_identity = artifact(app_apk)
test_identity = artifact(test_apk)
errors: list[str] = []
if base_error:
    errors.append(base_error)
if not app_identity["sha256"]:
    errors.append("app_apk_identity_missing")
if not test_identity["sha256"]:
    errors.append("test_apk_identity_missing")
if not config["sha256"]:
    errors.append("runtime_config_identity_missing")
if not node_id:
    errors.append("selected_node_missing")
if not base_error and not fast_ok:
    errors.append("fast_runner_lifecycle_incomplete")
if not base_error and not payload_ok:
    errors.append("payload_or_runtime_cleanup_evidence_incomplete")

lanes = result.get("lanes") if isinstance(result.get("lanes"), dict) else {}
lanes["fastRunner"] = fast_ok
result.update({
    "schemaVersion": 2,
    "passed": not errors,
    "stage": "complete" if not errors else "validation",
    "error": errors[0] if errors else "",
    "lanes": lanes,
    "provenance": {
        "git": {
            "sha": os.environ["WT_GIT_SHA"],
            "dirty": os.environ["WT_GIT_DIRTY"] == "true",
        },
        "artifacts": {
            "appApk": app_identity,
            "testApk": test_identity,
            "runtimeConfig": config,
        },
    },
    "fastRunner": {
        "invoked": fast_invoked,
        "passed": fast_ok,
        # The default fast runner writes `passed=true` only after its bounded disconnect succeeds.
        "lifecycle": ["Connected", "Disconnected"] if fast_ok else [],
    },
    "selectedNodeId": node_id,
    "payload": {
        "kind": "go-runtime-socks-http",
        "passed": payload_ok,
        "markerSha256": sha256_bytes(marker.encode("utf-8")) if marker else "",
    },
    "cleanup": {
        "fastRunnerDisconnected": fast_ok,
        "runtimeDisconnected": runtime_sequence_ok and junit_ok and result_code_ok,
    },
    # This runner proves app/runtime SOCKS payload only; it does not exercise device-wide split routing.
    "systemTunSplit": {
        "checked": False,
        "passed": False,
        "reason": "not exercised by this acceptance runner",
    },
    "updatedAt": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
})
temporary = result_path.with_suffix(result_path.suffix + ".tmp")
temporary.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
temporary.chmod(0o600)
os.replace(temporary, result_path)
raise SystemExit(0 if not errors else 1)
PY
}

if [[ -z "$DEVICE" ]]; then
  mapfile -t devices < <("$ADB" devices | awk 'NR > 1 && $2 == "device" {print $1}')
  if [[ "${#devices[@]}" -ne 1 ]]; then
    printf 'Expected exactly one online adb device; found %s. Use --device.\n' "${#devices[@]}" >&2
    exit 2
  fi
  DEVICE="${devices[0]}"
fi

if [[ "$BUILD" == true ]]; then
  printf '[android-acceptance] building APK\n'
  set +e
  "$BUILD_SCRIPT" >"$BUILD_RAW" 2>&1
  BUILD_RC=$?
  set -e
  redact_text "$BUILD_RAW" "$BUILD_LOG"
  if [[ $BUILD_RC -ne 0 ]]; then
    write_failure build
    printf 'APK build failed; see %s\n' "$BUILD_LOG" >&2
    exit "$BUILD_RC"
  fi
fi

if [[ ! -f "$APK" ]]; then
  printf 'APK not found after build: %s\n' "$APK" >&2
  write_failure apk
  exit 2
fi

runner_args=(--apk "$APK" --device "$DEVICE" --result-dir "$RESULT_DIR")
if [[ -n "$TELEMETRY_URL" ]]; then
  runner_args+=(--telemetry-url "$TELEMETRY_URL")
fi

set +e
"$ACCEPTANCE_RUNNER" "${runner_args[@]}"
ACCEPTANCE_RC=$?
set -e

BASE_ERROR="$(base_validation_error)"
if [[ $ACCEPTANCE_RC -ne 0 && -z "$BASE_ERROR" ]]; then
  BASE_ERROR="acceptance_runner_failed"
fi

FAST_RC=125
if [[ -z "$BASE_ERROR" ]]; then
  set +e
  DEVICE="$DEVICE" ANDROID_SERIAL="$DEVICE" APK="$APK" SKIP_INSTALL=1 RESULT="$FAST_RESULT" \
    "$FAST_RUNNER" >"$FAST_RAW" 2>&1
  FAST_RC=$?
  set -e
  redact_text "$FAST_RAW" "$FAST_LOG"
fi

set +e
finalize_result "$BASE_ERROR" "$FAST_RC"
FINAL_RC=$?
set -e
exit "$FINAL_RC"
