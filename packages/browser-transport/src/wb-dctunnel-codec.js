import { Msg } from './wb-frame-codec.js';

export { Msg };

const CHUNK_SIZE = 994;
let nextChunkID = 1;

export function encodeDcMessage(connID, msgType, payload = new Uint8Array(), obfuscator = null) {
  const body = payload instanceof Uint8Array ? payload : new Uint8Array(payload);
  const raw = new Uint8Array(5 + body.byteLength);
  const view = new DataView(raw.buffer, raw.byteOffset, raw.byteLength);
  view.setUint32(0, connID >>> 0, false);
  view.setUint8(4, msgType);
  raw.set(body, 5);
  return obfuscator ? obfuscator.encryptPayload(raw) : raw;
}

export function decodeDcMessage(data, obfuscator = null) {
  const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
  const raw = obfuscator ? obfuscator.decryptPayload(bytes) : bytes;
  if (!raw || raw.byteLength < 5) return null;
  const view = new DataView(raw.buffer, raw.byteOffset, raw.byteLength);
  return {
    connID: view.getUint32(0, false),
    msgType: view.getUint8(4),
    payload: raw.slice(5),
  };
}

export function chunkDcWirePayload(data) {
  const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
  const total = Math.max(1, Math.ceil(bytes.byteLength / CHUNK_SIZE));
  const id = nextChunkID++ & 0xffff;
  if (nextChunkID > 0xffff) nextChunkID = 1;
  const out = [];
  for (let i = 0; i < total; i++) {
    const start = i * CHUNK_SIZE;
    const end = Math.min(start + CHUNK_SIZE, bytes.byteLength);
    const part = bytes.slice(start, end);
    const frame = new Uint8Array(6 + part.byteLength);
    frame[0] = (id >>> 8) & 0xff;
    frame[1] = id & 0xff;
    frame[2] = (i >>> 8) & 0xff;
    frame[3] = i & 0xff;
    frame[4] = (total >>> 8) & 0xff;
    frame[5] = total & 0xff;
    frame.set(part, 6);
    out.push(frame);
  }
  return out;
}

export class DcChunkReassembler {
  constructor() {
    this.buffers = new Map();
  }

  push(data) {
    const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
    if (bytes.byteLength < 6) return [bytes];
    const id = (bytes[0] << 8) | bytes[1];
    const idx = (bytes[2] << 8) | bytes[3];
    const total = (bytes[4] << 8) | bytes[5];
    const payload = bytes.slice(6);

    if (total <= 0 || idx >= total) return [];
    if (total === 1) return [payload];

    let entry = this.buffers.get(id);
    if (!entry) {
      entry = { chunks: new Array(total), count: 0, size: 0 };
      this.buffers.set(id, entry);
    }
    if (!entry.chunks[idx]) {
      entry.chunks[idx] = payload;
      entry.count += 1;
      entry.size += payload.byteLength;
    }
    if (entry.count !== total) return [];

    this.buffers.delete(id);
    const out = new Uint8Array(entry.size);
    let offset = 0;
    for (const chunk of entry.chunks) {
      if (!chunk) return [];
      out.set(chunk, offset);
      offset += chunk.byteLength;
    }
    return [out];
  }
}
