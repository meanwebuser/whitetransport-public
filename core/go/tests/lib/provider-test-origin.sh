#!/usr/bin/env bash
# Shared test-owned HTTP origin for provider integration scripts.

PROVIDER_ORIGIN_PID=""
PROVIDER_ORIGIN_PORT=""
PROVIDER_ORIGIN_NONCE=""
PROVIDER_ORIGIN_READY_FILE=""
PROVIDER_ORIGIN_LOG=""

start_provider_test_origin() {
    local run_name="$1"
    local attempt

    PROVIDER_ORIGIN_READY_FILE="$(mktemp "/tmp/${run_name}-origin-ready.XXXXXX")"
    PROVIDER_ORIGIN_LOG="/tmp/${run_name}-origin.log"
    PROVIDER_ORIGIN_NONCE="wt-${run_name}-$(python3 -c 'import secrets; print(secrets.token_hex(16))')"
    rm -f "$PROVIDER_ORIGIN_READY_FILE"

    python3 - "$PROVIDER_ORIGIN_READY_FILE" "$PROVIDER_ORIGIN_NONCE" >"$PROVIDER_ORIGIN_LOG" 2>&1 <<'PY' &
import http.server
import pathlib
import sys

ready_path = pathlib.Path(sys.argv[1])
nonce = sys.argv[2].encode("ascii")


class NonceHandler(http.server.BaseHTTPRequestHandler):
    """Serve one exact per-run nonce as the provider test payload."""

    def do_GET(self) -> None:
        self.send_response(200)
        self.send_header("Content-Length", str(len(nonce)))
        self.send_header("Content-Type", "text/plain")
        self.end_headers()
        self.wfile.write(nonce)

    def log_message(self, _format: str, *_args: object) -> None:
        return


server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), NonceHandler)
ready_path.write_text(str(server.server_port), encoding="ascii")
server.serve_forever()
PY
    PROVIDER_ORIGIN_PID=$!

    for attempt in $(seq 1 50); do
        if ! kill -0 "$PROVIDER_ORIGIN_PID" 2>/dev/null; then
            echo "[!] Test-owned HTTP origin exited during startup; see $PROVIDER_ORIGIN_LOG" >&2
            return 1
        fi
        if [[ -s "$PROVIDER_ORIGIN_READY_FILE" ]]; then
            PROVIDER_ORIGIN_PORT="$(cat "$PROVIDER_ORIGIN_READY_FILE")"
            break
        fi
        sleep .1
    done

    if [[ ! "$PROVIDER_ORIGIN_PORT" =~ ^[0-9]+$ ]]; then
        echo "[!] Test-owned HTTP origin did not publish a valid port" >&2
        return 1
    fi
    if ! kill -0 "$PROVIDER_ORIGIN_PID" 2>/dev/null; then
        echo "[!] Test-owned HTTP origin died before its liveness check" >&2
        return 1
    fi

    local direct_payload
    direct_payload="$(curl --max-time 3 -sf "http://127.0.0.1:${PROVIDER_ORIGIN_PORT}/")" || {
        echo "[!] Test-owned HTTP origin did not answer its direct preflight" >&2
        return 1
    }
    if [[ "$direct_payload" != "$PROVIDER_ORIGIN_NONCE" ]]; then
        echo "[!] Test-owned HTTP origin returned the wrong direct preflight payload" >&2
        return 1
    fi
}

stop_provider_test_origin() {
    if [[ -n "$PROVIDER_ORIGIN_PID" ]]; then
        kill "$PROVIDER_ORIGIN_PID" 2>/dev/null || true
        wait "$PROVIDER_ORIGIN_PID" 2>/dev/null || true
    fi
    [[ -n "$PROVIDER_ORIGIN_READY_FILE" ]] && rm -f "$PROVIDER_ORIGIN_READY_FILE"
}
