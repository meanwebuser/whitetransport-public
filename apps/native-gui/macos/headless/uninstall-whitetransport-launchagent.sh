#!/bin/bash
set -euo pipefail
label=com.meanwebuser.whitetransport.headless
if [[ $# -gt 0 ]]; then label=$1; fi
[[ "$label" =~ ^[A-Za-z0-9._-]+$ ]] || exit 64
launchctl bootout "gui/$(id -u)/$label" >/dev/null 2>&1 || true
rm -f "$HOME/Library/LaunchAgents/$label.plist"
echo "uninstalled: $label"
