import assert from 'node:assert/strict';
import test from 'node:test';

import { androidCapabilities } from './adapters/android.ts';

test('Android session always installs the exact supplied APK', () => {
  const capabilities = androidCapabilities();

  assert.equal(capabilities['appium:enforceAppInstall'], true);
  assert.equal(capabilities['appium:appActivity'], '.CapacitorMainActivity');
  assert.equal(capabilities['appium:forceAppLaunch'], true);
  assert.equal(capabilities['appium:shouldTerminateApp'], true);
});
