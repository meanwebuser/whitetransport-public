import assert from 'node:assert/strict';
import test from 'node:test';

import { androidTextMatches } from './android-labels.ts';

test('Android selector pattern accepts localized WhiteTransport labels', () => {
  assert.equal(
    androidTextMatches(['WhiteTransport', 'WHITETRANSPORT']),
    '^(?:WhiteTransport|WHITETRANSPORT)$',
  );
  assert.equal(
    androidTextMatches(['Connect WhiteTransport', 'Подключиться']),
    '^(?:Connect WhiteTransport|Подключиться)$',
  );
  assert.equal(
    androidTextMatches(['Disconnect WhiteTransport', 'Отключить']),
    '^(?:Disconnect WhiteTransport|Отключить)$',
  );
});
