#!/usr/bin/env bash
# Deterministic regression for provider tests accidentally trusting port 18080.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/provider-test-origin.sh
source "$SCRIPT_DIR/lib/provider-test-origin.sh"

WRONG_PID=""
cleanup() {
    stop_provider_test_origin
    if [[ -n "$WRONG_PID" ]]; then
        kill "$WRONG_PID" 2>/dev/null || true
        wait "$WRONG_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# If another process already owns 18080, that is also a valid preoccupied
# condition. Otherwise this deliberately installs an unrelated listener there.
python3 - <<'PY' >/tmp/provider-origin-wrong.log 2>&1 &
import http.server


class WrongHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        body = b"wrong-fixed-port-origin"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, _format: str, *_args: object) -> None:
        return


http.server.ThreadingHTTPServer(("127.0.0.1", 18080), WrongHandler).serve_forever()
PY
WRONG_PID=$!
sleep .2
if ! kill -0 "$WRONG_PID" 2>/dev/null; then
    wait "$WRONG_PID" 2>/dev/null || true
    WRONG_PID=""
fi

start_provider_test_origin "provider-origin-contract"
kill -0 "$PROVIDER_ORIGIN_PID"
[[ "$PROVIDER_ORIGIN_PORT" != "18080" ]]
[[ "$(curl --max-time 3 -sf "http://127.0.0.1:${PROVIDER_ORIGIN_PORT}/")" == "$PROVIDER_ORIGIN_NONCE" ]]
[[ "$(curl --max-time 3 -sf http://127.0.0.1:18080/)" != "$PROVIDER_ORIGIN_NONCE" ]]

# curl honors NO_PROXY even when an explicit proxy is provided. Pointing the
# fake SOCKS endpoint at the HTTP origin makes the distinction deterministic:
# bypass returns the nonce, while a forced SOCKS handshake cannot succeed.
BYPASS_PAYLOAD="$(NO_PROXY=127.0.0.1 no_proxy=127.0.0.1 curl -x "socks5h://127.0.0.1:${PROVIDER_ORIGIN_PORT}" --max-time 3 -sf "http://127.0.0.1:${PROVIDER_ORIGIN_PORT}/")"
[[ "$BYPASS_PAYLOAD" == "$PROVIDER_ORIGIN_NONCE" ]]
if NO_PROXY=127.0.0.1 no_proxy=127.0.0.1 curl --noproxy '' -x "socks5h://127.0.0.1:${PROVIDER_ORIGIN_PORT}" --max-time 3 -sf "http://127.0.0.1:${PROVIDER_ORIGIN_PORT}/" >/dev/null 2>&1; then
    echo "[FAIL] curl reached the local origin despite a forced invalid SOCKS path" >&2
    exit 1
fi

echo "[PASS] Provider origin is owned, and forced SOCKS cannot be bypassed by NO_PROXY"
