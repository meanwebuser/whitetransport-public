#!/usr/bin/env bash
# Build once, then execute the Mail failover canary in rootless net/mount namespaces.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
RUN_DIR="$(mktemp -d /tmp/wt-mail-socks-failover.XXXXXX)"
BIN="$RUN_DIR/whitetransportd"
SUCCESS=false

cleanup() {
    local status=$?
    if [[ "$SUCCESS" != true ]]; then
        mkdir -p "$REPO_ROOT/trash/logs"
        destination="$REPO_ROOT/trash/logs/mail-failover-failed-$(date +%s)"
        mv "$RUN_DIR" "$destination" 2>/dev/null || true
        printf 'Mail failover outer artifacts: %s\n' "$destination" >&2
        status=1
    elif [[ "${WT_KEEP_ARTIFACTS:-0}" == 1 ]]; then
        printf 'Mail failover artifacts retained: %s\n' "$RUN_DIR" >&2
    else
        rm -rf "$RUN_DIR"
    fi
    exit "$status"
}
trap cleanup EXIT

for command_name in ip iptables mount nsenter openssl pgrep ps setsid ss timeout tr unshare curl python3; do
    command -v "$command_name" >/dev/null
done
unshare --user --map-root-user --net true >/dev/null 2>&1

(cd "$REPO_ROOT/core/go" && GOPROXY=off /usr/local/go/bin/go build -o "$BIN" ./cmd/whitetransportd)
WT_REPO_ROOT="$REPO_ROOT" WT_RUN_DIR="$RUN_DIR" WT_BIN="$BIN" WT_SCRIPT_DIR="$SCRIPT_DIR" \
    unshare --user --map-root-user --mount --net \
    "$SCRIPT_DIR/test-mail-socks-failover-inner.sh"

[[ -s "$RUN_DIR/result.json" && -f "$RUN_DIR/cleanup.ok" ]]
python3 - "$RUN_DIR/result.json" <<'PY'
import json, sys
result=json.load(open(sys.argv[1],encoding="utf-8"))
result["cleanup"]={
    "processesExited":True,"listenersReleased":True,"networkLinksReleased":True,
    "mountNamespacesReleased":True,"childrenExited":True,"tripwireRulesReleased":True,
    "privateSpoolReleased":True,
}
print(json.dumps(result,separators=(",",":")))
PY
SUCCESS=true
