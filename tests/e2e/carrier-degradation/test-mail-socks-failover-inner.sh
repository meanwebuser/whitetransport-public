#!/usr/bin/env bash
# Rootless three-surface proof: private TLS Mail control+egress -> file backup -> Mail failback.
set -Eeuo pipefail

on_error() {
    local status=$?
    printf 'Mail failover inner failure: line=%s status=%s command=%q\n' "$1" "$status" "$2" >&2
    return "$status"
}
trap 'on_error "$LINENO" "$BASH_COMMAND"' ERR

: "${WT_REPO_ROOT:?}" "${WT_RUN_DIR:?}" "${WT_BIN:?}" "${WT_SCRIPT_DIR:?}"

MAIL_ROOT="$WT_RUN_DIR/mail-private"
CLIENT_ROOT="$WT_RUN_DIR/client-private"
NODE_ROOT="$WT_RUN_DIR/node-private"
BACKUP_ROOT="$WT_RUN_DIR/file-backup"
MAIL_STATE="$MAIL_ROOT/spool"
MAIL_FAIL="$MAIL_ROOT/mail.failed"
MAIL_LOG="$WT_RUN_DIR/mail.log"
CLIENT_LOG="$WT_RUN_DIR/client.log"
NODE_LOG="$WT_RUN_DIR/node.log"
CLIENT_CFG="$WT_RUN_DIR/client.json"
NODE_CFG="$WT_RUN_DIR/node.json"
CERT="$WT_RUN_DIR/mail.crt"
KEY="$WT_RUN_DIR/mail.key"
STREAM_OUT="$WT_RUN_DIR/stream.out"
STREAM_ERR="$WT_RUN_DIR/stream.err"
STREAM_MARKER="$NODE_ROOT/stream.started"
TARGET_EVENTS="$WT_RUN_DIR/target-events.jsonl"
RESULT_FILE="$WT_RUN_DIR/result.json"
CLEANUP_RECEIPT="$WT_RUN_DIR/cleanup.ok"

MAIL_ADDR=10.204.0.2
CLIENT_ADDR=10.204.0.3
NODE_ADDR=10.204.0.4
SMTP_PORT=20465
IMAP_PORT=20993
CLIENT_API=20631
CLIENT_SOCKS=20632
NODE_API=20641
NODE_SOCKS=20642
TARGET_PORT=20701
PRIMARY_NONCE="mail-primary-${RANDOM}-${RANDOM}"
BACKUP_NONCE="file-backup-${RANDOM}-${RANDOM}"
RECOVERED_NONCE="mail-recovered-${RANDOM}-${RANDOM}"

MAIL_HOLDER="" CLIENT_HOLDER="" NODE_HOLDER=""
MAIL_PID="" MAIL_PGID="" MAIL_INITIAL_PID="" MAIL_RECOVERED_PID=""
CLIENT_PID="" NODE_PID="" TARGET_PID="" STREAM_PID=""
SUCCESS=false

kill_mail_service() {
    [[ -n "$MAIL_PGID" ]] || return 0
    kill -- "-$MAIL_PGID" 2>/dev/null || true
    for _ in $(seq 1 80); do kill -0 "$MAIL_PID" 2>/dev/null || break; sleep .025; done
    kill -KILL -- "-$MAIL_PGID" 2>/dev/null || true
    wait "$MAIL_PID" 2>/dev/null || true
    MAIL_PID="" MAIL_PGID=""
}

cleanup() {
    local status=$? cleanup_ok=true pid link
    local tracked_pids="$MAIL_INITIAL_PID $MAIL_RECOVERED_PID $MAIL_PID $CLIENT_PID $NODE_PID $TARGET_PID $STREAM_PID $MAIL_HOLDER $CLIENT_HOLDER $NODE_HOLDER"
    set +e
    kill_mail_service
    for pid in "$STREAM_PID" "$CLIENT_PID" "$NODE_PID" "$TARGET_PID"; do [[ -z "$pid" ]] || kill "$pid" 2>/dev/null || true; done
    for pid in "$STREAM_PID" "$CLIENT_PID" "$NODE_PID" "$TARGET_PID"; do [[ -z "$pid" ]] || wait "$pid" 2>/dev/null || true; done
    for pid in "$MAIL_HOLDER" "$CLIENT_HOLDER" "$NODE_HOLDER"; do [[ -z "$pid" ]] || kill "$pid" 2>/dev/null || true; done
    for pid in "$MAIL_HOLDER" "$CLIENT_HOLDER" "$NODE_HOLDER"; do [[ -z "$pid" ]] || wait "$pid" 2>/dev/null || true; done
    for link in veth-mail veth-client veth-node; do ip link del "$link" 2>/dev/null || true; done
    ip link del wtmailbr0 2>/dev/null || true
    for pid in $tracked_pids; do
        if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
            printf 'cleanup leaked pid %s\n' "$pid" >&2
            cleanup_ok=false
        fi
    done
    for link in wtmailbr0 veth-mail veth-client veth-node; do
        if ip link show "$link" >/dev/null 2>&1; then
            printf 'cleanup leaked network link %s\n' "$link" >&2
            cleanup_ok=false
        fi
    done
    if grep -Fq " $MAIL_ROOT " /proc/self/mountinfo \
        || grep -Fq " $CLIENT_ROOT " /proc/self/mountinfo \
        || grep -Fq " $NODE_ROOT " /proc/self/mountinfo; then
        printf 'cleanup leaked private mount into orchestrator surface\n' >&2
        cleanup_ok=false
    fi
    if [[ -e "$MAIL_ROOT/SPOOL_SENTINEL" ]]; then
        printf 'private Mail spool leaked into orchestrator mount surface\n' >&2
        cleanup_ok=false
    fi
    if ss -ltnH 2>/dev/null | grep -Eq ":(${SMTP_PORT}|${IMAP_PORT}|${CLIENT_API}|${CLIENT_SOCKS}|${NODE_API}|${NODE_SOCKS}|${TARGET_PORT})[[:space:]]"; then
        printf 'cleanup leaked test listener\n' >&2
        cleanup_ok=false
    fi
    if pgrep -f "$WT_RUN_DIR" >/dev/null 2>&1; then
        printf 'cleanup leaked child referencing run directory\n' >&2
        cleanup_ok=false
    fi
    if [[ "$SUCCESS" == true && "$cleanup_ok" == true && "$status" -eq 0 ]]; then
        : >"$CLEANUP_RECEIPT"
    else
        status=1
    fi
    if [[ "$SUCCESS" != true || "$cleanup_ok" != true ]]; then
        printf 'Mail failover artifacts: %s\n' "$WT_RUN_DIR" >&2
        [[ ! -f "$CLIENT_LOG" ]] || tail -n 140 "$CLIENT_LOG" >&2
        [[ ! -f "$NODE_LOG" ]] || tail -n 100 "$NODE_LOG" >&2
        [[ ! -f "$MAIL_LOG" ]] || tail -n 80 "$MAIL_LOG" >&2
    fi
    exit "$status"
}
trap cleanup EXIT

wait_pid_namespace() {
    local pid="$1"
    for _ in $(seq 1 100); do kill -0 "$pid" 2>/dev/null && [[ -e "/proc/$pid/ns/net" ]] && return 0; sleep .02; done
    return 1
}

start_surface() {
    unshare --net --mount --propagation private bash -ceu 'mount --make-rprivate /; exec sleep infinity' >/dev/null 2>&1 &
    local pid=$!
    wait_pid_namespace "$pid"
    printf '%s\n' "$pid"
}

attach_surface() {
    local holder="$1" host_if="$2" address="$3"
    ip link add "$host_if" type veth peer name eth0
    ip link set "$host_if" master wtmailbr0
    ip link set "$host_if" up
    ip link set eth0 netns "$holder"
    nsenter -t "$holder" -n -- ip link set lo up
    nsenter -t "$holder" -n -- ip addr add "${address}/24" dev eth0
    nsenter -t "$holder" -n -- ip link set eth0 up
}

client_curl() { nsenter -t "$CLIENT_HOLDER" -n -- curl "$@"; }

wait_client_http() {
    local path="$1"
    for _ in $(seq 1 240); do
        kill -0 "$CLIENT_PID" 2>/dev/null || return 1
        client_curl --noproxy '*' --max-time 1 -sf "http://127.0.0.1:${CLIENT_API}${path}" >/dev/null 2>&1 && return 0
        sleep .1
    done
    return 1
}

start_mail_service() {
    rm -f "$WT_RUN_DIR/mail.ready"
    nsenter -t "$MAIL_HOLDER" -n -m -- env \
        WT_MAIL_STATE="$MAIL_STATE" WT_MAIL_FAIL="$MAIL_FAIL" \
        setsid python3 "$WT_SCRIPT_DIR/mail_tls_fixture.py" \
        --listen "$MAIL_ADDR" --smtp-port "$SMTP_PORT" --imap-port "$IMAP_PORT" \
        --state-dir "$MAIL_STATE" --cert "$CERT" --key "$KEY" --failure-flag "$MAIL_FAIL" \
        --ready-file "$WT_RUN_DIR/mail.ready" \
        --smtp-username fixture-smtp-user --smtp-password fixture-smtp-pass \
        --imap-username fixture-imap-user --imap-password fixture-imap-pass >>"$MAIL_LOG" 2>&1 &
    MAIL_PID=$!
    for _ in $(seq 1 160); do [[ -f "$WT_RUN_DIR/mail.ready" ]] && kill -0 "$MAIL_PID" 2>/dev/null && break; sleep .05; done
    [[ -f "$WT_RUN_DIR/mail.ready" ]] && kill -0 "$MAIL_PID" 2>/dev/null
    MAIL_PGID="$(ps -o pgid= -p "$MAIL_PID" | tr -d ' ')"
    [[ -n "$MAIL_PGID" ]]
}

mkdir -p "$MAIL_ROOT" "$CLIENT_ROOT" "$NODE_ROOT" "$BACKUP_ROOT"
mount --make-rprivate /
ip link set lo up
ip link add wtmailbr0 type bridge
ip addr add 10.204.0.1/24 dev wtmailbr0
ip link set wtmailbr0 up

MAIL_HOLDER="$(start_surface)"
CLIENT_HOLDER="$(start_surface)"
NODE_HOLDER="$(start_surface)"
attach_surface "$MAIL_HOLDER" veth-mail "$MAIL_ADDR"
attach_surface "$CLIENT_HOLDER" veth-client "$CLIENT_ADDR"
attach_surface "$NODE_HOLDER" veth-node "$NODE_ADDR"

nsenter -t "$MAIL_HOLDER" -m -- mount -t tmpfs -o mode=0700 tmpfs "$MAIL_ROOT"
nsenter -t "$CLIENT_HOLDER" -m -- mount -t tmpfs -o mode=0700 tmpfs "$CLIENT_ROOT"
nsenter -t "$NODE_HOLDER" -m -- mount -t tmpfs -o mode=0700 tmpfs "$NODE_ROOT"
nsenter -t "$MAIL_HOLDER" -m -- mkdir -p "$MAIL_STATE"
nsenter -t "$MAIL_HOLDER" -m -- touch "$MAIL_ROOT/SPOOL_SENTINEL"
nsenter -t "$MAIL_HOLDER" -m -- grep -Fq " $MAIL_ROOT " /proc/self/mountinfo
nsenter -t "$CLIENT_HOLDER" -m -- sh -ceu 'test ! -e "$1/SPOOL_SENTINEL"; ! grep -Fq " $1 " /proc/self/mountinfo' sh "$MAIL_ROOT"
nsenter -t "$NODE_HOLDER" -m -- sh -ceu 'test ! -e "$1/SPOOL_SENTINEL"; ! grep -Fq " $1 " /proc/self/mountinfo' sh "$MAIL_ROOT"

openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=mail.fixture.test' \
    -addext 'subjectAltName=DNS:mail.fixture.test' -keyout "$KEY" -out "$CERT" >/dev/null 2>&1
start_mail_service
MAIL_INITIAL_PID="$MAIL_PID"

python3 - "$NODE_CFG" "$CLIENT_CFG" "$NODE_ROOT" "$CLIENT_ROOT" "$BACKUP_ROOT" "$MAIL_ADDR" "$SMTP_PORT" "$IMAP_PORT" "$CERT" <<'PY'
import json, os, sys
node_path,client_path,node_root,client_root,backup_root,mail_addr,smtp_port,imap_port,cert=sys.argv[1:]
store={
 "tokens":[
  {"id":"local-session","platform":"local","kind":"api_key","lifecycle":"embedded","status":"active","value":"deterministic-local-session-key-not-a-secret"},
  {"id":"mail-account-a","platform":"mail","kind":"composite","lifecycle":"embedded","status":"active","parts":{"smtp_username":"fixture-smtp-user","smtp_password":"fixture-smtp-pass","imap_username":"fixture-imap-user","imap_password":"fixture-imap-pass"}}
 ],
 "bindings":[
  {"token_id":"local-session","platform":"local","connection_type":"messages","channel_id":"control","role":"discovery","priority":10,"enabled":True},
  {"token_id":"mail-account-a","platform":"mail","connection_type":"imap_smtp","channel_id":"account-a","role":"discovery","priority":10,"enabled":True},
  {"token_id":"mail-account-a","platform":"mail","connection_type":"imap_smtp","channel_id":"account-a","role":"egress","priority":10,"enabled":True}
 ]}
mail={"smtp_address":f"{mail_addr}:{smtp_port}","imap_address":f"{mail_addr}:{imap_port}","account_id":"account-a","mailbox":"INBOX","from_address":"sender@example.test","to_address":"receiver@example.test","tls_server_name":"mail.fixture.test","ca_file":cert,"timeout_seconds":2}
def carriers(): return [
 {"id":"mail.control","carrier_type":"mail.imap_smtp","role":"discovery","endpoint":{"id":"mail.control","address":"control"},"mail_imap_smtp":mail},
 {"id":"mail.primary","carrier_type":"mail.imap_smtp","role":"egress","endpoint":{"id":"mail.primary","address":"egress-primary"},"mail_imap_smtp":mail},
 {"id":"file.backup","carrier_type":"file.mailbox","role":"egress","endpoint":{"id":"file.backup","address":"egress-backup"},"file_mailbox":{"dir":backup_root,"allow_egress":True}}
]
common={"enabled_carriers":["mail.control","mail.primary","file.backup"],"token_store":store,"upstream_proxy":{"url":"","client_egress_only":True,"apply_to_carriers":False}}
node={**common,"role":"node","node_id":"mail-failover-node","listen_api":"127.0.0.1:20641","socks_listen":"127.0.0.1:20642","state_file":os.path.join(node_root,"state.json"),"carrier_configs":carriers()}
client={**common,"role":"client","client_id":"mail-failover-client","listen_api":"127.0.0.1:20631","socks_listen":"127.0.0.1:20632","state_file":os.path.join(client_root,"state.json"),"carrier_configs":carriers()}
for path,cfg in ((node_path,node),(client_path,client)):
    with open(path,"w",encoding="utf-8") as output: json.dump(cfg,output,separators=(",",":"))
PY
CLIENT_CFG_DIGEST="$(sha256sum "$CLIENT_CFG" | awk '{print $1}')"
NODE_CFG_DIGEST="$(sha256sum "$NODE_CFG" | awk '{print $1}')"

nsenter -t "$NODE_HOLDER" -n -m -- python3 "$WT_SCRIPT_DIR/mail_target_fixture.py" \
    --listen "$NODE_ADDR" --port "$TARGET_PORT" --primary-nonce "$PRIMARY_NONCE" \
    --backup-nonce "$BACKUP_NONCE" --recovered-nonce "$RECOVERED_NONCE" \
    --stream-marker "$STREAM_MARKER" --event-log "$TARGET_EVENTS" >"$WT_RUN_DIR/target.log" 2>&1 &
TARGET_PID=$!
nsenter -t "$NODE_HOLDER" -n -m -- env WT_DEBUG=1 HOME="$NODE_ROOT" \
    "$WT_BIN" -config "$NODE_CFG" -serve >"$NODE_LOG" 2>&1 &
NODE_PID=$!
nsenter -t "$CLIENT_HOLDER" -n -m -- env WT_DEBUG=1 HOME="$CLIENT_ROOT" \
    "$WT_BIN" -config "$CLIENT_CFG" -serve >"$CLIENT_LOG" 2>&1 &
CLIENT_PID=$!

for _ in $(seq 1 160); do
    nsenter -t "$NODE_HOLDER" -n -- curl --noproxy '*' --max-time 1 -sf "http://${NODE_ADDR}:${TARGET_PORT}/nonce" >/dev/null 2>&1 && break
    sleep .05
done
nsenter -t "$NODE_HOLDER" -n -- curl --noproxy '*' -sf "http://${NODE_ADDR}:${TARGET_PORT}/nonce" >/dev/null
wait_client_http /health
CLIENT_START_TICKS="$(awk '{print $22}' "/proc/$CLIENT_PID/stat")"
NODE_START_TICKS="$(awk '{print $22}' "/proc/$NODE_PID/stat")"

# Calibrate and reset a reject counter for every possible client-direct target SYN.
nsenter -t "$CLIENT_HOLDER" -n -- iptables -A OUTPUT -p tcp -d "$NODE_ADDR" --dport "$TARGET_PORT" -j REJECT
if client_curl --noproxy '*' --max-time 1 -sf "http://${NODE_ADDR}:${TARGET_PORT}/nonce" >/dev/null 2>&1; then
    printf 'direct tripwire calibration unexpectedly reached target\n' >&2
    exit 1
fi
CALIBRATED_PACKETS="$(nsenter -t "$CLIENT_HOLDER" -n -- iptables -L OUTPUT -v -n -x | awk -v ip="$NODE_ADDR" -v port="dpt:$TARGET_PORT" '$9==ip && index($0,port){print $1; exit}')"
[[ "${CALIBRATED_PACKETS:-0}" -gt 0 ]]
nsenter -t "$CLIENT_HOLDER" -n -- iptables -Z OUTPUT

for _ in $(seq 1 300); do
    client_curl --noproxy '*' --max-time 2 -sf "http://127.0.0.1:${CLIENT_API}/v1/nodes" \
      | python3 -c 'import json,sys; raise SystemExit(0 if any(n.get("node_id")=="mail-failover-node" and n.get("available") for n in json.load(sys.stdin)) else 1)' 2>/dev/null && break
    sleep .1
done
client_curl --noproxy '*' -sf -X POST "http://127.0.0.1:${CLIENT_API}/v1/session/connect" \
    -H 'Content-Type: application/json' -d '{"node_id":"mail-failover-node"}' >/dev/null
for _ in $(seq 1 300); do
    client_curl --noproxy '*' --max-time 2 -sf "http://127.0.0.1:${CLIENT_API}/v1/status" \
      | python3 -c 'import json,sys; s=json.load(sys.stdin); raise SystemExit(0 if s.get("state")=="connected" else 1)' 2>/dev/null && break
    sleep .1
done
client_curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API}/v1/status" \
  | python3 -c 'import json,sys; s=json.load(sys.stdin); assert s.get("state")=="connected" and not s.get("selected_egress_endpoint_id") and s.get("automatic_egress_endpoint_id")=="mail.primary",s'

BACKUP_BEFORE="$(find "$BACKUP_ROOT" -type f | wc -l)"
[[ "$BACKUP_BEFORE" -eq 0 ]]

client_curl --no-buffer --noproxy '' --max-time 70 -sf -x "socks5h://127.0.0.1:${CLIENT_SOCKS}" \
    "http://${NODE_ADDR}:${TARGET_PORT}/stream" >"$STREAM_OUT" 2>"$STREAM_ERR" &
STREAM_PID=$!
for _ in $(seq 1 200); do
    nsenter -t "$NODE_HOLDER" -m -- test -f "$STREAM_MARKER" \
      && grep -q "$PRIMARY_NONCE" "$STREAM_OUT" \
      && grep -q 'chunk-0002' "$STREAM_OUT" \
      && [[ "$(grep -c 'socks5 connected .*route=mail.primary' "$CLIENT_LOG" || true)" -ge 1 ]] && break
    sleep .1
done
nsenter -t "$NODE_HOLDER" -m -- test -f "$STREAM_MARKER"
grep -q 'chunk-0002' "$STREAM_OUT"

FAILURE_STARTED_MS="$(date +%s%3N)"
kill_mail_service
for holder in "$CLIENT_HOLDER" "$NODE_HOLDER"; do
    if timeout 2 nsenter -t "$holder" -n -- bash -c "</dev/tcp/${MAIL_ADDR}/${SMTP_PORT}" >/dev/null 2>&1; then
        printf 'SMTP remained reachable after outage\n' >&2; exit 1
    fi
    if timeout 2 nsenter -t "$holder" -n -- bash -c "</dev/tcp/${MAIL_ADDR}/${IMAP_PORT}" >/dev/null 2>&1; then
        printf 'IMAP remained reachable after outage\n' >&2; exit 1
    fi
done
for _ in $(seq 1 200); do kill -0 "$STREAM_PID" 2>/dev/null || break; sleep .1; done
if kill -0 "$STREAM_PID" 2>/dev/null; then printf 'open Mail stream exceeded interruption bound\n' >&2; exit 1; fi
STREAM_STATUS=0
wait "$STREAM_PID" 2>/dev/null || STREAM_STATUS=$?
STREAM_PID=""
[[ "$STREAM_STATUS" -ne 0 ]]
INTERRUPTION_MS="$(( $(date +%s%3N) - FAILURE_STARTED_MS ))"
[[ "$INTERRUPTION_MS" -le 20000 ]]

SECOND="$(client_curl --noproxy '' --max-time 70 -sf -x "socks5h://127.0.0.1:${CLIENT_SOCKS}" "http://${NODE_ADDR}:${TARGET_PORT}/backup")"
[[ "$SECOND" == "$BACKUP_NONCE" ]]
grep -q 'socks5 connected .*route=file.backup' "$CLIENT_LOG"
BACKUP_AFTER="$(find "$BACKUP_ROOT" -type f | wc -l)"
[[ "$BACKUP_AFTER" -gt "$BACKUP_BEFORE" ]]
python3 - "$BACKUP_ROOT" <<'PY'
import json,pathlib,sys
records=[]
for path in pathlib.Path(sys.argv[1]).glob("*.jsonl"):
    records += [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]
assert records, "file backup contains no envelopes"
classes={record.get("traffic_class") for record in records}
assert classes=={"egress"},f"file backup carried non-egress traffic: {classes}"
PY

start_mail_service
MAIL_RECOVERED_PID="$MAIL_PID"
[[ "$MAIL_RECOVERED_PID" != "$MAIL_INITIAL_PID" ]]
for _ in $(seq 1 1200); do
    client_curl --noproxy '*' --max-time 2 -sf "http://127.0.0.1:${CLIENT_API}/v1/carriers" \
      | python3 -c 'import json,sys; p=json.load(sys.stdin).get("mail.primary",{}); raise SystemExit(0 if p.get("healthy") is True and p.get("lifecycle_state")=="constructed" else 1)' 2>/dev/null && break
    sleep .1
done
client_curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API}/v1/carriers" \
  | python3 -c 'import json,sys; p=json.load(sys.stdin)["mail.primary"]; assert p.get("healthy") is True and p.get("lifecycle_state")=="constructed",p'
MAIL_ROUTES_BEFORE="$(grep -c 'socks5 connected .*route=mail.primary' "$CLIENT_LOG")"
sleep 27
THIRD=""
for _ in $(seq 1 12); do
    THIRD="$(client_curl --noproxy '' --max-time 12 -sf -x "socks5h://127.0.0.1:${CLIENT_SOCKS}" "http://${NODE_ADDR}:${TARGET_PORT}/recovered" 2>/dev/null || true)"
    if [[ "$THIRD" == "$RECOVERED_NONCE" ]] && [[ "$(grep -c 'socks5 connected .*route=mail.primary' "$CLIENT_LOG")" -gt "$MAIL_ROUTES_BEFORE" ]]; then
        break
    fi
    sleep .5
done
[[ "$THIRD" == "$RECOVERED_NONCE" ]]
[[ "$(grep -c 'socks5 connected .*route=mail.primary' "$CLIENT_LOG")" -gt "$MAIL_ROUTES_BEFORE" ]]
client_curl --noproxy '*' -sf "http://127.0.0.1:${CLIENT_API}/v1/status" \
  | python3 -c 'import json,sys; s=json.load(sys.stdin); assert s.get("state")=="connected" and not s.get("selected_egress_endpoint_id") and s.get("automatic_egress_endpoint_id")=="mail.primary",s'

[[ "$(awk '{print $22}' "/proc/$CLIENT_PID/stat")" == "$CLIENT_START_TICKS" ]]
[[ "$(awk '{print $22}' "/proc/$NODE_PID/stat")" == "$NODE_START_TICKS" ]]
[[ "$(sha256sum "$CLIENT_CFG" | awk '{print $1}')" == "$CLIENT_CFG_DIGEST" ]]
[[ "$(sha256sum "$NODE_CFG" | awk '{print $1}')" == "$NODE_CFG_DIGEST" ]]
wait_client_http /health

# No post-calibration client packet may target the exit-only origin directly.
TRIPWIRE_PACKETS="$(nsenter -t "$CLIENT_HOLDER" -n -- iptables -L OUTPUT -v -n -x | awk -v ip="$NODE_ADDR" -v port="dpt:$TARGET_PORT" '$9==ip && index($0,port){print $1; exit}')"
[[ "${TRIPWIRE_PACKETS:-0}" -eq 0 ]]
python3 - "$TARGET_EVENTS" "$NODE_ADDR" <<'PY'
import json,sys
events=[json.loads(line) for line in open(sys.argv[1],encoding="utf-8") if line.strip()]
assert len(events)>=5,events
assert {event["source"] for event in events}=={sys.argv[2]},events
assert {"/nonce","/stream","/backup","/recovered"}.issubset({event["path"] for event in events}),events
PY

# Audit the provider-visible spool after MIME transfer decode and wtmail1 decode.
nsenter -t "$MAIL_HOLDER" -m -- python3 - "$MAIL_STATE" "$WT_RUN_DIR/mail-audit.json" \
    "$PRIMARY_NONCE" "$BACKUP_NONCE" "$RECOVERED_NONCE" "http://${NODE_ADDR}:${TARGET_PORT}" <<'PY'
import base64,json,pathlib,sys
from email import policy
from email.parser import BytesParser
root,out,*forbidden=sys.argv[1:]
messages=list(pathlib.Path(root).glob("*.eml"))
assert messages,"private Mail spool contains no messages"
for path in messages:
    raw=path.read_bytes()
    message=BytesParser(policy=policy.default).parsebytes(raw)
    record=message.get_payload(decode=True)
    assert record.startswith(b"wtmail1."),(path.name,record[:32])
    encoded=record[len(b"wtmail1."):]
    sealed=base64.urlsafe_b64decode(encoded+b"="*((4-len(encoded)%4)%4))
    assert len(sealed)>28,(path.name,len(sealed))
    visible=raw+record+sealed
    for value in forbidden+["fixture-smtp-user","fixture-smtp-pass","fixture-imap-user","fixture-imap-pass","session.offer","session.answer","session_id","tunnel.open","GET /"]:
        assert value.encode() not in visible,(path.name,value)
json.dump({"messages":len(messages),"wtmail1Records":len(messages),"decodedPlaintextLeaks":0},open(out,"w",encoding="utf-8"),separators=(",",":"))
PY

SMTP_STORES="$(nsenter -t "$MAIL_HOLDER" -m -- grep -c '"event":"smtp_store"' "$MAIL_STATE/events.jsonl" || true)"
IMAP_FETCHES="$(nsenter -t "$MAIL_HOLDER" -m -- grep -c '"event":"imap_fetch"' "$MAIL_STATE/events.jsonl" || true)"
MAIL_MESSAGES="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["messages"])' "$WT_RUN_DIR/mail-audit.json")"
[[ "$SMTP_STORES" -gt 0 && "$IMAP_FETCHES" -gt 0 && "$MAIL_MESSAGES" -gt 0 ]]

python3 - "$RESULT_FILE" "$INTERRUPTION_MS" "$MAIL_MESSAGES" "$SMTP_STORES" "$IMAP_FETCHES" "$CALIBRATED_PACKETS" <<'PY'
import json,sys
path,interruption,messages,stores,fetches,calibrated=sys.argv[1:]
result={
 "test":"carrier-mail-file-failover","exit":0,"controlRoute":"mail.control","primaryRoute":"mail.primary","backupRoute":"file.backup",
 "primaryNonceValid":True,"backupNonceValid":True,"recoveredNonceValid":True,"directTargetReachable":False,
 "directTripwireCalibratedPackets":int(calibrated),"directTripwirePostResetPackets":0,"targetSourcesOnlyNode":True,
 "openStreamInterrupted":True,"interruptionMs":int(interruption),"mailRecovered":True,"automaticFailbackToMail":True,
 "daemonRestarted":False,"processIdentityUnchanged":True,"configUnchanged":True,"manualSelectionUsed":False,
 "mountSurfacesIsolated":True,"privateMailSpool":True,"decodedPlaintextInMail":False,"wtmail1Records":int(messages),
 "backupTrafficClassesOnlyEgress":True,"mailMessages":int(messages),"smtpStores":int(stores),"imapFetches":int(fetches),
}
json.dump(result,open(path,"w",encoding="utf-8"),separators=(",",":"))
PY
SUCCESS=true
