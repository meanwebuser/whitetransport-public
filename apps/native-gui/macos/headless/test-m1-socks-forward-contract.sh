#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")" && pwd)
script="$root/forward-m1-socks.sh"
bash -n "$script"
repo_root=$(cd "$root/../../../.." && pwd)
mkdir -p "$repo_root/.tmp"
test_root=$(mktemp -d "$repo_root/.tmp/m1-forward-contract.XXXXXX")
trap 'rm -rf "$test_root"' EXIT
cat > "$test_root/ssh" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAKE_SSH_TRACE"
printf 'running\tconnected\t127.0.0.1:40123\t2026-11-16T16:30:00Z\n'
EOF
chmod 700 "$test_root/ssh"
: > "$test_root/id_rsa"
: > "$test_root/known_hosts"
FAKE_SSH_TRACE="$test_root/trace" PATH="$test_root:$PATH" "$script" \
  --listen 1099 --identity-file "$test_root/id_rsa" --known-hosts "$test_root/known_hosts" \
  > "$test_root/output"
grep -Fq -- '127.0.0.1:1099' "$test_root/output"
grep -Fq -- '-L 127.0.0.1:1099:127.0.0.1:40123' "$test_root/trace"
grep -Fq -- 'ExitOnForwardFailure=yes' "$test_root/trace"
if PATH="$test_root:$PATH" "$script" --remote-home '/Users/user;touch' --identity-file "$test_root/id_rsa" --known-hosts "$test_root/known_hosts" >/dev/null 2>&1; then
  printf 'remote-home injection guard failed\n' >&2
  exit 1
fi
grep -Fq -- 'status.env' "$script"
grep -Fq -- 'v1/status' "$script"
grep -Fq -- '--noproxy' "$script"
grep -Fq -- 'daemon_state' "$script"
grep -Fq -- 'connect_state' "$script"
grep -Fq -- '127\.0\.0\.1' "$script"
grep -Fq -- 'ProxyJump' "$script"
grep -Fq -- '-L "127.0.0.1:' "$script"
! grep -Fq -- '0.0.0.0' "$script"
echo 'M1 SOCKS forward contract: PASS'
