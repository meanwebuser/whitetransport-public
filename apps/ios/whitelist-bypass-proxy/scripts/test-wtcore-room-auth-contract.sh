#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
runtime="$root/whitelist-bypass-proxy/WTCoreRoomFirstRuntime.swift"
plugin="$root/whitelist-bypass-proxy/WtTransportPlugin.swift"
project="$root/whitelist-bypass-proxy.xcodeproj/project.pbxproj"
builder="$root/scripts/build-wtcore-framework.sh"
ios_builder="$(cd "$root/../../.." && pwd)/ops/build/mac-ios-build.sh"

test -f "$runtime"
test -f "$builder"
test -f "$ios_builder"
rg -F 'import WTCore' "$runtime"
rg -F 'MobileStartTransportWithLocalSession' "$runtime"
rg -F 'wt-runtime-config' "$runtime"
rg -F 'startIfProvisioned' "$plugin"
rg -F 'WTCore.xcframework' "$project"
rg -F 'gomobile bind -target=ios' "$builder"
rg -F 'LOCAL_IOS_WTCORE_FRAMEWORK' "$ios_builder"
rg -F 'tar -C "$(dirname "$LOCAL_IOS_WTCORE_FRAMEWORK")"' "$ios_builder"
