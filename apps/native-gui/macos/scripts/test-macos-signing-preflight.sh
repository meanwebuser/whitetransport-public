#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PREFLIGHT_SCRIPT="$SCRIPT_DIR/macos-signing-preflight.sh"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/wt-macos-signing-preflight.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT

FAKE_BIN="$TEST_ROOT/bin"
PROFILE_DIR="$TEST_ROOT/profiles"
mkdir -p "$FAKE_BIN" "$PROFILE_DIR"

cat > "$FAKE_BIN/uname" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == "-s" ]]; then
  printf 'Darwin\n'
  exit 0
fi
exec /usr/bin/uname "$@"
EOF

cat > "$FAKE_BIN/security" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$WT_SECURITY_LOG"
case "${1:-}" in
  find-identity)
    printf '  1) ABCDEF0123456789ABCDEF0123456789ABCDEF01 "Apple Development: Test (TEAM123)"\n'
    printf '     1 valid identities found\n'
    ;;
  cms)
    output_path=""
    while [[ $# -gt 0 ]]; do
      if [[ "$1" == "-o" ]]; then output_path="$2"; shift 2; continue; fi
      shift
    done
    cp "$WT_PROFILE_DUMP" "$output_path"
    ;;
  *)
    echo "unexpected security invocation: $*" >&2
    exit 1
    ;;
esac
EOF

cat > "$FAKE_BIN/plutil" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
cat "$WT_PROFILE_DUMP"
EOF

chmod +x "$FAKE_BIN/uname" "$FAKE_BIN/security" "$FAKE_BIN/plutil"
touch "$PROFILE_DIR/white-transport.provisionprofile"
PROFILE_DUMP="$TEST_ROOT/profile-dump.txt"
SECURITY_LOG="$TEST_ROOT/security.log"

cat > "$PROFILE_DUMP" <<'EOF'
{
  "Entitlements" => {
    "application-identifier" => "TEAM123.com.meanwebuser.whitetransport.packet-tunnel"
    "com.apple.developer.networking.networkextension" => [
      0 => "packet-tunnel-provider"
    ]
    "com.apple.security.application-groups" => [
      0 => "group.com.meanwebuser.whitetransport"
    ]
  }
}
EOF

success_output="$TEST_ROOT/success.out"
PATH="$FAKE_BIN:/usr/bin:/bin" \
  HOME="$TEST_ROOT/home" \
  WT_MACOS_PROFILE_DIR="$PROFILE_DIR" \
  WT_PROFILE_DUMP="$PROFILE_DUMP" \
  WT_SECURITY_LOG="$SECURITY_LOG" \
  "$PREFLIGHT_SCRIPT" >"$success_output"

grep -Fq 'code-signing identities: 1' "$success_output"
grep -Fq 'provisioning profiles: 1' "$success_output"
grep -Fq 'compatible profiles: 1' "$success_output"
grep -Fq 'required entitlements: Network Extension packet-tunnel-provider; App Group group.com.meanwebuser.whitetransport' "$success_output"
grep -Fq 'preflight: OK' "$success_output"
grep -Fxq 'find-identity -v -p codesigning' "$SECURITY_LOG"
grep -Eq "^cms -D -i ${PROFILE_DIR//\//\\/}/white-transport\\.provisionprofile -o .*/white-transport\\.provisionprofile\\.plist$" "$SECURITY_LOG"
if grep -Eq 'xcodebuild|profiles (install|renew)|security (import|cms .* -S)' "$SECURITY_LOG"; then
  echo "preflight invoked a mutating provisioning command" >&2
  exit 1
fi

cat > "$PROFILE_DUMP" <<'EOF'
{
  "Entitlements" => {
    "application-identifier" => "TEAM123.com.meanwebuser.whitetransport.packet-tunnel"
    "com.apple.security.application-groups" => [
      0 => "group.com.meanwebuser.whitetransport"
    ]
  }
}
EOF

failure_output="$TEST_ROOT/failure.out"
if PATH="$FAKE_BIN:/usr/bin:/bin" HOME="$TEST_ROOT/home" WT_MACOS_PROFILE_DIR="$PROFILE_DIR" WT_PROFILE_DUMP="$PROFILE_DUMP" WT_SECURITY_LOG="$SECURITY_LOG" "$PREFLIGHT_SCRIPT" >"$failure_output" 2>&1; then
  echo "preflight unexpectedly accepted a profile without packet-tunnel-provider" >&2
  exit 1
fi
grep -Fq 'no profile authorizes com.meanwebuser.whitetransport/com.meanwebuser.whitetransport.packet-tunnel with packet-tunnel-provider and group.com.meanwebuser.whitetransport' "$failure_output"

echo "macOS signing preflight shell contract: OK"
