import { Room, RoomEvent } from 'livekit-client';
import { authAndGetToken } from './wbstream-api-client.js';

const DEFAULT_CREATOR_IDENTITY = '9380ac10-a2a4-427e-a180-46b3c1acc384';

function isNegotiationError(error) {
  const text = `${error?.name || ''} ${error?.message || error || ''}`;
  return /NegotiationError|negotiation timed out/i.test(text);
}

function patchNegotiationLogNoise(engine, onStatus) {
  if (!engine || engine.__wbNegotiationLogPatch) return;
  engine.__wbNegotiationLogPatch = true;
  const patchLogger = (logger, name) => {
    if (!logger || logger.__wbNegotiationLogPatch || typeof logger.error !== 'function') return;
    logger.__wbNegotiationLogPatch = true;
    const originalError = logger.error.bind(logger);
    logger.error = (...args) => {
      if (args.some(isNegotiationError)) {
        onStatus({ stage: 'ignored-negotiation-log-error', logger: name });
        return;
      }
      return originalError(...args);
    };
  };
  patchLogger(engine.log, 'engine');
  patchLogger(engine.pcManager?.log, 'pcManager');
  patchLogger(engine.pcManager?.publisher?.log, 'publisher');
}

class LiveKitDataTransportLike extends EventTarget {
  constructor(room, { onStatus = () => {}, creatorIdentity = DEFAULT_CREATOR_IDENTITY } = {}) {
    super();
    this.room = room;
    this.onStatus = onStatus;
    this.creatorIdentity = creatorIdentity;
    this.readyState = 'connecting';
    this.binaryType = 'arraybuffer';
    this.bufferedAmount = 0;
    patchNegotiationLogNoise(this.room.engine, this.onStatus);
    this.#wireRoomEvents();
    queueMicrotask(() => this.#open());
  }

  #wireRoomEvents() {
    this.room.on(RoomEvent.DataReceived, (payload) => {
      if (!payload || payload.byteLength === 0) return;
      const bytes = payload instanceof Uint8Array ? payload : new Uint8Array(payload);
      const data = bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
      const out = new MessageEvent('message', { data });
      this.dispatchEvent(out);
      if (typeof this.onmessage === 'function') this.onmessage(out);
    });
    this.room.on(RoomEvent.Disconnected, (reason) => this.#close(`LiveKit disconnected: ${reason || ''}`));
    this.room.on(RoomEvent.ConnectionStateChanged, (state) => this.onStatus({ stage: 'connection-state', state }));
  }

  #open() {
    if (this.readyState !== 'connecting') return;
    this.readyState = 'open';
    this.onStatus({ stage: 'data-transport-open', mode: 'publishData+DataReceived' });
    const event = new Event('open');
    this.dispatchEvent(event);
    if (typeof this.onopen === 'function') this.onopen(event);
  }

  send(data) {
    if (this.readyState !== 'open') throw new Error('LiveKit data transport is not open');
    const payload = data instanceof Uint8Array ? data : new Uint8Array(data);
    const remotes = [...(this.room.remoteParticipants?.values?.() || [])].map((p) => ({ identity: p.identity, sid: p.sid }));
    const target = remotes.find((p) => p.identity === this.creatorIdentity)?.identity || this.creatorIdentity || undefined;
    const options = target ? { reliable: true, destinationIdentities: [target] } : { reliable: true };
    this.room.localParticipant.publishData(payload, options).catch((err) => {
      if (isNegotiationError(err)) {
        this.onStatus({ stage: 'ignored-publish-negotiation-error', message: err?.message || String(err) });
        return;
      }
      console.error('[whitetransport] publishData failed', err);
    });
    this.bufferedAmount = 0;
  }

  close() {
    try { this.room.disconnect(); } catch {}
    this.#close('closed by client');
  }

  #close(reason = '') {
    if (this.readyState === 'closed') return;
    this.readyState = 'closed';
    const event = new CloseEvent('close', { reason });
    this.dispatchEvent(event);
    if (typeof this.onclose === 'function') this.onclose(event);
  }
}

export async function createWbStreamLiveKitDataChannel({
  room,
  displayName = 'iPhone',
  fetchImpl = fetch,
  accessToken,
  livekitOptions = {},
  onStatus = () => {},
  creatorIdentity = DEFAULT_CREATOR_IDENTITY,
} = {}) {
  onStatus({ stage: 'auth', room });
  const auth = await authAndGetToken({ room, displayName, accessToken, fetchImpl });

  onStatus({ stage: 'connect-livekit', serverUrl: auth.serverUrl, roomID: auth.roomID });
  const roomClient = new Room({ adaptiveStream: false, dynacast: false, ...livekitOptions });
  await roomClient.connect(auth.serverUrl, auth.roomToken, { autoSubscribe: false });

  patchNegotiationLogNoise(roomClient.engine, onStatus);
  onStatus({ stage: 'connected', roomID: auth.roomID });
  return new LiveKitDataTransportLike(roomClient, { onStatus, creatorIdentity });
}
