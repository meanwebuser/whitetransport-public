#!/usr/bin/env bash
set -euo pipefail

# Read-only macOS signing and Network Extension provisioning preflight.
#
# This script only reads `security` identities and local provisioning profiles.
# It never invokes Xcode, account, keychain, or provisioning update operations.

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "macOS signing preflight must run on Darwin" >&2
  exit 69
fi

for required_command in security plutil find mktemp; do
  if ! command -v "$required_command" >/dev/null 2>&1; then
    echo "missing required macOS command: $required_command" >&2
    exit 70
  fi
done

PROFILE_DIR="${WT_MACOS_PROFILE_DIR:-$HOME/Library/MobileDevice/Provisioning Profiles}"
APP_BUNDLE_ID="${WT_MACOS_APP_BUNDLE_ID:-com.meanwebuser.whitetransport}"
EXTENSION_BUNDLE_ID="${WT_MACOS_EXTENSION_BUNDLE_ID:-com.meanwebuser.whitetransport.packet-tunnel}"
APP_GROUP="${WT_MACOS_APP_GROUP:-group.com.meanwebuser.whitetransport}"

echo "macOS signing preflight (read-only)"

identity_output="$(security find-identity -v -p codesigning 2>/dev/null || true)"
identity_count="$(awk '/valid identities found/ {print $1; exit}' <<<"$identity_output")"
identity_count="${identity_count:-0}"
echo "code-signing identities: $identity_count"
if [[ "$identity_count" -gt 0 ]]; then
  while IFS= read -r identity_name; do
    [[ -n "$identity_name" ]] && echo "  identity: $identity_name"
  done < <(sed -nE 's/^ *[0-9]+\) [0-9A-F]+ \"(.*)\"$/\1/p' <<<"$identity_output")
else
  echo "ERROR: no valid code-signing identity is available in the login keychain" >&2
fi

if [[ ! -d "$PROFILE_DIR" ]]; then
  echo "provisioning profiles: 0 (directory missing: $PROFILE_DIR)"
  echo "ERROR: create/download a compatible macOS provisioning profile before signing" >&2
  exit 1
fi

profile_count=0
compatible_count=0
profile_paths=()
while IFS= read -r -d '' profile_path; do
  profile_paths+=("$profile_path")
done < <(find "$PROFILE_DIR" -type f \( -name '*.mobileprovision' -o -name '*.provisionprofile' \) -print0)
profile_count="${#profile_paths[@]}"
echo "provisioning profiles: $profile_count"

if [[ "$profile_count" -eq 0 ]]; then
  echo "ERROR: no provisioning profiles found in $PROFILE_DIR" >&2
  exit 1
fi

temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/wt-macos-signing-preflight.XXXXXX")"
trap 'rm -rf "$temp_dir"' EXIT

for profile_path in "${profile_paths[@]}"; do
  decoded_path="$temp_dir/$(basename "$profile_path").plist"
  if ! security cms -D -i "$profile_path" -o "$decoded_path" >/dev/null 2>&1; then
    echo "  profile: $(basename "$profile_path") (unreadable)"
    continue
  fi

  profile_dump="$(plutil -p "$decoded_path" 2>/dev/null || true)"
  entitlements_dump="$profile_dump"
  app_id_compatible=0
  if grep -Fq "$APP_BUNDLE_ID" <<<"$profile_dump" || grep -Fq "$EXTENSION_BUNDLE_ID" <<<"$profile_dump"; then
    app_id_compatible=1
  fi

  has_network_extension=0
  if grep -Fq 'com.apple.developer.networking.networkextension' <<<"$entitlements_dump" && \
     grep -Fq 'packet-tunnel-provider' <<<"$entitlements_dump"; then
    has_network_extension=1
  fi
  has_app_group=0
  if grep -Fq 'com.apple.security.application-groups' <<<"$entitlements_dump" && \
     grep -Fq "$APP_GROUP" <<<"$entitlements_dump"; then
    has_app_group=1
  fi

  if [[ "$app_id_compatible" -eq 1 && "$has_network_extension" -eq 1 && "$has_app_group" -eq 1 ]]; then
    compatible_count=$((compatible_count + 1))
    echo "  compatible: $(basename "$profile_path") (Network Extension packet-tunnel-provider; App Group $APP_GROUP)"
  else
    echo "  profile: $(basename "$profile_path") (missing required app ID, Network Extension, or App Group entitlement)"
  fi
done

echo "compatible profiles: $compatible_count"
if [[ "$identity_count" -eq 0 ]]; then
  echo "ERROR: install/select a valid Apple Development or Distribution signing identity" >&2
  exit 1
fi
if [[ "$compatible_count" -eq 0 ]]; then
  echo "ERROR: no profile authorizes $APP_BUNDLE_ID/$EXTENSION_BUNDLE_ID with packet-tunnel-provider and $APP_GROUP" >&2
  exit 1
fi

echo "required entitlements: Network Extension packet-tunnel-provider; App Group $APP_GROUP"
echo "preflight: OK"
