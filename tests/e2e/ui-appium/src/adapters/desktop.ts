import path from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../../../..');

/**
 * Desktop (Electron) WDIO capabilities.
 *
 * Environment variables are inherited by the Electron process from the
 * parent WDIO runner.  For payload tests the runner script (run-payload.sh)
 * sets WT_VK_TOKEN, WT_OK_TOKEN, WT_DESKTOP_RUNTIME_MODE=whitetransportd,
 * and WT_DAEMON_BIN so the Go daemon starts with real carriers.
 */
export function desktopCapabilities(): Record<string, unknown> {
  const electronEntry = process.env.WT_E2E_DESKTOP_ENTRY
    ? path.resolve(process.env.WT_E2E_DESKTOP_ENTRY)
    : path.resolve(repoRoot, 'apps/desktop/dist/electron-main.js');
  return {
    browserName: 'electron',
    browserVersion: process.env.WT_E2E_ELECTRON_VERSION ?? '31.7.7',
    'wdio:electronServiceOptions': {
      appEntryPoint: electronEntry,
      appArgs: ['--no-sandbox'],
    },
    'goog:chromeOptions': {
      binary: process.env.WT_E2E_ELECTRON_BIN
        ? path.resolve(process.env.WT_E2E_ELECTRON_BIN)
        : path.resolve(repoRoot, 'node_modules/.bin/electron'),
    },
  };
}
