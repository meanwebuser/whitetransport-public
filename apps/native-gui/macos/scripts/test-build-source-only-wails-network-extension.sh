#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILD_SCRIPT="$SCRIPT_DIR/build-source-only-wails-network-extension.sh"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/wt-wails-build-contract.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT

FAKE_BIN="$TEST_ROOT/bin"
PRODUCTS_DIR="$TEST_ROOT/products"
WAILS_DYLD_CAPTURE="$TEST_ROOT/wails-dyld-framework-path"
WAILS_CGO_LDFLAGS_CAPTURE="$TEST_ROOT/wails-cgo-ldflags"
XCODE_DYLD_CAPTURE="$TEST_ROOT/xcode-dyld-framework-path"
EXISTING_FRAMEWORK_PATH="$TEST_ROOT/existing-frameworks"
mkdir -p "$FAKE_BIN" "$EXISTING_FRAMEWORK_PATH"

cat > "$FAKE_BIN/uname" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == "-s" ]]; then
  printf 'Darwin\n'
  exit 0
fi
exec /usr/bin/uname "$@"
EOF

cat > "$FAKE_BIN/xcodebuild" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "${DYLD_FRAMEWORK_PATH-}" >> "$WT_XCODE_DYLD_CAPTURE"
EOF

cat > "$FAKE_BIN/wails" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "${DYLD_FRAMEWORK_PATH-}" > "$WT_WAILS_DYLD_CAPTURE"
printf '%s\n' "${CGO_LDFLAGS-}" > "$WT_WAILS_CGO_LDFLAGS_CAPTURE"
exit 86
EOF

cat > "$FAKE_BIN/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
output=""
previous=""
for argument in "$@"; do
  if [[ "$previous" == "-o" ]]; then
    output="$argument"
    break
  fi
  previous="$argument"
done
test -n "$output"
printf '#!/bin/sh\nexit 0\n' > "$output"
chmod 755 "$output"
EOF

chmod +x "$FAKE_BIN/uname" "$FAKE_BIN/xcodebuild" "$FAKE_BIN/wails" "$FAKE_BIN/go"
export WT_WAILS_DYLD_CAPTURE="$WAILS_DYLD_CAPTURE"
export WT_WAILS_CGO_LDFLAGS_CAPTURE="$WAILS_CGO_LDFLAGS_CAPTURE"
export WT_XCODE_DYLD_CAPTURE="$XCODE_DYLD_CAPTURE"

set +e
PATH="$FAKE_BIN:/usr/bin:/bin" DYLD_FRAMEWORK_PATH="$EXISTING_FRAMEWORK_PATH" \
  "$BUILD_SCRIPT" "$PRODUCTS_DIR"
status=$?
set -e

if [[ "$status" -ne 86 ]]; then
  echo "build script did not reach the fake Wails process: exit=$status" >&2
  exit 1
fi

expected_wails_path="$PRODUCTS_DIR/Release:$EXISTING_FRAMEWORK_PATH"
actual_wails_path="$(cat "$WAILS_DYLD_CAPTURE")"
if [[ "$actual_wails_path" != "$expected_wails_path" ]]; then
  echo "Wails DYLD_FRAMEWORK_PATH=$actual_wails_path, want $expected_wails_path" >&2
  exit 1
fi

actual_wails_ldflags="$(cat "$WAILS_CGO_LDFLAGS_CAPTURE")"
expected_rpath_flag='-Wl,-rpath,@executable_path/../Frameworks'
if [[ "$actual_wails_ldflags" != *"$expected_rpath_flag"* ]]; then
  echo "Wails CGO_LDFLAGS=$actual_wails_ldflags, want to contain $expected_rpath_flag" >&2
  exit 1
fi

while IFS= read -r xcode_path; do
  if [[ "$xcode_path" != "$EXISTING_FRAMEWORK_PATH" ]]; then
    echo "xcodebuild DYLD_FRAMEWORK_PATH=$xcode_path, want unchanged $EXISTING_FRAMEWORK_PATH" >&2
    exit 1
  fi
done < "$XCODE_DYLD_CAPTURE"

echo "source-only Wails framework-path contract: OK"
