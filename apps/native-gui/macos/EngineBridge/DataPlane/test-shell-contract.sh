#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS="$SCRIPT_DIR/test-c-abi-data-plane.sh"

grep -Fq 'C-ABI engine smoke' "$HARNESS"
grep -Fq 'GOARCH="$HOST_ARCH"' "$HARNESS"
! grep -Fq 'GOARCH=arm64' "$HARNESS"
! grep -Fq 'ipconfig getifaddr en0' "$HARNESS"
! grep -Fq 'ifconfig' "$HARNESS"
! grep -Fq -- '--ipv4-host' "$HARNESS"
! grep -Fq -- '--ipv6-host' "$HARNESS"
for log in fixture.log client.log runner.log manifest; do
  grep -Fq "$log" "$HARNESS"
done
grep -Fq 'retained=true' "$HARNESS"
grep -Fq 'retained=false' "$HARNESS"
grep -Fq 'artifact_dir=' "$HARNESS"
grep -Fq 'WT_C_ABI_ARTIFACT_DIR' "$HARNESS"
grep -Fq 'ARTIFACT_DIR_AUTO_CREATED=0' "$HARNESS"
grep -Fq 'ARTIFACT_DIR_AUTO_CREATED=1' "$HARNESS"
grep -Fq 'if [[ "$ARTIFACT_DIR_AUTO_CREATED" -eq 1 ]]; then' "$HARNESS"
grep -Fq 'runner_wait_status=$?' "$HARNESS"
grep -Fq 'rounds=%s\n' "$HARNESS"
grep -Fq '"${WT_TEST_ROUNDS:-3}"' "$HARNESS"
! grep -Fq '[[ "$RUNNER_EXIT" != "not-started" ]] || RUNNER_EXIT=$?' "$HARNESS"
! grep -Fq "rounds=\${WT_TEST_ROUNDS:-3}\\n'" "$HARNESS"
grep -Fq 'interpretation=pass' "$HARNESS"
grep -Fq 'interpretation=fail' "$HARNESS"

TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/wt-c-abi-shell-contract.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT
CLEANUP_SOURCE="$(awk '/^cleanup\(\) \{/{capture=1} capture {print} capture && /^}$/ {exit}' "$HARNESS")"

CALLER_DIR="$TEST_ROOT/caller-owned"
mkdir -p "$CALLER_DIR"
: > "$CALLER_DIR/runner.log"
: > "$CALLER_DIR/sentinel"
CALLER_OUTPUT="$(CLEANUP_SOURCE="$CLEANUP_SOURCE" bash -c '
  eval "$CLEANUP_SOURCE"
  ARTIFACT_DIR="$1"
  ARTIFACT_DIR_AUTO_CREATED=0
  FIXTURE_PID=""
  RUNNER_PID=""
  FIXTURE_EXIT="not-started"
  RUNNER_EXIT=0
  cleanup
' _ "$CALLER_DIR")"
[[ -f "$CALLER_DIR/sentinel" ]]
grep -Fq 'status=pass retained=true' <<< "$CALLER_OUTPUT"

AUTO_DIR="$TEST_ROOT/auto-created"
mkdir -p "$AUTO_DIR"
: > "$AUTO_DIR/runner.log"
AUTO_OUTPUT="$(CLEANUP_SOURCE="$CLEANUP_SOURCE" bash -c '
  eval "$CLEANUP_SOURCE"
  ARTIFACT_DIR="$1"
  ARTIFACT_DIR_AUTO_CREATED=1
  FIXTURE_PID=""
  RUNNER_PID=""
  FIXTURE_EXIT="not-started"
  RUNNER_EXIT=0
  cleanup
' _ "$AUTO_DIR")"
[[ ! -e "$AUTO_DIR" ]]
grep -Fq 'status=pass retained=false' <<< "$AUTO_OUTPUT"

STATUS_DIR="$TEST_ROOT/status-capture"
mkdir -p "$STATUS_DIR"
: > "$STATUS_DIR/runner.log"
CLEANUP_SOURCE="$CLEANUP_SOURCE" bash -c '
  eval "$CLEANUP_SOURCE"
  ARTIFACT_DIR="$1"
  ARTIFACT_DIR_AUTO_CREATED=0
  FIXTURE_PID=""
  FIXTURE_EXIT="not-started"
  RUNNER_EXIT="not-started"
  (exit 23) &
  RUNNER_PID=$!
  cleanup
' _ "$STATUS_DIR" >/dev/null
grep -Fq 'runner_exit=23' "$STATUS_DIR/manifest"

PASS_PRINTF="$(grep -F "printf 'interpretation=pass proof_boundary=C-ABI-engine-smoke rounds=%s" "$HARNESS")"
ROUNDS_OUTPUT="$(WT_TEST_ROUNDS=7 bash -c "$PASS_PRINTF")"
grep -Fq 'rounds=7' <<< "$ROUNDS_OUTPUT"

echo "C-ABI engine smoke shell contract: OK"
