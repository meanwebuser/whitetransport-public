#!/usr/bin/env bash
# Simple test to verify enhanced configs
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
CORE_GO="$REPO_ROOT/core/go"
CONFIG_DEV="$REPO_ROOT/config/dev"

echo "SCRIPT_DIR: $SCRIPT_DIR"
echo "REPO_ROOT: $REPO_ROOT"
echo "CORE_GO: $CORE_GO"
echo "CONFIG_DEV: $CONFIG_DEV"

echo "Checking if directories exist:"
echo "  CORE_GO exists: $([ -d "$CORE_GO" ] && echo 'yes' || echo 'no')"
echo "  CONFIG_DEV exists: $([ -d "$CONFIG_DEV" ] && echo 'yes' || echo 'no')"

echo "Checking config files:"
echo "  local-node-enhanced.json: $([ -f "$CONFIG_DEV/local-node-enhanced.json" ] && echo 'yes' || echo 'no')"
echo "  local-client-enhanced.json: $([ -f "$CONFIG_DEV/local-client-enhanced.json" ] && echo 'yes' || echo 'no')"

echo "Checking Go build:"
cd "$CORE_GO"
echo "  Current directory: $(pwd)"
echo "  go.mod exists: $([ -f "go.mod" ] && echo 'yes' || echo 'no')"
echo "  cmd/whitetransportd exists: $([ -f "cmd/whitetransportd" ] && echo 'yes' || echo 'no')"