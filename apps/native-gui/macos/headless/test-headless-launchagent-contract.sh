#!/bin/bash
set -euo pipefail
root=$(cd "$(dirname "$0")" && pwd)
bash -n "$root/whitetransportd-launchagent-runner.sh" "$root/install-whitetransport-launchagent.sh" "$root/uninstall-whitetransport-launchagent.sh"
grep -Fq -- '--serve --instance' "$root/whitetransportd-launchagent-runner.sh"
grep -Fq -- '/v1/session/connect' "$root/whitetransportd-launchagent-runner.sh"
grep -Fq -- 'KeepAlive' "$root/install-whitetransport-launchagent.sh"
grep -Fq -- 'RunAtLoad' "$root/install-whitetransport-launchagent.sh"
grep -Fq -- 'launchctl bootstrap' "$root/install-whitetransport-launchagent.sh"
grep -Fq -- 'launchctl bootout' "$root/uninstall-whitetransport-launchagent.sh"
grep -Fq -- 'instance=${4:-whitetransport-macos-background}' "$root/whitetransportd-launchagent-runner.sh"
grep -Fq -- 'http://127\.0\.0\.1' "$root/whitetransportd-launchagent-runner.sh"
! grep -Fq -- 'daemon.stdout.log' "$root/whitetransportd-launchagent-runner.sh"
grep -Fq -- 'StandardOutPath</key><string>/dev/null' "$root/install-whitetransport-launchagent.sh"
grep -Fq -- 'xml_escape' "$root/install-whitetransport-launchagent.sh"
bash "$root/test-m1-socks-forward-contract.sh"
echo 'headless LaunchAgent contract: PASS'
