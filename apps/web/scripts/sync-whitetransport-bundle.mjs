import { copyFile, mkdir } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const rootDir = resolve(dirname(fileURLToPath(import.meta.url)), '../../..');
const targetDir = resolve(rootDir, 'apps/web/public/whitetransport');

function runBrowserBuild() {
  const result = spawnSync('npm', ['--prefix', 'packages/browser-transport', 'run', 'build'], {
    cwd: rootDir,
    stdio: 'inherit',
  });
  if (result.status !== 0) {
    throw new Error(`browser-transport build failed with status ${result.status ?? 'unknown'}`);
  }
}

await mkdir(targetDir, { recursive: true });
runBrowserBuild();

await Promise.all([
  copyFile(
    resolve(rootDir, 'packages/browser-transport/dist/whitetransport-wb.js'),
    resolve(targetDir, 'whitetransport-wb.js'),
  ),
  copyFile(
    resolve(rootDir, 'packages/browser-transport/dist/whitetransport-wb.js.map'),
    resolve(targetDir, 'whitetransport-wb.js.map'),
  ),
]);

console.info(`[web] synced WhiteTransport browser bundle into ${targetDir}`);
