import { androidCapabilities } from './src/adapters/android.js';
import { desktopCapabilities } from './src/adapters/desktop.js';

const platform = process.env.WT_E2E_PLATFORM ?? 'desktop';

function capabilities(): unknown[] {
  if (platform === 'android') return [androidCapabilities()];
  if (platform === 'desktop') return [desktopCapabilities()];
  throw new Error(`Unsupported WT_E2E_PLATFORM=${platform}`);
}

export const config = {
  runner: 'local',
  specs: ['./src/specs/**/*.spec.ts'],
  maxInstances: 1,
  logLevel: 'info',
  bail: 0,
  waitforTimeout: Number(process.env.WT_E2E_WAIT_MS ?? '30000'),
  connectionRetryTimeout: 120000,
  connectionRetryCount: 1,
  framework: 'mocha',
  reporters: ['spec'],
  mochaOpts: {
    timeout: Number(process.env.WT_E2E_TEST_TIMEOUT_MS ?? '180000'),
  },
  services: platform === 'android' ? ['appium'] : ['electron'],
  capabilities: capabilities(),
};
