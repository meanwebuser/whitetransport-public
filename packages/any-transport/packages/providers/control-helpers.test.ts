import { describe, expect, test } from '@jest/globals';
import type { ProviderChannel } from '@whitetransport/provider-channels';
import { DEFAULT_BUDGET } from '../scheduler/budget';
import { LegacyProviderChannelAdapter } from './channel-contract';
import {
  publishAdminCommand,
  publishClientFeedback,
  readAdminCommands,
  readClientFeedback,
} from './control-helpers';
import { MemoryProvider } from './memory';

function createMemoryChannel(id: string): ProviderChannel {
  return new LegacyProviderChannelAdapter(new MemoryProvider(), {
    id,
    kind: 'memory',
    label: id,
    budget: DEFAULT_BUDGET,
  });
}

describe('control helpers', () => {
  test('publishes and reads client feedback envelopes', async () => {
    const channels = [createMemoryChannel('memory-a')];

    await publishClientFeedback({
      channels,
      id: 'feedback-1',
      source: { id: 'ios-1', role: 'client', platform: 'ios' },
      clientId: 'ios-1',
      severity: 'warning',
      code: 'provider_failed',
      message: 'provider timed out',
      providerId: 'vk-1',
      observedAt: 1710000000000,
    });
    const result = await readClientFeedback(channels);

    expect(result.announcements).toHaveLength(1);
    expect(result.announcements[0].envelope.body.code).toBe('provider_failed');
    expect(result.announcements[0].envelope.body.providerId).toBe('vk-1');
  });

  test('publishes and reads admin command envelopes separately', async () => {
    const channels = [createMemoryChannel('memory-a')];

    await publishClientFeedback({
      channels,
      id: 'feedback-2',
      source: { id: 'android-1', role: 'client' },
      clientId: 'android-1',
      severity: 'info',
      code: 'connected',
      message: 'connected',
    });
    await publishAdminCommand({
      channels,
      id: 'command-msg-1',
      source: { id: 'admin-1', role: 'admin', platform: 'web' },
      commandId: 'command-1',
      action: 'refresh_discovery',
      targetId: 'room-1',
    });
    const result = await readAdminCommands(channels);

    expect(result.announcements).toHaveLength(1);
    expect(result.announcements[0].envelope.body.action).toBe('refresh_discovery');
    expect(result.announcements[0].envelope.body.targetId).toBe('room-1');
  });
});
