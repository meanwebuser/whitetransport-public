import { describe, expect, test } from '@jest/globals';
import type {
  ByteDuplex,
  ProviderHealth,
  ProviderIdentity,
  StreamTransportChannel,
  TransportEndpoint,
} from '@whitetransport/provider-channels';
import { StreamTransportDialer } from './stream-router';

function createDuplex(label: string): ByteDuplex {
  return {
    async write(_chunk: Uint8Array): Promise<void> {},
    async read(): Promise<Uint8Array | null> {
      return new TextEncoder().encode(label);
    },
    async close(): Promise<void> {},
  };
}

function createEndpoint(providerId: string, protocol: TransportEndpoint['protocol'] = 'wb-tunnel'): TransportEndpoint {
  return {
    id: `${providerId}:endpoint`,
    providerId,
    protocol,
    url: `${protocol}://${providerId}`,
  };
}

function createChannel(options: {
  id: string;
  health: ProviderHealth;
  stream?: ByteDuplex;
  connectError?: Error;
}): StreamTransportChannel {
  const identity: ProviderIdentity = {
    id: options.id,
    kind: options.id.startsWith('wb') ? 'whitelist-bypass' : 'memory',
    label: options.id,
    direction: 'duplex',
    encoding: 'stream',
  };

  return {
    identity,
    budget: { maxPayloadBytes: 65536, sendsPerMinute: 60 },
    async getHealth() {
      return options.health;
    },
    async connect() {
      if (options.connectError) throw options.connectError;
      return options.stream ?? createDuplex(options.id);
    },
  };
}

describe('StreamTransportDialer', () => {
  test('selects the lowest-priority usable stream route', async () => {
    const wb = createChannel({ id: 'wb', health: { state: 'healthy', latencyMs: 50 }, stream: createDuplex('wb') });
    const ytp = createChannel({ id: 'ytp', health: { state: 'healthy', latencyMs: 10 }, stream: createDuplex('ytp') });
    const dialer = new StreamTransportDialer([
      { channel: ytp, endpoint: createEndpoint('ytp', 'socks5'), priority: 20 },
      { channel: wb, endpoint: createEndpoint('wb'), priority: 10 },
    ]);

    const result = await dialer.connect();

    expect(result.route.channel.identity.id).toBe('wb');
    expect(result.attempted).toEqual(['wb']);
  });

  test('falls back to the next stream route when connect fails', async () => {
    const wb = createChannel({
      id: 'wb',
      health: { state: 'healthy', latencyMs: 10 },
      connectError: new Error('room died'),
    });
    const ytp = createChannel({ id: 'ytp', health: { state: 'degraded', latencyMs: 100 }, stream: createDuplex('ytp') });
    const dialer = new StreamTransportDialer([
      { channel: wb, endpoint: createEndpoint('wb'), priority: 1 },
      { channel: ytp, endpoint: createEndpoint('ytp', 'socks5'), priority: 2 },
    ]);

    const result = await dialer.connect();

    expect(result.route.channel.identity.id).toBe('ytp');
    expect(result.attempted).toEqual(['wb', 'ytp']);
    expect(result.failures).toEqual([
      { providerId: 'wb', endpointId: 'wb:endpoint', reason: 'room died' },
    ]);
  });

  test('can filter routes by protocol', async () => {
    const wb = createChannel({ id: 'wb', health: { state: 'healthy' } });
    const ytp = createChannel({ id: 'ytp', health: { state: 'healthy' } });
    const dialer = new StreamTransportDialer([
      { channel: wb, endpoint: createEndpoint('wb', 'wb-tunnel'), priority: 1 },
      { channel: ytp, endpoint: createEndpoint('ytp', 'socks5'), priority: 2 },
    ]);

    const result = await dialer.connect({ protocol: 'socks5' });

    expect(result.route.channel.identity.id).toBe('ytp');
    expect(result.attempted).toEqual(['ytp']);
  });

  test('reports health failures when no route can be used', async () => {
    const wb = createChannel({ id: 'wb', health: { state: 'offline', failureReason: 'no room' } });
    const dialer = new StreamTransportDialer([{ channel: wb, endpoint: createEndpoint('wb'), priority: 1 }]);

    await expect(dialer.connect()).rejects.toMatchObject({
      name: 'StreamDialError',
      attempted: [],
      failures: [{ providerId: 'wb', endpointId: 'wb:endpoint', reason: 'no room' }],
    });
  });
});
