#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  printf 'usage: %s start|stop|status|test\n' "$0" >&2
  exit 2
fi
case "$1" in
  start|stop|status|test) ;;
  *) printf 'unsupported command: %s\n' "$1" >&2; exit 2 ;;
esac

console_user_home() {
  local user="${SUDO_USER:-$(id -un)}"
  if [[ "$user" == "root" || -z "$user" ]]; then
    printf '%s\n' "${HOME}"
    return
  fi
  if command -v dscl >/dev/null 2>&1; then
    dscl . -read "/Users/$user" NFSHomeDirectory | awk '{print $2}'
    return
  fi
  if command -v getent >/dev/null 2>&1; then
    getent passwd "$user" | awk -F: '{print $6}'
    return
  fi
  printf '%s\n' "$HOME"
}

USER_HOME="$(console_user_home)"
BIN="${WT_DIRECT_HELPER_BIN:-$USER_HOME/Library/Application Support/WhiteTransport/bin/direct-helper}"
CONFIG="${WT_DIRECT_HELPER_CONFIG:-$USER_HOME/Library/Application Support/WhiteTransport/direct-helper/config.json}"
exec "$BIN" "$1" --config "$CONFIG"
