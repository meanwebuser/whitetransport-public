import assert from 'node:assert/strict';
import test from 'node:test';

import { parseSocksPortFromRuntimeConfig } from './android-socks-probe.ts';

test('Android SOCKS probe follows the listener embedded in the APK runtime config', () => {
  const port = parseSocksPortFromRuntimeConfig(JSON.stringify({ socks_listen: '127.0.0.1:1085' }));
  assert.equal(port, 1085);
});
