import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  createDataChannelByteDuplex,
  createWhitelistBypassByteDuplexConnector,
} from '../src/whitelist-bypass-byte-duplex.js';

class FakeDataChannel {
  constructor() {
    this.binaryType = '';
    this.readyState = 'connecting';
    this.sent = [];
    this.listeners = new Map();
  }

  addEventListener(type, listener, options = {}) {
    const listeners = this.listeners.get(type) || [];
    listeners.push({ listener, once: Boolean(options.once) });
    this.listeners.set(type, listeners);
  }

  removeEventListener(type, listener) {
    const listeners = this.listeners.get(type) || [];
    this.listeners.set(type, listeners.filter((entry) => entry.listener !== listener));
  }

  send(data) {
    this.sent.push(data);
  }

  open() {
    this.readyState = 'open';
    this.dispatch('open', { type: 'open' });
  }

  receive(data) {
    this.dispatch('message', { type: 'message', data });
  }

  close() {
    this.readyState = 'closed';
    this.dispatch('close', { type: 'close', reason: 'fake close' });
  }

  dispatch(type, event) {
    const listeners = [...(this.listeners.get(type) || [])];
    for (const entry of listeners) {
      entry.listener(event);
      if (entry.once) this.removeEventListener(type, entry.listener);
    }
  }
}

function asArray(data) {
  return [...new Uint8Array(data.buffer || data, data.byteOffset || 0, data.byteLength || data.length)];
}

async function waitForAdapterAttach() {
  await Promise.resolve();
}

test('DataChannelByteDuplex reads queued inbound bytes', async () => {
  const dataChannel = new FakeDataChannel();
  const duplex = createDataChannelByteDuplex(dataChannel);

  await waitForAdapterAttach();
  dataChannel.open();
  dataChannel.receive(new Uint8Array([1, 2, 3]).buffer);

  assert.deepEqual(asArray(await duplex.read()), [1, 2, 3]);
});

test('DataChannelByteDuplex waits for open before writing', async () => {
  const dataChannel = new FakeDataChannel();
  const duplex = createDataChannelByteDuplex(dataChannel);
  const writePromise = duplex.write(new Uint8Array([4, 5, 6]));

  await waitForAdapterAttach();
  assert.equal(dataChannel.sent.length, 0);
  dataChannel.open();
  await writePromise;

  assert.deepEqual(asArray(dataChannel.sent[0]), [4, 5, 6]);
});

test('DataChannelByteDuplex resolves pending reads with null on clean close', async () => {
  const dataChannel = new FakeDataChannel();
  const duplex = createDataChannelByteDuplex(dataChannel);
  const readPromise = duplex.read();

  await waitForAdapterAttach();
  dataChannel.open();
  dataChannel.close();

  assert.equal(await readPromise, null);
});

test('DataChannelByteDuplex close does not wait for delayed channel factory', async () => {
  const dataChannel = new FakeDataChannel();
  let resolveDataChannel = null;
  const dataChannelPromise = new Promise((resolve) => {
    resolveDataChannel = resolve;
  });
  const duplex = createDataChannelByteDuplex(() => dataChannelPromise);

  await duplex.close();
  resolveDataChannel(dataChannel);
  await waitForAdapterAttach();

  assert.equal(dataChannel.readyState, 'closed');
  assert.equal(await duplex.read(), null);
});

test('WhitelistBypass connector passes endpoint room and returns ByteDuplex', async () => {
  const dataChannel = new FakeDataChannel();
  let receivedOptions = null;
  const connector = createWhitelistBypassByteDuplexConnector({
    displayName: 'browser-test',
    createDataChannel: async (options) => {
      receivedOptions = options;
      return dataChannel;
    },
  });

  const endpoint = {
    id: 'test-endpoint',
    providerId: 'wb',
    protocol: 'wb-tunnel',
    url: 'wbstream://room-1',
    metadata: { tunnelMode: 'data-channel' },
  };
  const duplex = await connector(endpoint, {
    identity: { id: 'wb', kind: 'whitelist-bypass', label: 'WB', direction: 'duplex', encoding: 'stream' },
    budget: { maxPayloadBytes: 65536, sendsPerMinute: 120 },
  });

  await waitForAdapterAttach();
  dataChannel.open();
  await duplex.write(new Uint8Array([9]));

  assert.equal(receivedOptions.room, 'wbstream://room-1');
  assert.equal(receivedOptions.displayName, 'browser-test');
  assert.deepEqual(asArray(dataChannel.sent[0]), [9]);
});

test('WhitelistBypass connector rejects non-WB endpoints explicitly', async () => {
  const connector = createWhitelistBypassByteDuplexConnector({
    createDataChannel: async () => new FakeDataChannel(),
  });

  await assert.rejects(
    () => connector({ id: 'bad', providerId: 'x', protocol: 'wisp' }, {}),
    /Expected wb-tunnel endpoint/,
  );
});
