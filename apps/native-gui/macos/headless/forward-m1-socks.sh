#!/usr/bin/env bash
set -euo pipefail

# Open an authenticated local SSH forward to the loopback SOCKS listener on
# the headless M1. The daemon stays loopback-only; every device gets its own
# SSH-authenticated local endpoint.

usage() {
  cat >&2 <<'EOF'
Usage: forward-m1-socks.sh [options]

Options:
  --listen PORT              local loopback port (default: 1088)
  --m1-target USER@HOST      final M1 SSH target (default: user@127.0.0.1)
  --m1-port PORT             M1 SSH port (default: 2222)
  --proxy-jump USER@HOST     optional SSH jump host for another device
  --remote-home PATH         M1 home directory (default: /Users/user)
  --identity-file PATH       SSH private key path
  --known-hosts PATH         SSH known-hosts file
  -h, --help                 show this help

The command reads the actual SOCKS endpoint from the M1 headless runner state
and then stays in the foreground while the forward is active.
EOF
}

listen_port=1088
m1_target="${WT_M1_SSH_TARGET:-user@127.0.0.1}"
m1_port="${WT_M1_SSH_PORT:-2222}"
proxy_jump="${WT_M1_SSH_PROXY_JUMP:-}"
remote_home="${WT_M1_REMOTE_HOME:-/Users/user}"
identity_file="${WT_M1_SSH_IDENTITY_FILE:-${HOME}/.ssh/id_rsa}"
known_hosts="${WT_M1_SSH_KNOWN_HOSTS_FILE:-${HOME}/.ssh/known_hosts}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --listen) [[ $# -ge 2 ]] || { usage; exit 2; }; listen_port=$2; shift 2 ;;
    --m1-target) [[ $# -ge 2 ]] || { usage; exit 2; }; m1_target=$2; shift 2 ;;
    --m1-port) [[ $# -ge 2 ]] || { usage; exit 2; }; m1_port=$2; shift 2 ;;
    --proxy-jump) [[ $# -ge 2 ]] || { usage; exit 2; }; proxy_jump=$2; shift 2 ;;
    --remote-home) [[ $# -ge 2 ]] || { usage; exit 2; }; remote_home=$2; shift 2 ;;
    --identity-file) [[ $# -ge 2 ]] || { usage; exit 2; }; identity_file=$2; shift 2 ;;
    --known-hosts) [[ $# -ge 2 ]] || { usage; exit 2; }; known_hosts=$2; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) printf 'unknown option: %s\n' "$1" >&2; usage; exit 2 ;;
  esac
done

is_port() { [[ "$1" =~ ^[1-9][0-9]{0,4}$ ]] && ((10#$1 <= 65535)); }
is_target() { [[ "$1" =~ ^[A-Za-z0-9._-]+@[A-Za-z0-9._:-]+$ ]]; }

is_port "$listen_port" || { printf 'invalid local port: %s\n' "$listen_port" >&2; exit 2; }
is_port "$m1_port" || { printf 'invalid M1 SSH port: %s\n' "$m1_port" >&2; exit 2; }
is_target "$m1_target" || { printf 'invalid M1 SSH target\n' >&2; exit 2; }
if [[ -n "$proxy_jump" ]] && ! is_target "$proxy_jump"; then
  printf 'invalid SSH proxy jump\n' >&2
  exit 2
fi
[[ "$remote_home" =~ ^/[A-Za-z0-9._/-]+$ ]] || {
  printf 'remote home must be a simple absolute macOS path\n' >&2
  exit 2
}
[[ -r "$known_hosts" ]] || { printf 'known-hosts file is not readable: %s\n' "$known_hosts" >&2; exit 78; }
[[ -r "$identity_file" ]] || { printf 'identity file is not readable: %s\n' "$identity_file" >&2; exit 78; }

ssh_args=(
  -o BatchMode=yes
  -o ExitOnForwardFailure=yes
  -o StrictHostKeyChecking=yes
  -o UserKnownHostsFile="$known_hosts"
  -o IdentitiesOnly=yes
  -o IdentityFile="$identity_file"
  -o ServerAliveInterval=30
  -o ServerAliveCountMax=3
  -o LogLevel=ERROR
)
if [[ -n "$proxy_jump" ]]; then
  ssh_args+=( -o ProxyJump="$proxy_jump" )
fi

status_line="$(ssh "${ssh_args[@]}" -p "$m1_port" "$m1_target" sh -s -- "$remote_home" <<'REMOTE'
set -eu
home="$1"
status_file="$home/Library/Application Support/WhiteTransport/headless/state/status.env"
[[ -r "$status_file" ]] || { printf 'headless status is unavailable\n' >&2; exit 70; }
api_url="$(awk -F= '$1 == "api_url" {print $2}' "$status_file")"
daemon_state="$(awk -F= '$1 == "daemon_state" {print $2}' "$status_file")"
connect_state="$(awk -F= '$1 == "connect_state" {print $2}' "$status_file")"
updated_at="$(awk -F= '$1 == "updated_at" {print $2}' "$status_file")"
[[ "$api_url" =~ ^http://127\.0\.0\.1:[1-9][0-9]{0,4}$ ]] || { printf 'headless API endpoint is invalid\n' >&2; exit 70; }
live_status="$(curl --noproxy '*' --fail --silent --show-error --max-time 5 "$api_url/v1/status")"
live_state="$(printf '%s' "$live_status" | sed -nE 's/.*"state"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p')"
socks_listen="$(printf '%s' "$live_status" | sed -nE 's/.*"socks_listen"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p')"
printf '%s\t%s\t%s\t%s\n' "$daemon_state" "$live_state" "$socks_listen" "$updated_at"
REMOTE
)"

IFS=$'\t' read -r daemon_state connect_state socks_listen updated_at <<<"$status_line"
[[ "$daemon_state" == "running" ]] || { printf 'M1 daemon is not running: %s\n' "$daemon_state" >&2; exit 70; }
[[ "$connect_state" == "connected" ]] || { printf 'M1 session is not connected: %s\n' "$connect_state" >&2; exit 70; }
[[ "$socks_listen" =~ ^127\.0\.0\.1:[1-9][0-9]{0,4}$ ]] || {
  printf 'M1 status did not expose a loopback SOCKS endpoint\n' >&2
  exit 70
}
socks_port="${socks_listen##*:}"
is_port "$socks_port" || { printf 'invalid M1 SOCKS port\n' >&2; exit 70; }

printf 'forwarding 127.0.0.1:%s -> M1 %s (status=%s)\n' "$listen_port" "$socks_listen" "$updated_at"
exec ssh "${ssh_args[@]}" -N -T \
  -L "127.0.0.1:${listen_port}:127.0.0.1:${socks_port}" \
  -p "$m1_port" "$m1_target"
