#!/usr/bin/env bash
set -euo pipefail

# C-ABI engine smoke proves only the native packet-engine boundary. It is not a
# PacketTunnel install, Network Extension, or installed-client proof lane.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENGINE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

HOST_ARCH_RAW="${WT_C_ABI_HOST_ARCH:-$(uname -m)}"
case "$HOST_ARCH_RAW" in
  arm64|aarch64) HOST_ARCH="arm64" ;;
  x86_64|amd64) HOST_ARCH="amd64" ;;
  *) echo "unsupported host architecture: $HOST_ARCH_RAW" >&2; exit 64 ;;
esac

ARTIFACT_DIR="${WT_C_ABI_ARTIFACT_DIR:-}"
ARTIFACT_DIR_AUTO_CREATED=0
if [[ -z "$ARTIFACT_DIR" ]]; then
  ARTIFACT_DIR="$(mktemp -d "${TMPDIR:-/tmp}/wt-c-abi-engine-smoke.XXXXXX")"
  ARTIFACT_DIR_AUTO_CREATED=1
else
  mkdir -p "$ARTIFACT_DIR"
fi
ARTIFACT_DIR="$(cd "$ARTIFACT_DIR" && pwd)"
BUILD_DIR="$ARTIFACT_DIR/build"
mkdir -p "$BUILD_DIR"

FIXTURE_PID=""
RUNNER_PID=""
FIXTURE_EXIT="not-started"
RUNNER_EXIT="not-started"
EXIT_STATUS=1

cleanup() {
  local trap_status=$?
  set +e
  if [[ -n "$RUNNER_PID" && "$RUNNER_EXIT" == "not-started" ]]; then
    local runner_wait_status=0
    wait "$RUNNER_PID"
    runner_wait_status=$?
    RUNNER_EXIT="$runner_wait_status"
  fi
  if [[ -n "$FIXTURE_PID" ]]; then
    kill "$FIXTURE_PID" 2>/dev/null || true
    wait "$FIXTURE_PID"
    FIXTURE_EXIT=$?
  fi
  if [[ "$RUNNER_EXIT" == "not-started" ]]; then RUNNER_EXIT="$trap_status"; fi
  CLIENT_PID_LINES="$(grep -o 'child_pid=[0-9]* client_exit=[0-9]*' "$ARTIFACT_DIR/runner.log" 2>/dev/null || true)"
  {
    echo "proof_boundary=C-ABI-engine-smoke"
    echo "fixture_pid=$FIXTURE_PID"
    echo "runner_pid=$RUNNER_PID"
    echo "fixture_exit=$FIXTURE_EXIT"
    echo "runner_exit=$RUNNER_EXIT"
    echo "script_exit=$trap_status"
    if [[ -n "$CLIENT_PID_LINES" ]]; then
      while IFS= read -r line; do echo "client_$line"; done <<< "$CLIENT_PID_LINES"
    fi
  } > "$ARTIFACT_DIR/manifest"
  if [[ "$trap_status" -eq 0 && "$RUNNER_EXIT" -eq 0 ]]; then
    if [[ "$ARTIFACT_DIR_AUTO_CREATED" -eq 1 ]]; then
      printf 'artifact_dir=%s status=pass retained=false\n' "$ARTIFACT_DIR"
      rm -rf "$ARTIFACT_DIR"
    else
      printf 'artifact_dir=%s status=pass retained=true\n' "$ARTIFACT_DIR"
    fi
  else
    printf 'artifact_dir=%s status=fail retained=true\n' "$ARTIFACT_DIR"
  fi
  exit "$trap_status"
}
trap cleanup EXIT

GO_BINARY="${GO_BINARY:-/usr/local/go/bin/go}"
GO_BINARY="$GO_BINARY" "$ENGINE_DIR/build-c-archive.sh" "$BUILD_DIR/engine"
(
  cd "$ENGINE_DIR"
  CGO_ENABLED=1 GOOS=darwin GOARCH="$HOST_ARCH" "$GO_BINARY" build -trimpath -o "$BUILD_DIR/packet-client" ./DataPlane/client
)
xcrun clang "$SCRIPT_DIR/runner.c" \
  -I "$BUILD_DIR/engine" -L "$BUILD_DIR/engine" -lWhiteTransportTun2Socks \
  -framework CoreFoundation -framework Security -lresolv -lpthread \
  -o "$BUILD_DIR/runner"

: > "$ARTIFACT_DIR/fixture.log"
: > "$ARTIFACT_DIR/client.log"
: > "$ARTIFACT_DIR/runner.log"
: > "$ARTIFACT_DIR/round"
export WT_C_ABI_ROUND_FILE="$ARTIFACT_DIR/round"
export WT_CLIENT_LOG="$ARTIFACT_DIR/client.log"
python3 "$SCRIPT_DIR/local_fixture.py" > "$ARTIFACT_DIR/fixture.log" 2>&1 &
FIXTURE_PID=$!
FIXTURE_JSON=""
for _ in $(seq 1 50); do
  if [[ -s "$ARTIFACT_DIR/fixture.log" ]]; then
    IFS= read -r FIXTURE_JSON < "$ARTIFACT_DIR/fixture.log" || true
    [[ -n "$FIXTURE_JSON" ]] && break
  fi
  sleep 0.1
done
if [[ -z "$FIXTURE_JSON" ]]; then
  echo "fixture startup result=failure" >&2
  exit 1
fi
SOCKS_PORT="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["socks"])' "$FIXTURE_JSON")"

set +e
"$BUILD_DIR/runner" "$BUILD_DIR/packet-client" "$SOCKS_PORT" > "$ARTIFACT_DIR/runner.log" 2>&1 &
RUNNER_PID=$!
wait "$RUNNER_PID"
RUNNER_EXIT=$?
set -e
if [[ "$RUNNER_EXIT" -eq 0 ]]; then
  EXIT_STATUS=0
  printf 'interpretation=pass proof_boundary=C-ABI-engine-smoke rounds=%s\n' "${WT_TEST_ROUNDS:-3}"
else
  printf 'interpretation=fail proof_boundary=C-ABI-engine-smoke runner_exit=%s artifact_dir=%s\n' "$RUNNER_EXIT" "$ARTIFACT_DIR" >&2
  EXIT_STATUS="$RUNNER_EXIT"
fi
exit "$EXIT_STATUS"
