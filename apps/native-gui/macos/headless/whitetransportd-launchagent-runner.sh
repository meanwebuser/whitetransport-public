#!/bin/bash
set -uo pipefail
if [[ $# -lt 3 || $# -gt 4 ]]; then echo "usage: $0 <daemon> <config> <state-dir> [instance]" >&2; exit 64; fi
daemon=$1
config=$2
state_dir=$3
instance=${4:-whitetransport-macos-background}
if [[ ! -x "$daemon" || ! -f "$config" ]]; then echo "daemon/config is missing or not executable" >&2; exit 78; fi
umask 077
mkdir -p "$state_dir"
chmod 700 "$state_dir"
stderr_log="$state_dir/daemon.stderr.log"
status_file="$state_dir/status.env"
write_status() {
  api_url=$1; socks_listen=$2; connect_state=$3; daemon_state=$4
  tmp="$state_dir/status.env.tmp.$$"
  {
    printf 'api_url=%s\n' "$api_url"
    printf 'socks_listen=%s\n' "$socks_listen"
    printf 'connect_state=%s\n' "$connect_state"
    printf 'daemon_state=%s\n' "$daemon_state"
    printf 'updated_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  } >"$tmp"
  chmod 600 "$tmp"
  mv -f "$tmp" "$status_file"
}
extract_api() { sed -nE 's#.*(http://127\.0\.0\.1:[0-9]+).*#\1#p'; }
extract_socks() { sed -nE 's#.*"socks_listen"[[:space:]]*:[[:space:]]*"([^"]+)".*#\1#p'; }
write_status "" "" waiting starting
last_api=""
"$daemon" --config "$config" --serve --instance "$instance" 2>>"$stderr_log" |
  while IFS= read -r line; do
    api_url=$(printf '%s\n' "$line" | extract_api)
    if [[ -z "$api_url" || "$api_url" == "$last_api" ]]; then continue; fi
    last_api=$api_url
    connect_state=failed
    if curl --noproxy '*' --fail --silent --show-error --max-time 20 -X POST "$api_url/v1/session/connect" -H 'Content-Type: application/json' --data-binary '{}' >/dev/null 2>>"$stderr_log"; then connect_state=connected; fi
    status_json=$(curl --noproxy '*' --fail --silent --show-error --max-time 10 "$api_url/v1/status" 2>>"$stderr_log" || true)
    socks_listen=$(printf '%s\n' "$status_json" | extract_socks)
    write_status "$api_url" "$socks_listen" "$connect_state" running
  done
pipeline_status=("${PIPESTATUS[@]}")
daemon_status=${pipeline_status[0]}
write_status "$last_api" "" stopped exited
exit "$daemon_status"
