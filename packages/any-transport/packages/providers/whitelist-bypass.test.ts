import { describe, expect, test } from '@jest/globals';
import type { ByteDuplex } from '@whitetransport/provider-channels';
import {
  WhitelistBypassTransport,
  assertWhitelistBypassEndpoint,
  createWhitelistBypassEndpoint,
} from './whitelist-bypass';

function createDuplex(): ByteDuplex {
  return {
    async write(_chunk: Uint8Array): Promise<void> {},
    async read(): Promise<Uint8Array | null> {
      return null;
    },
    async close(): Promise<void> {},
  };
}

describe('WhitelistBypassTransport', () => {
  test('creates a shared wb-tunnel endpoint shape', () => {
    const endpoint = createWhitelistBypassEndpoint({
      providerId: 'wb',
      roomUrl: 'wbstream://room',
      tunnelMode: 'video',
      role: 'creator',
    });

    expect(endpoint).toEqual({
      id: 'wb:wbstream-room',
      providerId: 'wb',
      protocol: 'wb-tunnel',
      url: 'wbstream://room',
      metadata: {
        tunnelMode: 'video',
        role: 'creator',
        carrier: 'wbstream',
      },
    });
  });

  test('delegates connect to an injected WBStream connector', async () => {
    const duplex = createDuplex();
    const transport = new WhitelistBypassTransport({
      id: 'wb',
      roomUrl: 'wbstream://room',
      connector: async (endpoint, context) => {
        expect(endpoint.url).toBe('wbstream://room');
        expect(context.identity.kind).toBe('whitelist-bypass');
        return duplex;
      },
    });

    await expect(transport.connect()).resolves.toBe(duplex);
    await expect(transport.getHealth()).resolves.toMatchObject({ state: 'healthy' });
  });

  test('fails explicitly when no WBStream connector is wired', async () => {
    const transport = new WhitelistBypassTransport({ id: 'wb', roomUrl: 'wbstream://room' });

    await expect(transport.connect()).rejects.toThrow('WBStream adapter is not wired');
    await expect(transport.getHealth()).resolves.toMatchObject({ state: 'offline' });
  });

  test('validates wb-tunnel endpoints before connector usage', () => {
    expect(() => assertWhitelistBypassEndpoint({
      id: 'bad',
      providerId: 'wb',
      protocol: 'wisp',
      url: 'wbstream://room',
    })).toThrow('Expected wb-tunnel endpoint');
  });
});
