#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VERIFY_SCRIPT="$SCRIPT_DIR/verify-local.sh"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/wt-native-gui-toolchain.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT

cat > "$TEST_ROOT/go" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == "env" && "${2:-}" == "GOVERSION" ]]; then
  printf '%s\n' go1.24.9
  exit 0
fi
echo "unexpected Go invocation: $*" >&2
exit 1
EOF
chmod 755 "$TEST_ROOT/go"

set +e
output="$(WT_GO_BIN="$TEST_ROOT/go" "$VERIFY_SCRIPT" 2>&1)"
status=$?
set -e
if [[ "$status" -ne 2 ]]; then
  printf 'old Go toolchain returned status %s, want 2\n%s\n' "$status" "$output" >&2
  exit 1
fi
grep -Fq 'requires >=1.26.1' <<<"$output"

echo "native GUI Go toolchain floor contract: OK"
