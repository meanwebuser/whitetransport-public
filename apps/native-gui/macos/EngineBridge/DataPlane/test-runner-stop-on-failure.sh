#!/usr/bin/env bash
set -euo pipefail

# Compile runner.c against a tiny local C-ABI stub so this contract runs on Linux
# without a Darwin framework or any product engine implementation.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/wt-c-abi-runner-contract.XXXXXX")"
trap 'rm -rf "$BUILD_DIR"' EXIT

cat > "$BUILD_DIR/WhiteTransportTun2Socks.h" <<'EOF'
#include <stdint.h>
int32_t WTStartTun2Socks(int32_t fd, int32_t mtu, int32_t socks_port);
int32_t WTStopTun2Socks(void);
char *WTLastError(void);
void WTFreeCString(char *value);
EOF

cat > "$BUILD_DIR/stub.c" <<'EOF'
#include <stdint.h>
#include <stdlib.h>
#include <unistd.h>
#include <stdio.h>
int32_t WTStartTun2Socks(int32_t fd, int32_t mtu, int32_t socks_port) {
    (void)mtu; (void)socks_port;
    close(fd);
    return 0;
}
int32_t WTStopTun2Socks(void) {
    fputs("stop_called\n", stderr);
    return 0;
}
char *WTLastError(void) { return NULL; }
void WTFreeCString(char *value) { free(value); }
EOF

cat > "$BUILD_DIR/failing-client.sh" <<'EOF'
#!/usr/bin/env bash
exit 7
EOF
chmod +x "$BUILD_DIR/failing-client.sh"

cc -std=c11 -Wall -Wextra -Werror \
  -I "$BUILD_DIR" "$SCRIPT_DIR/runner.c" "$BUILD_DIR/stub.c" \
  -o "$BUILD_DIR/runner"
set +e
WT_TEST_ROUNDS=1 "$BUILD_DIR/runner" "$BUILD_DIR/failing-client.sh" 1085 > "$BUILD_DIR/runner.log" 2>&1
status=$?
set -e
test "$status" -eq 7
grep -q '^stop_called$' "$BUILD_DIR/runner.log"
grep -q 'child_failure' "$BUILD_DIR/runner.log"
grep -q 'descriptor=.*ebadf' "$BUILD_DIR/runner.log"
echo "runner stop-on-child-failure contract: OK"
