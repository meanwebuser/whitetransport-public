#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
manifest="${root_dir}/build/windows/wails.exe.manifest"
package_script="${root_dir}/package.json"
ensure_wintun_script="${root_dir}/scripts/ensure-wintun.sh"
post_pack_script="${root_dir}/scripts/post-build-pack-windows.sh"

[[ -f "${manifest}" ]] || { echo "missing Windows application manifest: ${manifest}" >&2; exit 1; }
grep -Fq '<requestedExecutionLevel level="requireAdministrator" uiAccess="false"/>' "${manifest}" || {
  echo "Windows manifest must request administrator elevation" >&2
  exit 1
}
grep -Fq 'wails build -platform windows/amd64' "${package_script}" || {
  echo "Windows package script must build amd64" >&2
  exit 1
}
grep -Fq 'ensure-wintun.sh --destination build/bin/wintun.dll' "${package_script}" || {
  echo "Windows package script must use the reproducible Wintun ensure step" >&2
  exit 1
}
[[ -x "${ensure_wintun_script}" ]] || { echo "missing Wintun ensure script: ${ensure_wintun_script}" >&2; exit 1; }
grep -Fq 'post-build-pack-windows.sh' "${package_script}" || {
  echo "Windows package script must bundle daemon and client runtime resources" >&2
  exit 1
}
[[ -x "${post_pack_script}" ]] || { echo "missing Windows post-pack script: ${post_pack_script}" >&2; exit 1; }

echo "Windows amd64 package contract: PASS"
