import assert from 'node:assert/strict';
import test from 'node:test';

import { resolveRuntimeConnectTarget } from '../../../../apps/client-web/src/lib/runtime-connection.ts';

test('Capacitor delegates node selection when pre-connect discovery is empty', () => {
  assert.deepEqual(resolveRuntimeConnectTarget({ capacitorHost: true, knownServerIds: [] }), { kind: 'runtime' });
});

test('ordinary web keeps empty node discovery unavailable', () => {
  assert.deepEqual(resolveRuntimeConnectTarget({ capacitorHost: false, knownServerIds: [] }), { kind: 'unavailable' });
});

test('an explicit server remains preferred on every host', () => {
  assert.deepEqual(
    resolveRuntimeConnectTarget({ capacitorHost: true, selectedServerId: 'node-1', onlineServerId: 'node-2', knownServerIds: ['node-1', 'node-2'] }),
    { kind: 'server', serverId: 'node-1' },
  );
});

test('Capacitor ignores a stale preferred node when discovery is empty', () => {
  assert.deepEqual(
    resolveRuntimeConnectTarget({ capacitorHost: true, preferredServerId: 'stale-node', knownServerIds: [] }),
    { kind: 'runtime' },
  );
});
