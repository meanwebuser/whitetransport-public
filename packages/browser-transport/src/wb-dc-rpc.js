import { encodeFrame, FrameDecoder, Msg } from './wb-frame-codec.js';

export class WbDcRpc {
  constructor(dataChannel) {
    this.dc = dataChannel;
    this.nextConnID = 1;
    this.streams = new Map();
    this.decoder = new FrameDecoder((frame) => this.#onFrame(frame));

    this.dc.binaryType = 'arraybuffer';
    this.dc.addEventListener('message', (event) => {
      this.decoder.push(event.data);
    });
  }

  openStream(meta) {
    const connID = this.nextConnID++;
    const stream = new WbStream(this, connID, meta);
    this.streams.set(connID, stream);
    this.send(connID, Msg.Connect, new TextEncoder().encode(JSON.stringify(meta)));
    return stream;
  }

  send(connID, msgType, payload) {
    this.dc.send(encodeFrame(connID, msgType, payload));
  }

  close(connID) {
    this.send(connID, Msg.Close, new Uint8Array());
    this.streams.delete(connID);
  }

  #onFrame({ connID, msgType, payload }) {
    const stream = this.streams.get(connID);
    if (!stream) return;
    stream._onFrame(msgType, payload);
    if (msgType === Msg.Close || msgType === Msg.ConnectErr) this.streams.delete(connID);
  }
}

export class WbStream extends EventTarget {
  constructor(rpc, connID, meta) {
    super();
    this.rpc = rpc;
    this.connID = connID;
    this.meta = meta;
    this.opened = false;
    this.closed = false;
  }

  send(data) {
    const payload = data instanceof Uint8Array ? data : new TextEncoder().encode(String(data));
    this.rpc.send(this.connID, Msg.Data, payload);
  }

  close() {
    if (this.closed) return;
    this.closed = true;
    this.rpc.close(this.connID);
    this.dispatchEvent(new Event('close'));
  }

  _onFrame(msgType, payload) {
    if (msgType === Msg.ConnectOK) {
      this.opened = true;
      this.dispatchEvent(new Event('open'));
      return;
    }
    if (msgType === Msg.ConnectErr) {
      this.dispatchEvent(new CustomEvent('error', { detail: new TextDecoder().decode(payload) }));
      this.close();
      return;
    }
    if (msgType === Msg.Data) {
      this.dispatchEvent(new CustomEvent('data', { detail: payload }));
      return;
    }
    if (msgType === Msg.Close) {
      this.close();
    }
  }
}
