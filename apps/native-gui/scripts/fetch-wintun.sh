#!/usr/bin/env bash
set -euo pipefail

version="0.14.1"
archive_sha256="07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51"
destination="${1:-}"
if [[ "${destination}" != "--destination" ]]; then
  echo "usage: $0 --destination <wintun.dll>" >&2
  exit 2
fi
destination="${2:-}"
if [[ -z "${destination}" || "${destination}" == -* ]]; then
  echo "usage: $0 --destination <wintun.dll>" >&2
  exit 2
fi

if ! command -v curl >/dev/null 2>&1 || ! command -v unzip >/dev/null 2>&1; then
  echo "fetch-wintun requires curl and unzip" >&2
  exit 1
fi

work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT
archive="${work_dir}/wintun-${version}.zip"
curl --ipv4 --fail --location --silent --show-error --connect-timeout 15 --max-time 120 "https://www.wintun.net/builds/wintun-${version}.zip" --output "${archive}"
actual_sha256="$(sha256sum "${archive}" | awk '{print $1}')"
if [[ "${actual_sha256}" != "${archive_sha256}" ]]; then
  echo "Wintun archive SHA-256 mismatch: ${actual_sha256}" >&2
  exit 1
fi

mkdir -p "$(dirname "${destination}")"
unzip -p "${archive}" "wintun/bin/amd64/wintun.dll" > "${destination}.tmp"
mv "${destination}.tmp" "${destination}"
echo "installed Wintun ${version} amd64 DLL: ${destination}"
