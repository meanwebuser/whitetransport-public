import { describe, expect, test } from '@jest/globals';
import type { ChannelPayload, ProviderChannel } from '@whitetransport/provider-channels';
import { createControlEnvelope } from '@whitetransport/provider-channels';
import { DEFAULT_BUDGET } from '../scheduler/budget';
import { LegacyProviderChannelAdapter } from './channel-contract';
import {
  ProviderControlPublishError,
  publishControlEnvelope,
  readControlEnvelopes,
} from './control-bus';
import { MemoryProvider } from './memory';

function createMemoryChannel(id: string): ProviderChannel {
  return new LegacyProviderChannelAdapter(new MemoryProvider(), {
    id,
    kind: 'memory',
    label: id,
    budget: DEFAULT_BUDGET,
  });
}

function createFeedbackEnvelope() {
  return createControlEnvelope({
    id: 'feedback-1',
    kind: 'client_feedback',
    createdAt: 1710000000000,
    source: { id: 'ios-1', role: 'client', platform: 'ios' },
    body: {
      clientId: 'ios-1',
      severity: 'warning',
      code: 'provider_failed',
      message: 'provider timed out',
      providerId: 'vk-1',
      observedAt: 1710000000000,
    },
  });
}

describe('provider control bus', () => {
  test('publishes and reads filtered control envelopes across channels', async () => {
    const channels = [createMemoryChannel('memory-a'), createMemoryChannel('memory-b')];
    const envelope = createFeedbackEnvelope();

    const publishResult = await publishControlEnvelope({ channels, envelope, minSuccesses: 2 });
    const readResult = await readControlEnvelopes({ channels, kind: 'client_feedback' });

    expect(publishResult.published).toHaveLength(2);
    expect(readResult.announcements).toHaveLength(2);
    expect(readResult.announcements[0].envelope.body.clientId).toBe('ios-1');
    expect(Object.keys(readResult.cursors).sort()).toEqual(['memory-a', 'memory-b']);
  });

  test('reports publish failures and enforces minimum successes', async () => {
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

    await expect(publishControlEnvelope({
      channels: [createMemoryChannel('healthy'), failing],
      envelope: createFeedbackEnvelope(),
      minSuccesses: 2,
    })).rejects.toBeInstanceOf(ProviderControlPublishError);
  });
});
