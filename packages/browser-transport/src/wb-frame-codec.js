export const Msg = Object.freeze({
  Connect: 0x01,
  ConnectOK: 0x02,
  ConnectErr: 0x03,
  Data: 0x04,
  Close: 0x05,
  UDP: 0x06,
  UDPReply: 0x07,
  Config: 0x08,
  ConfigAck: 0x09,
});

export const ControlConnID = 0;

export function encodeFrame(connID, msgType, payload = new Uint8Array()) {
  const bodyLen = 5 + payload.byteLength;
  const out = new Uint8Array(4 + bodyLen);
  const view = new DataView(out.buffer, out.byteOffset, out.byteLength);
  view.setUint32(0, bodyLen, false);
  view.setUint32(4, connID >>> 0, false);
  view.setUint8(8, msgType);
  out.set(payload, 9);
  return out;
}

export class FrameDecoder {
  constructor(onFrame) {
    this.onFrame = onFrame;
    this.buffer = new Uint8Array(0);
  }

  push(chunk) {
    const input = chunk instanceof Uint8Array ? chunk : new Uint8Array(chunk);
    const merged = new Uint8Array(this.buffer.byteLength + input.byteLength);
    merged.set(this.buffer, 0);
    merged.set(input, this.buffer.byteLength);
    this.buffer = merged;

    while (this.buffer.byteLength >= 4) {
      const view = new DataView(this.buffer.buffer, this.buffer.byteOffset, this.buffer.byteLength);
      const frameLen = view.getUint32(0, false);
      if (frameLen < 5) throw new Error(`invalid WB frame length: ${frameLen}`);
      const total = 4 + frameLen;
      if (this.buffer.byteLength < total) return;

      const frame = this.buffer.slice(0, total);
      const frameView = new DataView(frame.buffer, frame.byteOffset, frame.byteLength);
      const connID = frameView.getUint32(4, false);
      const msgType = frameView.getUint8(8);
      const payload = frame.slice(9);
      this.onFrame({ connID, msgType, payload });
      this.buffer = this.buffer.slice(total);
    }
  }
}
