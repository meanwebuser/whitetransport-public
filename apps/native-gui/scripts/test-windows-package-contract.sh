#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
manifest="${root_dir}/build/windows/wails.exe.manifest"
package_script="${root_dir}/package.json"

[[ -f "${manifest}" ]] || { echo "missing Windows application manifest: ${manifest}" >&2; exit 1; }
grep -Fq '<requestedExecutionLevel level="requireAdministrator" uiAccess="false"/>' "${manifest}" || {
  echo "Windows manifest must request administrator elevation" >&2
  exit 1
}
grep -Fq 'wails build -platform windows/amd64' "${package_script}" || {
  echo "Windows package script must build amd64" >&2
  exit 1
}
grep -Fq 'fetch-wintun.sh --destination build/bin/wintun.dll' "${package_script}" || {
  echo "Windows package script must bundle Wintun" >&2
  exit 1
}

echo "Windows amd64 package contract: PASS"
