#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <acceptance.json>" >&2
  exit 64
fi

python3 - "$1" <<'PY'
import hashlib
import ipaddress
import json
import pathlib
import re
import sys

evidence_path = pathlib.Path(sys.argv[1]).resolve()
root = evidence_path.parent

def fail(message: str) -> None:
    raise SystemExit(f"macOS off-host acceptance: FAIL: {message}")

def need(condition: bool, message: str) -> None:
    if not condition:
        fail(message)

def load_json(path_value: str, label: str) -> dict:
    path = pathlib.Path(path_value)
    if not path.is_absolute():
        path = root / path
    need(path.is_file(), f"missing {label}: {path}")
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        fail(f"invalid {label}: {exc}")
    need(isinstance(value, dict), f"{label} must be an object")
    return value

def file_sha256(path_value: str, label: str) -> None:
    path = pathlib.Path(path_value)
    if not path.is_absolute():
        path = root / path
    need(path.is_file(), f"missing {label}: {path}")
    expected = bundle[label + "Sha256"]
    actual = hashlib.sha256(path.read_bytes()).hexdigest()
    need(re.fullmatch(r"[0-9a-f]{64}", expected or "") is not None, f"invalid {label} hash")
    need(actual == expected, f"{label} hash mismatch")

data = load_json(str(evidence_path), "acceptance artifact")
need(data.get("schema") == 1, "unsupported schema")
need(data.get("passed") is True, "artifact passed=false")
need(data.get("proofBoundary") == "macos-offhost-system-route", "wrong proof boundary")
need(re.fullmatch(r"[0-9a-f]{40}", data.get("sourceCommit", "")) is not None, "source commit identity is missing")

bundle = data.get("bundle")
need(isinstance(bundle, dict) and bundle.get("structuralVerifier") == "pass", "bundle structural verification is missing")
for required in ("executablePath", "executableSha256", "daemonPath", "daemonSha256", "singBoxPath", "singBoxSha256"):
    need(required in bundle, f"bundle field missing: {required}")
file_sha256(bundle["executablePath"], "executable")
file_sha256(bundle["daemonPath"], "daemon")
file_sha256(bundle["singBoxPath"], "singBox")

topology = data.get("topology")
need(isinstance(topology, dict), "topology is missing")
need(topology.get("sameHost") is False, "client and node must be off-host")
need(topology.get("nodeId"), "node id is missing")
try:
    direct_ip = ipaddress.ip_address(topology["macDirectIp"])
    exit_ip = ipaddress.ip_address(topology["expectedNodeExitIp"])
except (KeyError, ValueError):
    fail("topology IPs are invalid")
need(direct_ip != exit_ip, "direct and expected node IPs must differ")

remote = data.get("remote")
need(isinstance(remote, dict) and remote.get("passed") is True, "remote GUI result did not pass")
need(remote.get("proofBoundary") == "system-route", "remote proof boundary is not system-route")
need(remote.get("targetNodeId") == topology["nodeId"], "remote node does not match topology")
need(remote.get("systemVpnState") == "connected", "system VPN was not connected")
need(remote.get("systemRouteProbeRequested") is True and remote.get("systemRouteProbePassed") is True, "system route probe was not proven")
need(remote.get("systemRouteIp") == topology["expectedNodeExitIp"], "system route IP does not match node exit IP")

snapshots = data.get("snapshots")
need(isinstance(snapshots, dict), "route snapshots are missing")
before = load_json(snapshots.get("before", ""), "before snapshot")
during = load_json(snapshots.get("during", ""), "during snapshot")
after = load_json(snapshots.get("after", ""), "after snapshot")
need(before.get("routeFingerprint") == after.get("routeFingerprint"), "route fingerprint was not restored")
need(len(during.get("tunnelInterfaces", [])) > 0, "during snapshot has no tunnel interface")
need(len(during.get("ownedRoutes", [])) > 0, "during snapshot has no owned routes")
need(len(during.get("ownedProcesses", [])) > 0, "during snapshot has no owned processes")
need(len(during.get("ownedListeners", [])) > 0, "during snapshot has no owned listeners")
for field in ("tunnelInterfaces", "ownedRoutes", "ownedProcesses", "ownedListeners"):
    need(len(after.get(field, [])) == 0, f"cleanup left {field}")

node = data.get("nodeEvidence")
need(isinstance(node, dict) and node.get("matchedLines", 0) > 0, "node-side evidence is missing")
node_log_path = pathlib.Path(node.get("logPath", ""))
if not node_log_path.is_absolute():
    node_log_path = root / node_log_path
need(node_log_path.is_file(), f"missing node log: {node_log_path}")
node_log = node_log_path.read_text(encoding="utf-8", errors="replace")
need(node.get("sessionId", "") in node_log, "node log does not contain the matching session")
need(node.get("egressTarget", "") in node_log, "node log does not contain the matching egress target")
need(node.get("releaseObserved") is True, "node release was not observed")

cleanup = data.get("cleanup")
need(isinstance(cleanup, dict) and cleanup.get("passed") is True, "cleanup proof is missing")
need(cleanup.get("routeFingerprintRestored") is True, "cleanup did not restore route fingerprint")
for field in ("tunnelInterfacesAfter", "ownedRoutesAfter", "ownedProcessesAfter", "ownedListenersAfter"):
    need(cleanup.get(field) == 0, f"cleanup field is nonzero: {field}")
need(cleanup.get("nodeReleaseObserved") is True, "cleanup lacks node release")

print("macOS off-host acceptance: PASS")
PY
