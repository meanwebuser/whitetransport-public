#!/usr/bin/env bash
set -euo pipefail

# Wails runs frontend:install from apps/native-gui/frontend. Keep dependency
# authority at the repository root: apps/client-web is a workspace and does
# not own a lockfile. This check is intentionally read-only and archive-safe.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NATIVE_GUI_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
REPO_ROOT="$(cd "$NATIVE_GUI_DIR/../.." && pwd)"

node - "$REPO_ROOT" <<'NODE'
const fs = require('node:fs');
const path = require('node:path');

const repoRoot = path.resolve(process.argv[2]);
const readJson = (relativePath) => {
  const absolutePath = path.join(repoRoot, relativePath);
  if (!fs.existsSync(absolutePath) || !fs.statSync(absolutePath).isFile()) {
    throw new Error(`missing required manifest: ${relativePath}`);
  }
  try {
    return JSON.parse(fs.readFileSync(absolutePath, 'utf8'));
  } catch (error) {
    throw new Error(`invalid JSON manifest: ${relativePath}: ${error.message}`);
  }
};

const rootPackage = readJson('package.json');
const rootLock = readJson('package-lock.json');
const wailsConfig = readJson('apps/native-gui/wails.json');
const clientPackage = readJson('apps/client-web/package.json');
const clientLock = path.join(repoRoot, 'apps/client-web/package-lock.json');

if (!Array.isArray(rootPackage.workspaces) || !rootPackage.workspaces.includes('apps/client-web')) {
  throw new Error('root package.json must declare apps/client-web as an npm workspace');
}
if (rootLock.lockfileVersion !== 3 || !rootLock.packages?.['apps/client-web']) {
  throw new Error('root package-lock.json must be lockfile v3 and contain the apps/client-web workspace');
}
if (fs.existsSync(clientLock)) {
  throw new Error('apps/client-web must not gain a nested package-lock.json');
}

const installCommand = wailsConfig['frontend:install'];
if (typeof installCommand !== 'string' || !installCommand.includes('npm --prefix ../../.. ci')) {
  throw new Error('Wails frontend:install must run npm ci from the repository root via --prefix ../../..');
}
if (/npm(?:\s+--prefix\s+)?\s+ci\s+--prefix\s+apps\/client-web/.test(installCommand)
    || /npm\s+--prefix\s+apps\/client-web\s+ci/.test(installCommand)) {
  throw new Error('Wails frontend:install must not run npm ci with apps/client-web as its prefix');
}
if (typeof clientPackage.name !== 'string' || clientPackage.name.length === 0) {
  throw new Error('apps/client-web/package.json must remain a valid workspace manifest');
}

console.log('macOS Wails dependency preflight: root package-lock + workspace contract OK');
NODE
