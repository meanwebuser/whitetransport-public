#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
PACK_SCRIPT="$SCRIPT_DIR/post-build-pack.sh"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/wt-client-bundle-contract.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT

APP_PATH="$TEST_ROOT/WhiteTransport.app"
SOURCE_STORE="$TEST_ROOT/generated-client-token-store.json"
OPAQUE_FILE="$TEST_ROOT/club123"
OPAQUE_FILE_2="$TEST_ROOT/club456"
OPAQUE_METADATA="$TEST_ROOT/vk-tokens.json"
OPAQUE_SERVER_STORE="$TEST_ROOT/server-token-store.json"
SINGBOX_SOURCE="$TEST_ROOT/sing-box"
BOOTSTRAP_SECRET_SOURCE="$TEST_ROOT/bootstrap.secret"
mkdir -p "$APP_PATH/Contents/MacOS"

cat >"$SOURCE_STORE" <<'JSON'
{
  "tokens": [{"id":"vk-client-fixture","platform":"vk","kind":"api_key","lifecycle":"embedded","status":"active","value":"synthetic-client-token"}],
  "bindings": [{"token_id":"vk-client-fixture","platform":"vk","connection_type":"messages","channel_id":"fixture-peer","role":"discovery","priority":10,"enabled":true}]
}
JSON
cat >"$SINGBOX_SOURCE" <<'SH'
#!/usr/bin/env bash
exit 0
SH
chmod 755 "$SINGBOX_SOURCE"
printf 'fixture-bootstrap-secret\n' >"$BOOTSTRAP_SECRET_SOURCE"
printf 'synthetic-opaque-client-token\n' >"$OPAQUE_FILE"
printf 'synthetic-opaque-client-token-2\n' >"$OPAQUE_FILE_2"
cat >"$OPAQUE_METADATA" <<'JSON'
{"accounts":[{"id":"club123","channels":[{"peer_id":"fixture-peer","role":"discovery"},{"peer_id":"fixture-control","role":"node-client"}]},{"id":"club456","channels":[{"peer_id":"fixture-peer","role":"discovery"},{"peer_id":"fixture-control-2","role":"node-client"}]}]}
JSON
cat >"$OPAQUE_SERVER_STORE" <<'JSON'
{"tokens":[],"bindings":[]}
JSON

# A credentialed source-only/client package must accept an explicit generated
# client TokenStore and emit both bundle resources and a config pointer.
if ! WT_CLIENT_BOOTSTRAP_FILES="$SOURCE_STORE" WT_NATIVE_GUI_SING_BOX="$SINGBOX_SOURCE" \
  WT_BOOTSTRAP_KEY_V2=1 WT_BOOTSTRAP_SECRET_FILE="$BOOTSTRAP_SECRET_SOURCE" \
  bash "$PACK_SCRIPT" "$APP_PATH" >/dev/null 2>&1; then
  echo "post-build-pack rejected explicit WT_CLIENT_BOOTSTRAP_FILES" >&2
  exit 1
fi
test -s "$APP_PATH/Contents/MacOS/resources/token-store.json"
test -s "$APP_PATH/Contents/Resources/daemon.json"
python3 - "$APP_PATH/Contents/Resources/daemon.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    config = json.load(handle)
if config.get("role") != "client":
    raise SystemExit("post-build daemon config must use role=client")
token_store = config.get("token_store")
if not isinstance(token_store, dict) or not token_store.get("tokens") or not token_store.get("bindings"):
    raise SystemExit("post-build daemon config must embed a non-empty client TokenStore")
if "token_store_path" in config:
    raise SystemExit("post-build daemon config must not use token_store_path")
if "bootstrap_secret" not in config:
    raise SystemExit("post-build daemon config must preserve explicit bootstrap_secret")
PY

if WT_CLIENT_BOOTSTRAP_FILES="$SOURCE_STORE" WT_NATIVE_GUI_SING_BOX="$SINGBOX_SOURCE" \
  WT_BOOTSTRAP_KEY_V2=1 bash "$PACK_SCRIPT" "$TEST_ROOT/MissingBootstrapSecret.app" >/dev/null 2>&1; then
  echo "post-build-pack accepted v2 without an explicit bootstrap secret" >&2
  exit 1
fi

OPAQUE_APP="$TEST_ROOT/OpaqueFiles.app"
WT_CLIENT_BOOTSTRAP_FILES="$OPAQUE_FILE:$OPAQUE_FILE_2" \
  WT_CLIENT_BOOTSTRAP_METADATA="$OPAQUE_METADATA" \
  WT_CLIENT_BOOTSTRAP_SERVER_STORE="$OPAQUE_SERVER_STORE" \
  WT_NATIVE_GUI_SING_BOX="$SINGBOX_SOURCE" \
  bash "$PACK_SCRIPT" "$OPAQUE_APP" >/dev/null
grep -Fq 'vk-client-bootstrap-club123' "$OPAQUE_APP/Contents/MacOS/resources/token-store.json"
grep -Fq 'vk-client-bootstrap-club456' "$OPAQUE_APP/Contents/MacOS/resources/token-store.json"
python3 - "$OPAQUE_APP/Contents/Resources/daemon.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    config = json.load(handle)
store = config["token_store"]
if len(store["tokens"]) != 2 or len(store["bindings"]) != 4:
    raise SystemExit(f"opaque two-file projection counts are wrong: {len(store['tokens'])}/{len(store['bindings'])}")
roles = {binding["role"] for binding in store["bindings"]}
if roles != {"discovery", "node-client"}:
    raise SystemExit(f"opaque projection contains unexpected roles: {roles}")
PY

# A credentialed candidate with no explicit store must fail rather than fetch
# ambient production secrets. Provisioning-only is the sole no-credentials
# exception and must leave no token-store artifact behind.
if WT_NATIVE_GUI_SING_BOX="$SINGBOX_SOURCE" bash "$PACK_SCRIPT" "$TEST_ROOT/MissingStore.app" >/dev/null 2>&1; then
  echo "post-build-pack accepted credentialed package without explicit TokenStore" >&2
  exit 1
fi
PROVISIONING_APP="$TEST_ROOT/ProvisioningOnly.app"
WT_NATIVE_GUI_PROVISIONING_ONLY=1 WT_NATIVE_GUI_SING_BOX="$SINGBOX_SOURCE" \
  bash "$PACK_SCRIPT" "$PROVISIONING_APP" >/dev/null
test ! -e "$PROVISIONING_APP/Contents/MacOS/resources/token-store.json"

echo "post-build client bundle contract: OK"
