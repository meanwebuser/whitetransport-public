import { describe, expect, test } from '@jest/globals';
import type { ChannelPayload } from '@whitetransport/provider-channels';
import { DEFAULT_BUDGET } from '../scheduler/budget';
import {
  LegacyProviderChannelAdapter,
  decodeChannelPayloadFromLegacyProvider,
  encodeChannelPayloadForLegacyProvider,
} from './channel-contract';
import { MemoryProvider } from './memory';

function createPayload(body: Uint8Array = new Uint8Array([1, 2, 3])): ChannelPayload {
  return {
    kind: 'room_state',
    createdAt: 1710000000000,
    expiresAt: 1710000600000,
    body,
    metadata: { topic: 'room-discovery' },
  };
}

describe('LegacyProviderChannelAdapter', () => {
  test('round-trips shared payloads through legacy text providers', async () => {
    const provider = new MemoryProvider();
    const adapter = new LegacyProviderChannelAdapter(provider, {
      id: 'memory-control',
      kind: 'memory',
      label: 'memory control channel',
      budget: DEFAULT_BUDGET,
    });
    const payload = createPayload();

    const published = await adapter.publish(payload);
    const read = await adapter.read();

    expect(published.providerId).toBe('memory-control');
    expect(published.remoteId).toBe('1');
    expect(read.nextCursor).toEqual({ value: '1' });
    expect(read.messages).toHaveLength(1);
    expect(read.messages[0].payload).toEqual(payload);
    await expect(adapter.getHealth()).resolves.toMatchObject({ state: 'healthy' });
  });

  test('skips unrelated mailbox text while preserving prefixed envelopes', async () => {
    const provider = new MemoryProvider();
    await provider.append({ text: 'operator note', priority: 0, deadline: 1710000000000 });

    const adapter = new LegacyProviderChannelAdapter(provider, {
      kind: 'memory',
      budget: DEFAULT_BUDGET,
    });
    await adapter.publish(createPayload(new Uint8Array([9])));

    const read = await adapter.read();

    expect(read.messages).toHaveLength(1);
    expect(read.messages[0].payload.body).toEqual(new Uint8Array([9]));
  });

  test('encodes and decodes WTPC1 wire envelopes explicitly', () => {
    const payload = createPayload(new Uint8Array([4, 5, 6]));
    const wireText = encodeChannelPayloadForLegacyProvider(payload);

    expect(wireText.startsWith('WTPC1.')).toBe(true);
    expect(decodeChannelPayloadFromLegacyProvider(wireText)).toEqual(payload);
    expect(() => decodeChannelPayloadFromLegacyProvider('plain text')).toThrow('WTPC1 envelope prefix');
  });
});
