import { describe, expect, test } from '@jest/globals';
import type { ChannelPayload, ProviderChannel } from '@whitetransport/provider-channels';
import { DEFAULT_BUDGET } from '../scheduler/budget';
import { LegacyProviderChannelAdapter } from './channel-contract';
import { MemoryProvider } from './memory';
import {
  RoomStatePublishError,
  publishRoomState,
  readRoomStates,
} from './room-discovery';

function createMemoryChannel(id: string): ProviderChannel {
  return new LegacyProviderChannelAdapter(new MemoryProvider(), {
    id,
    kind: 'memory',
    label: id,
    budget: DEFAULT_BUDGET,
  });
}

describe('room discovery provider bus', () => {
  test('publishes and reads room state across provider channels', async () => {
    const channels = [createMemoryChannel('memory-a'), createMemoryChannel('memory-b')];
    const publishResult = await publishRoomState({
      channels,
      minSuccesses: 2,
      id: 'room-msg-1',
      source: { id: 'creator-1', role: 'creator', platform: 'server' },
      roomId: 'room-1',
      revision: 1,
      state: 'ready',
      endpoints: [{
        id: 'wb-room-1',
        providerId: 'whitelist-bypass-wbstream',
        protocol: 'wb-tunnel',
        url: 'wbstream://room-1',
      }],
    });
    const readResult = await readRoomStates({ channels });

    expect(publishResult.published).toHaveLength(2);
    expect(publishResult.failures).toHaveLength(0);
    expect(readResult.announcements).toHaveLength(2);
    expect(readResult.announcements.map((entry) => entry.envelope.body.roomId)).toEqual(['room-1', 'room-1']);
    expect(readResult.failures).toHaveLength(0);
  });

  test('reports provider failures without losing successful room publishes', async () => {
    const healthy = createMemoryChannel('healthy');
    const failing: ProviderChannel = {
      identity: { id: 'broken', kind: 'memory', label: 'broken', direction: 'duplex', encoding: 'text' },
      budget: { maxPayloadBytes: 1024, sendsPerMinute: 1 },
      async getHealth() {
        return { state: 'offline', failureReason: 'test channel' };
      },
      async publish(_payload: ChannelPayload) {
        throw new Error('publish failed');
      },
      async read() {
        return { messages: [] };
      },
    };

    const result = await publishRoomState({
      channels: [healthy, failing],
      id: 'room-msg-2',
      source: { id: 'creator-1', role: 'creator' },
      roomId: 'room-2',
      revision: 1,
      state: 'ready',
      endpoints: [],
    });

    expect(result.published).toHaveLength(1);
    expect(result.failures).toHaveLength(1);
    expect(result.failures[0].providerId).toBe('broken');
  });

  test('throws when quorum requirement is not met', async () => {
    const healthy = createMemoryChannel('healthy');
    const failing: ProviderChannel = {
      identity: { id: 'broken', kind: 'memory', label: 'broken', direction: 'duplex', encoding: 'text' },
      budget: { maxPayloadBytes: 1024, sendsPerMinute: 1 },
      async getHealth() {
        return { state: 'offline' };
      },
      async publish(_payload: ChannelPayload) {
        throw new Error('publish failed');
      },
      async read() {
        return { messages: [] };
      },
    };

    await expect(publishRoomState({
      channels: [healthy, failing],
      minSuccesses: 2,
      id: 'room-msg-3',
      source: { id: 'creator-1', role: 'creator' },
      roomId: 'room-3',
      revision: 1,
      state: 'ready',
      endpoints: [],
    })).rejects.toBeInstanceOf(RoomStatePublishError);
  });
});
