#!/usr/bin/env bash
# test-singbox-vless.sh — guarded live sing-box VLESS/Xray egress smoke.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

: "${WT_SINGBOX_VLESS_URI:?set WT_SINGBOX_VLESS_URI to a vless:// profile}"
: "${WT_SINGBOX_BINARY:?set WT_SINGBOX_BINARY to the sing-box executable}"

SINGBOX_PORT="${WT_SINGBOX_SMOKE_PORT:-19080}"
TARGET_URL="${WT_SINGBOX_TARGET_URL:-https://ifconfig.me/ip}"
TMP_DIR="$(mktemp -d)"
CONFIG_PATH="$TMP_DIR/sing-box.json"
LOG_PATH="$TMP_DIR/sing-box.log"
SINGBOX_PID=""

cleanup() {
	[[ -n "$SINGBOX_PID" ]] && kill "$SINGBOX_PID" 2>/dev/null || true
	[[ -n "$SINGBOX_PID" ]] && wait "$SINGBOX_PID" 2>/dev/null || true
	rm -rf "$TMP_DIR"
}
trap cleanup EXIT

export PATH="$PATH:/usr/local/go/bin"

echo "[*] Running live Go tunnel smoke..."
(cd "$GO_ROOT" && WT_SINGBOX_REAL=1 /usr/local/go/bin/go test ./internal/tunnel -run TestRealSingBoxVLESSEgressHTTP -count=1 -v)

echo "[*] Generating temporary sing-box curl config..."
python3 - "$WT_SINGBOX_VLESS_URI" "$SINGBOX_PORT" >"$CONFIG_PATH" <<'PY'
import json
import sys
from urllib.parse import parse_qs, urlparse

uri = sys.argv[1]
listen_port = int(sys.argv[2])
parsed = urlparse(uri)
query = parse_qs(parsed.query)
if parsed.scheme != "vless":
    raise SystemExit("WT_SINGBOX_VLESS_URI must use vless://")

def one(name, default=""):
    values = query.get(name)
    return values[0] if values else default

outbound = {
    "type": "vless",
    "tag": "proxy",
    "server": parsed.hostname,
    "server_port": parsed.port,
    "uuid": parsed.username,
    "network": "tcp",
}
if one("security") == "tls":
    tls = {
        "enabled": True,
        "server_name": one("sni", parsed.hostname),
        "insecure": one("allowInsecure", "0") in ("1", "true"),
    }
    if one("fp"):
        tls["utls"] = {"enabled": True, "fingerprint": one("fp")}
    outbound["tls"] = tls
if one("type"):
    transport = {"type": one("type")}
    if one("host"):
        transport["host"] = one("host")
    if one("path"):
        transport["path"] = one("path")
    outbound["transport"] = transport

print(json.dumps({
    "log": {"level": "warn", "timestamp": True},
    "inbounds": [{
        "type": "mixed",
        "tag": "wt-curl-in",
        "listen": "127.0.0.1",
        "listen_port": listen_port,
    }],
    "outbounds": [
        outbound,
        {"type": "direct", "tag": "direct"},
    ],
    "route": {"final": "proxy"},
}, indent=2))
PY

echo "[*] Starting sing-box for curl smoke..."
"$WT_SINGBOX_BINARY" run -c "$CONFIG_PATH" >"$LOG_PATH" 2>&1 &
SINGBOX_PID=$!
for _ in $(seq 1 50); do
	if (echo >"/dev/tcp/127.0.0.1/$SINGBOX_PORT") >/dev/null 2>&1; then
		break
	fi
	sleep 0.1
done

echo "[*] Running curl through sing-box SOCKS..."
DIRECT_IP="$(env -i PATH=/usr/bin:/bin curl -fsS --noproxy "*" --max-time 10 "$TARGET_URL" || true)"
SINGBOX_IP="$(env -i PATH=/usr/bin:/bin curl -fsS --max-time 20 --socks5-hostname "127.0.0.1:$SINGBOX_PORT" "$TARGET_URL")"

printf 'direct=%s\n' "$DIRECT_IP"
printf 'singbox=%s\n' "$SINGBOX_IP"
test -n "$SINGBOX_IP"
