#!/usr/bin/env bash
# Read endpoints only after the owned process has bound its listener.
wait_owned_port() {
    local file="$1" pid="$2" port
    for _ in $(seq 1 100); do
        port="$(cat "$file" 2>/dev/null || true)"
        if [[ "$port" =~ ^[0-9]+$ ]] && ((port > 0 && port < 65536)); then
            printf '%s' "$port"
            return 0
        fi
        kill -0 "$pid" 2>/dev/null || return 1
        sleep .1
    done
    return 1
}
owned_daemon_api_port() {
    local log="$1" pid="$2" port
    for _ in $(seq 1 100); do
        port="$(sed -n 's/.*whitetransportd api listening on http:\/\/127\.0\.0\.1:\([0-9][0-9]*\).*/\1/p' "$log" | tail -n 1)"
        if [[ "$port" =~ ^[0-9]+$ ]] && ((port > 0 && port < 65536)); then
            printf '%s' "$port"
            return 0
        fi
        kill -0 "$pid" 2>/dev/null || return 1
        sleep .1
    done
    return 1
}
owned_daemon_socks_port() {
    local api_port="$1"
    shift
    "$@" curl --noproxy '*' --max-time 2 -sf "http://127.0.0.1:$api_port/v1/status" |
        python3 -c 'import json,sys; address=json.load(sys.stdin)["socks_listen"]; host,port=address.rsplit(":",1); assert host=="127.0.0.1" and 0<int(port)<65536; print(port)'
}
