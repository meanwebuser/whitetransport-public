#!/usr/bin/env bash
set -Eeuo pipefail

# Build a self-contained Linux unpacked package without touching the shared
# Wails build tree. AppImage conversion is intentionally a separate concern.
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
GUI_BINARY="${WT_UBUNTU_GUI_BINARY:-}"
DAEMON_BINARY="${WT_UBUNTU_DAEMON_BINARY:-}"
DAEMON_CONFIG="${WT_UBUNTU_DAEMON_CONFIG:-}"
SING_BOX_BINARY="${WT_UBUNTU_SING_BOX_BINARY:-}"
TOKEN_STORE="${WT_UBUNTU_TOKEN_STORE:-}"
CLIENT_BOOTSTRAP_FILES="${WT_CLIENT_BOOTSTRAP_FILES:-}"
OUTPUT_DIR="${WT_UBUNTU_PACKAGE_OUTPUT:-$ROOT_DIR/artifacts/ubuntu-release/WhiteTransport-ubuntu-x64-unpacked}"
SOURCE_COMMIT="${WT_UBUNTU_SOURCE_COMMIT:-$(git -C "$ROOT_DIR" rev-parse HEAD)}"
STATE_FILE_OVERRIDE="${WT_UBUNTU_STATE_FILE:-}"
LISTEN_API_OVERRIDE="${WT_UBUNTU_LISTEN_API:-}"
SOCKS_LISTEN_OVERRIDE="${WT_UBUNTU_SOCKS_LISTEN:-}"

die() { echo "package-ubuntu: $*" >&2; exit 1; }
require_file() { local label="$1" path="$2"; [[ -f "$path" ]] || die "$label is missing: $path"; }
require_executable() { local label="$1" path="$2"; require_file "$label" "$path"; [[ -x "$path" ]] || die "$label is not executable: $path"; }

[[ "$(uname -m)" == "x86_64" ]] || die "Ubuntu package host must be x86_64"
[[ "$SOURCE_COMMIT" =~ ^[0-9a-f]{40}$ ]] || die "source commit must be a full Git SHA"
require_executable "GUI binary" "$GUI_BINARY"
require_executable "daemon binary" "$DAEMON_BINARY"
require_executable "sing-box binary" "$SING_BOX_BINARY"
if [[ -n "$TOKEN_STORE" && -n "$CLIENT_BOOTSTRAP_FILES" ]]; then
  die "choose one of WT_UBUNTU_TOKEN_STORE or WT_CLIENT_BOOTSTRAP_FILES"
fi
if [[ -z "$TOKEN_STORE" && -z "$CLIENT_BOOTSTRAP_FILES" ]]; then
  die "client TokenStore source is required"
fi
if [[ -n "$TOKEN_STORE" ]]; then require_file "TokenStore" "$TOKEN_STORE"; fi
if [[ -n "$DAEMON_CONFIG" ]]; then require_file "daemon config" "$DAEMON_CONFIG"; fi

if [[ -d "$OUTPUT_DIR" ]] && find "$OUTPUT_DIR" -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
  die "output directory is not empty: $OUTPUT_DIR"
fi
mkdir -p "$OUTPUT_DIR/resources"

install -m 0755 "$GUI_BINARY" "$OUTPUT_DIR/WhiteTransport"
install -m 0755 "$DAEMON_BINARY" "$OUTPUT_DIR/whitetransportd-linux-x64"
install -m 0755 "$SING_BOX_BINARY" "$OUTPUT_DIR/sing-box-linux-x64"
install -m 0755 "$DAEMON_BINARY" "$OUTPUT_DIR/whitetransportd"
install -m 0755 "$SING_BOX_BINARY" "$OUTPUT_DIR/sing-box"
install -m 0755 "$DAEMON_BINARY" "$OUTPUT_DIR/resources/whitetransportd"
install -m 0755 "$SING_BOX_BINARY" "$OUTPUT_DIR/resources/sing-box"
if [[ -n "$CLIENT_BOOTSTRAP_FILES" ]]; then
  python3 "$ROOT_DIR/ops/config/merge-client-token-stores.py" \
    "$CLIENT_BOOTSTRAP_FILES" "$OUTPUT_DIR/resources/token-store.json" >/dev/null
else
  python3 "$ROOT_DIR/ops/config/filter-client-token-store.py" \
    "$TOKEN_STORE" "$OUTPUT_DIR/resources/token-store.json" >/dev/null
fi

python3 - "$OUTPUT_DIR/resources/token-store.json" "$OUTPUT_DIR/resources/daemon.json" "$DAEMON_CONFIG" "$STATE_FILE_OVERRIDE" "$LISTEN_API_OVERRIDE" "$SOCKS_LISTEN_OVERRIDE" <<'PY'
import json, sys
from pathlib import Path
store = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
destination = Path(sys.argv[2])
config = json.loads(Path(sys.argv[3]).read_text(encoding="utf-8")) if sys.argv[3] else {"role": "client"}
config["role"] = "client"
config["token_store"] = store
for key, value in (("state_file", sys.argv[4]), ("listen_api", sys.argv[5]), ("socks_listen", sys.argv[6])):
    if value: config[key] = value
destination.write_text(json.dumps(config, indent=2) + "\n", encoding="utf-8")
destination.chmod(0o600)
PY

python3 - "$OUTPUT_DIR" "$SOURCE_COMMIT" <<'PY'
import hashlib, json, subprocess, sys
from pathlib import Path
root, source_commit = Path(sys.argv[1]), sys.argv[2]
def digest(path): return hashlib.sha256(path.read_bytes()).hexdigest()
def version(path):
    try:
        completed = subprocess.run([str(path), "--version"], text=True, capture_output=True, timeout=15)
        output = (completed.stdout or completed.stderr).strip()
        if completed.returncode != 0 and path.name.startswith("sing-box"):
            completed = subprocess.run([str(path), "version"], text=True, capture_output=True, timeout=15)
            output = (completed.stdout or completed.stderr).strip()
    except (OSError, subprocess.SubprocessError): return "unavailable"
    return output or "unavailable"
artifacts = []
for relative in ("WhiteTransport", "whitetransportd-linux-x64", "sing-box-linux-x64", "whitetransportd", "sing-box", "resources/whitetransportd", "resources/sing-box", "resources/token-store.json", "resources/daemon.json"):
    path = root / relative
    artifacts.append({"path": relative, "sha256": digest(path), "size": path.stat().st_size})
runtime_store = json.loads((root / "resources/token-store.json").read_text(encoding="utf-8"))
manifest = {"schemaVersion": 1, "platform": "ubuntu-x64", "packageFormat": "unpacked", "sourceCommit": source_commit,
  "gui": {"path": "WhiteTransport", "version": version(root / "WhiteTransport")},
  "daemon": {"path": "whitetransportd-linux-x64", "version": version(root / "whitetransportd-linux-x64")},
  "singBox": {"path": "sing-box-linux-x64", "version": version(root / "sing-box-linux-x64")},
  "tokenStore": {"path": "resources/token-store.json", "tokenCount": len(runtime_store.get("tokens", [])), "bindingCount": len(runtime_store.get("bindings", [])), "sha256": digest(root / "resources/token-store.json")},
  "artifacts": artifacts}
(root / "package-manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
print(json.dumps({"output": str(root), "sourceCommit": source_commit, "tokenCount": len(runtime_store.get("tokens", [])), "bindingCount": len(runtime_store.get("bindings", []))}))
PY

sha256sum "$OUTPUT_DIR/WhiteTransport" "$OUTPUT_DIR/whitetransportd-linux-x64" "$OUTPUT_DIR/sing-box-linux-x64" >"$OUTPUT_DIR/SHA256SUMS"
echo "package-ubuntu: unpacked bundle ready at $OUTPUT_DIR"
