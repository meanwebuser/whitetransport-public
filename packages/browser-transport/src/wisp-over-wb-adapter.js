import { Msg, ControlConnID } from './wb-frame-codec.js';
import { encodeDcMessage, decodeDcMessage, chunkDcWirePayload, DcChunkReassembler } from './wb-dctunnel-codec.js';
import { WbObfuscator } from './wb-obfuscator.js';
import {
  WispPacketType,
  WispStreamType,
  WispCloseReason,
  parseWispPacket,
  encodeWispClose,
  encodeWispContinue,
  encodeWispData,
  encodeWispInfo,
} from './wisp-packet-codec.js';

function concatAddress(hostname, port) {
  if (hostname.includes(':') && !hostname.startsWith('[')) return `[${hostname}]:${port}`;
  return `${hostname}:${port}`;
}

function normalizeBytes(data) {
  if (data instanceof Uint8Array) return data;
  if (data instanceof ArrayBuffer) return new Uint8Array(data);
  if (data?.buffer instanceof ArrayBuffer) return new Uint8Array(data.buffer, data.byteOffset || 0, data.byteLength);
  return new TextEncoder().encode(String(data));
}

/**
 * WebSocket-like object for Wisp clients, but the physical carrier is WB WebRTC DataChannel.
 *
 * Input side:  Wisp packets from Scramjet/libcurl/bare-mux.
 * Output side: whitelist-bypass tunnel frames consumed by the exit-node creator RelayBridge.
 */
export class WispOverWbSocket extends EventTarget {
  constructor(dataChannelPromiseOrFactory, { room = '', obfuscator = null } = {}) {
    super();
    this.readyState = WebSocket.CONNECTING;
    this.binaryType = 'arraybuffer';
    this.bufferedAmount = 0;
    this.dc = null;
    this.streams = new Map();
    this.obfuscator = obfuscator || (room ? WbObfuscator.fromJoinLink(room) : null);
    this.chunker = new DcChunkReassembler();
    this.creatorReady = false;
    this.readyTimer = null;
    this.readyFallbackTimer = null;
    this.readyAttempts = 0;
    this.pendingClientPackets = [];
    this.maxPendingClientPackets = 512;

    const dataChannelPromise = typeof dataChannelPromiseOrFactory === 'function'
      ? dataChannelPromiseOrFactory()
      : dataChannelPromiseOrFactory;
    Promise.resolve(dataChannelPromise).then((dc) => this.#attach(dc)).catch((err) => this.#fail(err));
  }

  #attach(dc) {
    this.dc = dc;
    this.dc.binaryType = 'arraybuffer';
    this.dc.addEventListener('message', (event) => {
      const payloads = this.chunker.push(normalizeBytes(event.data));
      for (const payload of payloads) {
        const frame = decodeDcMessage(payload, this.obfuscator);
        if (frame) this.#onWbFrame(frame);
        else console.warn('[wisp-over-wb] failed to decode/decrypt WB DC message', payload.byteLength);
      }
    });
    this.dc.addEventListener('close', () => this.#close());
    this.dc.addEventListener('error', (event) => this.dispatchEvent(new ErrorEvent('error', { error: event.error, message: event.message || 'WB DataChannel error' })));

    const startReadyHandshake = () => this.#startCreatorReadyHandshake();
    if (this.dc.readyState === 'open') startReadyHandshake();
    else this.dc.addEventListener('open', startReadyHandshake, { once: true });
  }

  #fail(error) {
    this.readyState = WebSocket.CLOSED;
    if (this.readyTimer) clearTimeout(this.readyTimer);
    if (this.readyFallbackTimer) clearTimeout(this.readyFallbackTimer);
    this.dispatchEvent(new ErrorEvent('error', { error, message: error?.message || String(error) }));
    this.dispatchEvent(new CloseEvent('close'));
  }

  #startCreatorReadyHandshake() {
    if (this.readyState !== WebSocket.CONNECTING || this.creatorReady) return;
    this.readyFallbackTimer = setTimeout(() => this.#markCreatorReady('fallback-timeout'), 3000);
    this.#sendCreatorReadyProbe();
  }

  #sendCreatorReadyProbe() {
    if (this.readyState !== WebSocket.CONNECTING || this.creatorReady) return;
    this.readyAttempts += 1;
    this.#sendWb(ControlConnID, Msg.Config, encodeConfigPayload(24, 30, 1));
    this.readyTimer = setTimeout(() => this.#sendCreatorReadyProbe(), 250);
  }

  #markCreatorReady(reason = 'config-ack') {
    if (this.creatorReady || this.readyState !== WebSocket.CONNECTING) return;
    this.creatorReady = true;
    if (this.readyTimer) clearTimeout(this.readyTimer);
    if (this.readyFallbackTimer) clearTimeout(this.readyFallbackTimer);
    this.readyTimer = null;
    this.readyFallbackTimer = null;
    this.readyState = WebSocket.OPEN;
    const openEvent = new Event('open');
    this.dispatchEvent(openEvent);
    if (typeof this.onopen === 'function') this.onopen(openEvent);
    // WISP v2 handshake: announce server capabilities with INFO. Do not send
    // CONTINUE(0): CONTINUE is per-stream flow control, and Safari/iOS libcurl
    // aborts TLS if it receives CONTINUE for a not-yet-created stream.
    this.#emitWisp(encodeWispInfo());
    console.info('[wisp-over-wb] creator ready, WISP INFO sent', { attempts: this.readyAttempts, reason, queued: this.pendingClientPackets.length });
    this.#flushPendingClientPackets();
  }

  send(data) {
    const bytes = normalizeBytes(data);
    if (this.readyState === WebSocket.CONNECTING) {
      if (this.pendingClientPackets.length >= this.maxPendingClientPackets) {
        this.pendingClientPackets.shift();
      }
      this.pendingClientPackets.push(bytes);
      return;
    }
    if (this.readyState !== WebSocket.OPEN) throw new Error('WispOverWbSocket is not open');
    this.#handleClientPacket(bytes);
  }

  #flushPendingClientPackets() {
    if (this.readyState !== WebSocket.OPEN || this.pendingClientPackets.length === 0) return;
    const queued = this.pendingClientPackets.splice(0);
    console.info('[wisp-over-wb] flushing queued client packets', { count: queued.length });
    for (const bytes of queued) this.#handleClientPacket(bytes);
  }

  #handleClientPacket(bytes) {
    const pkt = parseWispPacket(bytes);
    if (pkt.type === WispPacketType.Connect) return this.#onWispConnect(pkt);
    if (pkt.type === WispPacketType.Data) return this.#onWispData(pkt);
    if (pkt.type === WispPacketType.Close) return this.#onWispClose(pkt);
    if (pkt.type === WispPacketType.Continue) return;
    if (pkt.type === WispPacketType.Info) {
      // INFO is optional metadata from newer WISP clients. There is no WB-side
      // stream for it, so do not answer with CONTINUE(0).
      console.info('[wisp-over-wb] client INFO received, ignored');
      return;
    }
    console.warn('[wisp-over-wb] unknown Wisp packet type', pkt.type);
  }

  close(code, reason) {
    this.readyState = WebSocket.CLOSING;
    try { this.dc?.close?.(); } catch {}
    this.#close(code, reason);
  }

  #onWispConnect(pkt) {
    if (pkt.streamType !== WispStreamType.Tcp) {
      this.#emitWisp(encodeWispClose(pkt.streamID, WispCloseReason.NetworkError));
      return;
    }
    const addr = concatAddress(pkt.hostname, pkt.port);
    console.info('[wisp-over-wb] CONNECT', { streamID: pkt.streamID, addr });
    this.streams.set(pkt.streamID, { hostname: pkt.hostname, port: pkt.port, addr });
    this.#sendWb(pkt.streamID, Msg.Connect, new TextEncoder().encode(addr));
  }

  #onWispData(pkt) {
    if (!this.streams.has(pkt.streamID)) return;
    this.#sendWb(pkt.streamID, Msg.Data, pkt.data);
  }

  #onWispClose(pkt) {
    this.#sendWb(pkt.streamID, Msg.Close, new Uint8Array());
    this.streams.delete(pkt.streamID);
  }

  #onWbFrame({ connID, msgType, payload }) {
    if (connID === ControlConnID && msgType === Msg.ConfigAck) {
      this.#markCreatorReady();
      return;
    }
    if (msgType === Msg.ConnectOK) {
      this.#emitWisp(encodeWispContinue(connID));
      return;
    }
    if (msgType === Msg.ConnectErr) {
      this.#emitWisp(encodeWispClose(connID, WispCloseReason.UnreachableHost));
      this.streams.delete(connID);
      return;
    }
    if (msgType === Msg.Data) {
      this.#emitWisp(encodeWispData(connID, payload));
      return;
    }
    if (msgType === Msg.Close) {
      this.#emitWisp(encodeWispClose(connID, WispCloseReason.Voluntary));
      this.streams.delete(connID);
    }
  }

  #sendWb(connID, msgType, payload) {
    const wire = encodeDcMessage(connID, msgType, payload, this.obfuscator);
    const chunks = chunkDcWirePayload(wire);
    for (const chunk of chunks) {
      this.dc.send(chunk);
    }
    this.bufferedAmount = this.dc.bufferedAmount || 0;
  }

  #emitWisp(bytes) {
    const data = bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
    this.dispatchEvent(new MessageEvent('message', { data }));
    if (typeof this.onmessage === 'function') this.onmessage({ data });
  }

  #close(code = 1000, reason = '') {
    if (this.readyTimer) clearTimeout(this.readyTimer);
    if (this.readyFallbackTimer) clearTimeout(this.readyFallbackTimer);
    this.readyTimer = null;
    this.readyFallbackTimer = null;
    this.pendingClientPackets = [];
    if (this.readyState === WebSocket.CLOSED) return;
    this.readyState = WebSocket.CLOSED;
    this.dispatchEvent(new CloseEvent('close', { code, reason }));
    if (typeof this.onclose === 'function') this.onclose({ code, reason });
  }
}

function encodeConfigPayload(fps, batch, trackCount) {
  const out = new Uint8Array(6);
  const view = new DataView(out.buffer);
  view.setUint16(0, fps, false);
  view.setUint16(2, batch, false);
  view.setUint16(4, trackCount, false);
  return out;
}

export function createWispOverWbWebSocketFactory(dataChannelFactory, options = {}) {
  return function WispOverWbWebSocket(_url, _protocols) {
    return new WispOverWbSocket(dataChannelFactory, options);
  };
}
