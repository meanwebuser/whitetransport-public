import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createControlEnvelope } from '@whitetransport/provider-channels';
import { MemoryVideoConferenceAdapter } from '../dist/index.js';

function createConfig(providerId = 'vk-video-vp8') {
  return {
    schemaVersion: 1,
    providerId,
    platform: providerId.startsWith('telemost') ? 'telemost' : 'vk',
    mode: providerId.endsWith('dualstream') ? 'dualstream' : 'vp8',
    carrier: providerId.endsWith('dualstream') ? 'dualstream' : 'vp8',
    runtimeKind: 'adapter',
    role: 'joiner',
    roomSource: { kind: 'existing-room-url', roomUrl: 'https://example.test/room' },
    browserHookMode: providerId.startsWith('telemost') ? 'inject-telemost-hook' : 'inject-vk-hook',
    pionRelay: { enabled: true, listenHost: '127.0.0.1', listenPort: 9001, iceTransportPolicy: 'relay' },
    vp8: { fps: 24, batch: 30, trackCount: 1, maxPacketBytes: 1200 },
    audio: { enabled: false },
  };
}

test('fake adapter creates a room, opens a stream, and echoes in-memory bytes', async () => {
  const adapter = new MemoryVideoConferenceAdapter({ now: () => 1000 });
  const config = createConfig();
  const room = await adapter.createRoom({ config, roomId: 'room-a' });
  const stream = await adapter.openStream({ config, room });

  await stream.duplex.write(new Uint8Array([1, 2, 3]));

  assert.equal(room.status, 'ready');
  assert.deepEqual(await stream.duplex.read(), new Uint8Array([1, 2, 3]));
  assert.equal((await adapter.getRuntimeStatus()).activeStreamIds.length, 1);
});

test('fake adapter accepts typed control messages without provider credentials', async () => {
  const adapter = new MemoryVideoConferenceAdapter();
  const envelope = createControlEnvelope({
    id: 'cmd-1',
    kind: 'admin_command',
    createdAt: 1000,
    source: { id: 'admin', role: 'admin' },
    body: { commandId: 'cmd-1', action: 'refresh_discovery' },
  });

  await adapter.sendControlMessage({ envelope });

  assert.equal(adapter.getControlMessages()[0]?.id, 'cmd-1');
});
