#!/usr/bin/env bash
set -euo pipefail

destination="${1:?usage: ensure-wintun.sh --destination <wintun.dll>}"
if [[ "${1:-}" == "--destination" ]]; then
  destination="${2:?usage: ensure-wintun.sh --destination <wintun.dll>}"
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source_path="${WT_NATIVE_GUI_WINTUN_DLL:-}"
if [[ -n "$source_path" ]]; then
  [[ -f "$source_path" ]] || { echo "Wintun override does not exist: $source_path" >&2; exit 1; }
  if [[ "$(readlink -f "$source_path")" != "$(readlink -m "$destination")" ]]; then
    install -D -m 0644 "$source_path" "$destination"
  fi
  echo "installed Wintun from explicit pinned source: $destination"
else
  exec bash "$script_dir/fetch-wintun.sh" --destination "$destination"
fi

[[ -s "$destination" ]] || { echo "Wintun output is empty: $destination" >&2; exit 1; }
