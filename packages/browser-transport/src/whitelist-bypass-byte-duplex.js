import { createWbStreamLiveKitDataChannel } from './wbstream-livekit-joiner.js';

/**
 * @typedef {import('@whitetransport/provider-channels').ByteDuplex} ByteDuplex
 * @typedef {import('@whitetransport/provider-channels').TransportEndpoint} TransportEndpoint
 * @typedef {import('@whitetransport/provider-channels').ProviderIdentity} ProviderIdentity
 * @typedef {import('@whitetransport/provider-channels').ProviderBudget} ProviderBudget
 */

/**
 * @typedef {object} WhitelistBypassConnectorContext
 * @property {ProviderIdentity} identity
 * @property {ProviderBudget} budget
 */

/**
 * @typedef {object} DataChannelByteDuplexOptions
 * @property {number} [maxQueuedChunks]
 */

/**
 * @typedef {object} WhitelistBypassByteDuplexConnectorOptions
 * @property {(options: object) => Promise<object>|object} [createDataChannel]
 * @property {DataChannelByteDuplexOptions} [duplexOptions]
 * @property {string} [displayName]
 * @property {typeof fetch} [fetchImpl]
 * @property {string} [accessToken]
 * @property {object} [livekitOptions]
 * @property {(status: object) => void} [onStatus]
 * @property {string} [creatorIdentity]
 */

const DEFAULT_MAX_QUEUED_CHUNKS = 1024;

function toError(error) {
  return error instanceof Error ? error : new Error(String(error));
}

function assertUint8Array(chunk) {
  if (!(chunk instanceof Uint8Array)) {
    throw new TypeError('ByteDuplex.write requires Uint8Array chunks');
  }
}

function normalizeBytes(data) {
  if (data instanceof Uint8Array) return data;
  if (data instanceof ArrayBuffer) return new Uint8Array(data);
  if (data?.buffer instanceof ArrayBuffer) return new Uint8Array(data.buffer, data.byteOffset || 0, data.byteLength);
  return new TextEncoder().encode(String(data));
}

function copyBytes(data) {
  const bytes = normalizeBytes(data);
  return new Uint8Array(bytes);
}

function assertDataChannel(dataChannel) {
  if (!dataChannel || typeof dataChannel.send !== 'function') {
    throw new Error('WBStream connector must return an RTCDataChannel-like object with send()');
  }
  if (typeof dataChannel.addEventListener !== 'function') {
    throw new Error('WBStream connector must return an EventTarget-like data channel');
  }
}

function addListener(target, type, listener, options) {
  target.addEventListener(type, listener, options);
  return () => target.removeEventListener?.(type, listener, options);
}

/**
 * Browser DataChannel adapter that implements the shared ByteDuplex contract.
 */
export class DataChannelByteDuplex {
  #channel = null;
  #channelPromise;
  #cleanup = [];
  #closed = false;
  #error = null;
  #opened = false;
  #openPromise;
  #queue = [];
  #readers = [];
  #rejectOpen = () => {};
  #resolveOpen = () => {};

  /**
   * @param {Promise<object>|object|(() => Promise<object>|object)} dataChannelPromiseOrFactory
   * @param {DataChannelByteDuplexOptions} options
   */
  constructor(dataChannelPromiseOrFactory, { maxQueuedChunks = DEFAULT_MAX_QUEUED_CHUNKS } = {}) {
    this.maxQueuedChunks = maxQueuedChunks;
    this.#openPromise = new Promise((resolve, reject) => {
      this.#resolveOpen = resolve;
      this.#rejectOpen = reject;
    });
    this.#openPromise.catch(() => undefined);

    const dataChannel = typeof dataChannelPromiseOrFactory === 'function'
      ? dataChannelPromiseOrFactory()
      : dataChannelPromiseOrFactory;
    this.#channelPromise = Promise.resolve(dataChannel).then((resolvedDataChannel) => {
      this.#attach(resolvedDataChannel);
      return resolvedDataChannel;
    }).catch((error) => {
      this.#fail(error);
      throw error;
    });
    this.#channelPromise.catch(() => undefined);
  }

  /**
   * Writes one binary chunk to the data channel.
   *
   * @param {Uint8Array} chunk
   * @returns {Promise<void>}
   */
  async write(chunk) {
    assertUint8Array(chunk);
    await this.#openPromise;
    if (this.#closed) throw new Error('WBStream ByteDuplex is closed');
    if (this.#error) throw this.#error;
    const dataChannel = await this.#channelPromise;
    dataChannel.send(new Uint8Array(chunk));
  }

  /**
   * Reads the next binary chunk from the data channel.
   *
   * @returns {Promise<Uint8Array|null>}
   */
  async read() {
    if (this.#queue.length > 0) return this.#queue.shift();
    if (this.#error) throw this.#error;
    if (this.#closed) return null;
    return new Promise((resolve, reject) => {
      this.#readers.push({ resolve, reject });
    });
  }

  /**
   * Closes the underlying data channel and resolves pending reads with null.
   *
   * @returns {Promise<void>}
   */
  async close() {
    if (this.#closed) return;
    try {
      this.#channel?.close?.();
    } finally {
      this.#close('closed locally');
    }
  }

  #attach(dataChannel) {
    if (this.#closed) {
      dataChannel?.close?.();
      return;
    }
    assertDataChannel(dataChannel);
    this.#channel = dataChannel;
    try {
      this.#channel.binaryType = 'arraybuffer';
    } catch {}

    this.#cleanup.push(addListener(this.#channel, 'message', (event) => this.#onMessage(event)));
    this.#cleanup.push(addListener(this.#channel, 'open', () => this.#markOpen(), { once: true }));
    this.#cleanup.push(addListener(this.#channel, 'close', (event) => this.#close(event?.reason || 'remote closed')));
    this.#cleanup.push(addListener(this.#channel, 'error', (event) => this.#fail(event?.error || event)));

    if (!this.#channel.readyState || this.#channel.readyState === 'open') {
      this.#markOpen();
    } else if (this.#channel.readyState === 'closed') {
      this.#close('remote closed');
    }
  }

  #markOpen() {
    if (this.#opened || this.#closed) return;
    this.#opened = true;
    this.#resolveOpen();
  }

  #onMessage(event) {
    if (this.#closed) return;
    const chunk = copyBytes(event.data);
    const reader = this.#readers.shift();
    if (reader) {
      reader.resolve(chunk);
      return;
    }
    if (this.#queue.length >= this.maxQueuedChunks) {
      this.#fail(new Error(`WBStream ByteDuplex queue exceeded ${this.maxQueuedChunks} chunks`));
      return;
    }
    this.#queue.push(chunk);
  }

  #close(reason) {
    if (this.#closed) return;
    this.#closed = true;
    this.#removeListeners();
    if (!this.#opened) {
      this.#rejectOpen(new Error(`WBStream ByteDuplex closed before opening: ${reason}`));
    }
    for (const reader of this.#readers.splice(0)) reader.resolve(null);
  }

  #fail(error) {
    if (this.#closed) return;
    const normalizedError = toError(error);
    this.#error = normalizedError;
    this.#closed = true;
    this.#removeListeners();
    this.#rejectOpen(normalizedError);
    for (const reader of this.#readers.splice(0)) reader.reject(normalizedError);
  }

  #removeListeners() {
    for (const cleanup of this.#cleanup.splice(0)) cleanup();
  }
}

/**
 * Wraps a browser WBStream DataChannel as the shared ByteDuplex interface.
 *
 * @param {Promise<object>|object|(() => Promise<object>|object)} dataChannelPromiseOrFactory
 * @param {DataChannelByteDuplexOptions} options
 * @returns {ByteDuplex}
 */
export function createDataChannelByteDuplex(dataChannelPromiseOrFactory, options = {}) {
  return new DataChannelByteDuplex(dataChannelPromiseOrFactory, options);
}

/**
 * Creates a connector compatible with WhitelistBypassTransport.setConnector().
 *
 * @param {WhitelistBypassByteDuplexConnectorOptions} options
 * @returns {(endpoint: TransportEndpoint, context: WhitelistBypassConnectorContext) => Promise<ByteDuplex>}
 */
export function createWhitelistBypassByteDuplexConnector({
  createDataChannel = createWbStreamLiveKitDataChannel,
  duplexOptions = {},
  ...dataChannelOptions
} = {}) {
  if (typeof createDataChannel !== 'function') {
    throw new TypeError('createDataChannel must be a function');
  }

  return async function connectWhitelistBypassEndpoint(endpoint, context) {
    if (!endpoint || endpoint.protocol !== 'wb-tunnel') {
      throw new Error(`Expected wb-tunnel endpoint, got ${endpoint?.protocol || 'empty endpoint'}`);
    }
    if (!endpoint.url) {
      throw new Error('whitelist-bypass connector requires endpoint.url room value');
    }

    const dataChannel = await createDataChannel({
      ...dataChannelOptions,
      room: endpoint.url,
      endpoint,
      context,
    });
    return createDataChannelByteDuplex(dataChannel, duplexOptions);
  };
}
