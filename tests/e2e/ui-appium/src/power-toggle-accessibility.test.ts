import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import { whiteTransportToggleLabel } from '../../../../apps/client-web/src/lib/runtime-accessibility.ts';

test('power toggle exposes stable native connect and disconnect actions', () => {
  assert.equal(whiteTransportToggleLabel(false), 'Connect WhiteTransport');
  assert.equal(whiteTransportToggleLabel(true), 'Disconnect WhiteTransport');
});

test('client power button wires the dynamic accessibility action', async () => {
  const sourceUrl = new URL('../../../../apps/client-web/src/components/vpn/client-view.tsx', import.meta.url);
  const source = await readFile(sourceUrl, 'utf8');

  assert.match(source, /data-testid="wt-connect-toggle"\s+aria-label=\{whiteTransportToggleLabel\(isOnline\)\}/);
});
