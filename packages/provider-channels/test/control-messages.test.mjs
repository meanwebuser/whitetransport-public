import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  createControlEnvelope,
  decodeControlPayload,
  encodeControlPayload,
  publishControlEnvelope,
  readControlEnvelopes,
} from '../dist/index.js';

test('control message codec round-trips room state envelopes', () => {
  const envelope = createControlEnvelope({
    id: 'msg-1',
    kind: 'room_state',
    createdAt: 1710000000000,
    source: { id: 'creator-1', role: 'creator', platform: 'server' },
    body: {
      roomId: 'room-1',
      revision: 7,
      state: 'ready',
      endpoints: [{
        id: 'wb-room-1',
        providerId: 'whitelist-bypass-wbstream',
        protocol: 'wb-tunnel',
        url: 'wbstream://room-1',
      }],
      providers: [{
        providerId: 'whitelist-bypass-wbstream',
        health: { state: 'healthy', latencyMs: 50 },
        priority: 1,
      }],
    },
  });

  const payload = encodeControlPayload(envelope, {
    expiresAt: 1710000600000,
    metadata: { topic: 'room-discovery' },
  });
  const decoded = decodeControlPayload(payload);

  assert.equal(payload.kind, 'room_state');
  assert.equal(payload.metadata['control.id'], 'msg-1');
  assert.equal(payload.metadata.topic, 'room-discovery');
  assert.deepEqual(decoded, envelope);
});

test('control message codec rejects kind mismatches', () => {
  const envelope = createControlEnvelope({
    id: 'msg-2',
    kind: 'client_feedback',
    createdAt: 1710000000000,
    source: { id: 'ios-1', role: 'client', platform: 'ios' },
    body: {
      clientId: 'ios-1',
      severity: 'warning',
      code: 'provider_failed',
      message: 'provider timed out',
      providerId: 'vk',
      observedAt: 1710000000000,
    },
  });
  const payload = {
    ...encodeControlPayload(envelope),
    kind: 'admin_command',
  };

  assert.throws(() => decodeControlPayload(payload), /kind mismatch/);
});

test('control bus publishes and reads admin commands across channels', async () => {
  const channels = [
    createMemoryChannel('memory-a'),
    createMemoryChannel('memory-b'),
  ];
  const envelope = createControlEnvelope({
    id: 'command-msg-1',
    kind: 'admin_command',
    createdAt: 1710000000000,
    source: { id: 'admin-1', role: 'admin', platform: 'web' },
    body: {
      commandId: 'command-1',
      action: 'refresh_discovery',
      targetId: 'room-1',
    },
  });

  const published = await publishControlEnvelope({ channels, envelope, minSuccesses: 2 });
  const read = await readControlEnvelopes({ channels, kind: 'admin_command' });

  assert.equal(published.published.length, 2);
  assert.equal(read.announcements.length, 2);
  assert.deepEqual(read.announcements.map((item) => item.envelope.body.commandId), ['command-1', 'command-1']);
});

function createMemoryChannel(id) {
  const messages = [];

  return {
    identity: {
      id,
      kind: 'memory',
      label: id,
      direction: 'duplex',
      encoding: 'text',
    },
    budget: {
      maxPayloadBytes: 65536,
      sendsPerMinute: 600,
    },
    async getHealth() {
      return { state: 'healthy' };
    },
    async publish(payload) {
      const remoteId = `${id}-${messages.length + 1}`;
      const acceptedAt = Date.now();
      messages.push({ providerId: id, remoteId, acceptedAt, payload });
      return { providerId: id, remoteId, acceptedAt };
    },
    async read(cursor) {
      const offset = cursor ? Number(cursor.value) : 0;
      return {
        messages: messages.slice(offset),
        nextCursor: { value: String(messages.length) },
      };
    },
  };
}
