export const WispPacketType = Object.freeze({
  Connect: 0x01,
  Data: 0x02,
  Continue: 0x03,
  Close: 0x04,
  Info: 0x05,
});

export const WispStreamType = Object.freeze({
  Tcp: 0x01,
  Udp: 0x02,
});

export const WispCloseReason = Object.freeze({
  Unknown: 0x01,
  Voluntary: 0x02,
  NetworkError: 0x03,
  UnreachableHost: 0x42,
});

const te = new TextEncoder();
const td = new TextDecoder();

export function parseWispPacket(data) {
  const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
  if (bytes.byteLength < 5) throw new Error('Wisp packet too small');
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const type = view.getUint8(0);
  const streamID = view.getUint32(1, true);
  const payload = bytes.slice(5);

  if (type === WispPacketType.Connect) {
    if (payload.byteLength < 3) throw new Error('Wisp CONNECT payload too small');
    const pv = new DataView(payload.buffer, payload.byteOffset, payload.byteLength);
    const streamType = pv.getUint8(0);
    const port = pv.getUint16(1, true);
    const hostname = td.decode(payload.slice(3));
    return { type, streamID, streamType, port, hostname, payload };
  }

  if (type === WispPacketType.Data) {
    return { type, streamID, data: payload, payload };
  }

  if (type === WispPacketType.Continue) {
    const pv = new DataView(payload.buffer, payload.byteOffset, payload.byteLength);
    return { type, streamID, bufferRemaining: payload.byteLength >= 4 ? pv.getUint32(0, true) : 0, payload };
  }

  if (type === WispPacketType.Close) {
    return { type, streamID, reason: payload[0] || WispCloseReason.Unknown, payload };
  }

  return { type, streamID, payload };
}

export function encodeWispData(streamID, data) {
  return encodeWispPacket(WispPacketType.Data, streamID, data);
}

export function encodeWispContinue(streamID, bufferRemaining = 0xffffff) {
  const payload = new Uint8Array(4);
  new DataView(payload.buffer).setUint32(0, bufferRemaining >>> 0, true);
  return encodeWispPacket(WispPacketType.Continue, streamID, payload);
}

export function encodeWispClose(streamID, reason = WispCloseReason.Voluntary) {
  return encodeWispPacket(WispPacketType.Close, streamID, new Uint8Array([reason]));
}

export function encodeWispInfo() {
  return encodeWispPacket(WispPacketType.Info, 0, new Uint8Array([2, 0]));
}

export function encodeWispPacket(type, streamID, payload = new Uint8Array()) {
  const body = payload instanceof Uint8Array ? payload : te.encode(String(payload));
  const out = new Uint8Array(5 + body.byteLength);
  const view = new DataView(out.buffer, out.byteOffset, out.byteLength);
  view.setUint8(0, type);
  view.setUint32(1, streamID >>> 0, true);
  out.set(body, 5);
  return out;
}
