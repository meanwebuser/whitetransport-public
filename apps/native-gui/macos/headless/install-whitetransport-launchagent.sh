#!/bin/bash
set -euo pipefail

daemon=
config=
label=com.meanwebuser.whitetransport.headless
state_dir=
no_bootstrap=false

usage() { echo "usage: $0 --daemon PATH --config PATH [--label LABEL] [--state-dir PATH] [--no-bootstrap]" >&2; }
while [[ $# -gt 0 ]]; do
  case "$1" in
    --daemon) [[ $# -ge 2 ]] || { usage; exit 64; }; daemon=$2; shift 2 ;;
    --config) [[ $# -ge 2 ]] || { usage; exit 64; }; config=$2; shift 2 ;;
    --label) [[ $# -ge 2 ]] || { usage; exit 64; }; label=$2; shift 2 ;;
    --state-dir) [[ $# -ge 2 ]] || { usage; exit 64; }; state_dir=$2; shift 2 ;;
    --no-bootstrap) no_bootstrap=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage; exit 64 ;;
  esac
done

[[ -n "$daemon" && -n "$config" ]] || { usage; exit 64; }
[[ "$daemon" = /* && "$config" = /* ]] || { echo "absolute daemon/config paths required" >&2; exit 64; }
[[ "$label" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "invalid label" >&2; exit 64; }
[[ -x "$daemon" && -f "$config" ]] || { echo "daemon/config missing" >&2; exit 78; }
[[ "$(stat -f '%Lp' "$config")" == 600 ]] || { echo "config must be mode 0600" >&2; exit 78; }

xml_escape() {
  printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's/"/\&quot;/g'
}

uid=$(id -u)
home=$(printenv HOME)
[[ -n "$home" ]] || { echo "HOME is required" >&2; exit 78; }
root="$home/Library/Application Support/WhiteTransport/headless"
if [[ -z "$state_dir" ]]; then state_dir="$root/state"; fi
plist_dir="$home/Library/LaunchAgents"
plist="$plist_dir/$label.plist"
runner_src=$(cd "$(dirname "$0")" && pwd)/whitetransportd-launchagent-runner.sh
runner="$root/whitetransportd-launchagent-runner.sh"
daemon_xml=$(xml_escape "$daemon")
config_xml=$(xml_escape "$config")
state_dir_xml=$(xml_escape "$state_dir")
runner_xml=$(xml_escape "$runner")

install -d -m 700 "$root" "$state_dir" "$plist_dir"
install -m 700 "$runner_src" "$runner"
cat >"$plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>$label</string>
<key>ProgramArguments</key><array>
<string>$runner_xml</string><string>$daemon_xml</string><string>$config_xml</string><string>$state_dir_xml</string><string>whitetransport-macos-background</string>
</array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
<key>ProcessType</key><string>Background</string>
<key>ThrottleInterval</key><integer>5</integer>
<key>LowPriorityIO</key><true/>
<key>StandardOutPath</key><string>/dev/null</string>
<key>StandardErrorPath</key><string>$state_dir_xml/launchd.stderr.log</string>
</dict></plist>
PLIST
chmod 600 "$plist"
printf 'label=%s\\ndaemon=%s\\nconfig=%s\\nplist=%s\\n' "$label" "$daemon" "$config" "$plist" >"$state_dir/install-receipt.env"
chmod 600 "$state_dir/install-receipt.env"

if [[ "$no_bootstrap" == true ]]; then echo "installed plist: $plist"; exit 0; fi
domain="gui/$uid"
launchctl bootout "$domain/$label" >/dev/null 2>&1 || true
launchctl bootstrap "$domain" "$plist"
launchctl kickstart -k "$domain/$label"
launchctl print "$domain/$label" >/dev/null
echo "installed and loaded: $label"
