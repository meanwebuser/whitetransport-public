import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  STANDARD_DISCOVERY_CARRIERS,
  createControlEnvelope,
  createDiscoveryEnvelope,
  createVkDiscoveryBusLane,
  discoveryCarrierSupports,
} from '../dist/index.js';

test('standard discovery carriers expose VK and OK as encrypted bus carriers', () => {
  const carriers = new Map(STANDARD_DISCOVERY_CARRIERS.map((carrier) => [carrier.identity.id, carrier]));

  assert.equal(carriers.get('vk-text')?.encrypted, true);
  assert.equal(carriers.get('ok-graph-messages')?.encrypted, true);
  assert.equal(discoveryCarrierSupports(carriers.get('vk-text'), 'room.announce'), true);
  assert.equal(discoveryCarrierSupports(carriers.get('ok-graph-messages'), 'client.log'), true);
});

test('discovery envelope wraps high-level control messages without provider branching', () => {
  const control = createControlEnvelope({
    id: 'room-msg-1',
    kind: 'room_state',
    createdAt: 1710000000000,
    source: { id: 'example-exit-node', role: 'creator', platform: 'server' },
    body: {
      roomId: 'wbstream://room-1',
      revision: 1,
      state: 'ready',
      endpoints: [],
      providers: [],
    },
  });
  const discovery = createDiscoveryEnvelope({
    id: 'discovery-1',
    kind: 'room.announce',
    createdAt: 1710000000001,
    source: { nodeId: 'example-exit-node', actorId: 'creator-1', role: 'creator' },
    control,
  });

  assert.equal(discovery.kind, 'room.announce');
  assert.equal(discovery.control.kind, 'room_state');
  assert.equal(discovery.source.nodeId, 'example-exit-node');
});

test('VK bus lanes model multiple chats without transport-provider branching', () => {
  const lanes = ['c1log', 'c2log', 'c3log', 'c4log'].map((name, index) =>
    createVkDiscoveryBusLane({ name, peerId: String(2000000001 + index), role: 'log' }),
  );

  assert.deepEqual(lanes.map((lane) => lane.id), ['vk-log-c1log', 'vk-log-c2log', 'vk-log-c3log', 'vk-log-c4log']);
  assert.equal(lanes.every((lane) => lane.carrierId === 'vk-text'), true);
  assert.equal(lanes.every((lane) => lane.capabilities.includes('client-log')), true);
});
