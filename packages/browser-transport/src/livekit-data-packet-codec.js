const WIRE_VARINT = 0;
const WIRE_BYTES = 2;
const DATA_PACKET_FIELD_KIND = 1;
const DATA_PACKET_FIELD_USER = 2;
const USER_PACKET_FIELD_PAYLOAD = 2;

function encodeVarint(value) {
  let v = Number(value >>> 0);
  const out = [];
  while (v >= 0x80) {
    out.push((v & 0x7f) | 0x80);
    v = Math.floor(v / 128);
  }
  out.push(v);
  return out;
}

function encodeTag(field, wire) {
  return encodeVarint((field << 3) | wire);
}

function encodeBytes(field, bytes) {
  const b = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
  return new Uint8Array([...encodeTag(field, WIRE_BYTES), ...encodeVarint(b.byteLength), ...b]);
}

function encodeInt32(field, value) {
  return new Uint8Array([...encodeTag(field, WIRE_VARINT), ...encodeVarint(value)]);
}

function concat(parts) {
  const total = parts.reduce((sum, p) => sum + p.byteLength, 0);
  const out = new Uint8Array(total);
  let offset = 0;
  for (const part of parts) {
    out.set(part, offset);
    offset += part.byteLength;
  }
  return out;
}

function readVarint(bytes, state) {
  let value = 0;
  let shift = 0;
  for (;;) {
    if (state.pos >= bytes.byteLength) throw new Error('varint: unexpected eof');
    const b = bytes[state.pos++];
    value += (b & 0x7f) * (2 ** shift);
    if (b < 0x80) return value >>> 0;
    shift += 7;
    if (shift >= 32) throw new Error('varint: overflow');
  }
}

function readBytes(bytes, state) {
  const len = readVarint(bytes, state);
  if (state.pos + len > bytes.byteLength) throw new Error('bytes: unexpected eof');
  const out = bytes.slice(state.pos, state.pos + len);
  state.pos += len;
  return out;
}

function skipField(bytes, state, wire) {
  if (wire === WIRE_VARINT) {
    readVarint(bytes, state);
    return;
  }
  if (wire === WIRE_BYTES) {
    readBytes(bytes, state);
    return;
  }
  throw new Error(`unsupported protobuf wire type ${wire}`);
}

export function encodeLiveKitDataPacketUser(payload, kind = 0) {
  const userPacket = encodeBytes(USER_PACKET_FIELD_PAYLOAD, payload);
  const parts = [];
  if (kind !== 0) parts.push(encodeInt32(DATA_PACKET_FIELD_KIND, kind));
  parts.push(encodeBytes(DATA_PACKET_FIELD_USER, userPacket));
  return concat(parts);
}

export function decodeLiveKitDataPacketUser(data) {
  const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
  const state = { pos: 0 };
  try {
    while (state.pos < bytes.byteLength) {
      const tag = readVarint(bytes, state);
      const field = tag >>> 3;
      const wire = tag & 0x7;
      if (field === DATA_PACKET_FIELD_USER && wire === WIRE_BYTES) {
        const inner = readBytes(bytes, state);
        const userState = { pos: 0 };
        while (userState.pos < inner.byteLength) {
          const userTag = readVarint(inner, userState);
          const userField = userTag >>> 3;
          const userWire = userTag & 0x7;
          if (userField === USER_PACKET_FIELD_PAYLOAD && userWire === WIRE_BYTES) {
            return readBytes(inner, userState);
          }
          skipField(inner, userState, userWire);
        }
        return null;
      }
      skipField(bytes, state, wire);
    }
  } catch {
    return null;
  }
  return null;
}
