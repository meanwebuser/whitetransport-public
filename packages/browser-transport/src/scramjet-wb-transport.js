import { WbDcRpc } from './wb-dc-rpc.js';

export class ScramjetWbTransport {
  constructor({ dataChannelFactory, room, targetBase } = {}) {
    this.dataChannelFactory = dataChannelFactory;
    this.room = room;
    this.targetBase = targetBase || '';
    this.rpc = null;
  }

  async init() {
    if (!this.dataChannelFactory) {
      throw new Error('dataChannelFactory is required: WB Stream browser joiner is not implemented yet');
    }
    const dc = await this.dataChannelFactory({ room: this.room });
    this.rpc = new WbDcRpc(dc);
  }

  async fetch(input, init = {}) {
    const source = typeof input === 'string' ? {} : input;
    const url = typeof input === 'string' ? input : input.url;
    const method = (init.method || source.method || 'GET').toUpperCase();
    const headers = new Headers(init.headers || source.headers || {});
    const wantsJsonp = init.jsonp === true || /^(1|true|yes)$/i.test(headers.get('x-wb-jsonp') || '');

    if (wantsJsonp) {
      return this.jsonpFetch(url, { method, headers, callbackParam: init.jsonpCallbackParam, timeoutMs: init.jsonpTimeoutMs });
    }

    if (!this.rpc) await this.init();
    const body = init.body ? await new Response(init.body).arrayBuffer() : null;

    const stream = this.rpc.openStream({ type: 'fetch', url, method, headers: Object.fromEntries(headers.entries()) });
    const chunks = [];
    let responseMeta = null;

    const wait = new Promise((resolve, reject) => {
      stream.addEventListener('open', () => {
        if (body) stream.send(new Uint8Array(body));
      });
      stream.addEventListener('data', (event) => {
        const payload = event.detail;
        // TODO: split response meta/body framing.
        // Placeholder convention: first packet JSON meta if not set, rest body.
        if (!responseMeta) {
          try {
            responseMeta = JSON.parse(new TextDecoder().decode(payload));
            return;
          } catch {}
        }
        chunks.push(payload);
      });
      stream.addEventListener('close', () => resolve());
      stream.addEventListener('error', (event) => reject(new Error(event.detail || 'WB fetch stream error')));
    });

    await wait;
    const bodyBytes = concat(chunks);
    return new Response(bodyBytes, {
      status: responseMeta?.status || 200,
      headers: responseMeta?.headers || {},
    });
  }

  jsonpFetch(url, { method = 'GET', headers = new Headers(), callbackParam = 'callback', timeoutMs = 15000 } = {}) {
    if (typeof document === 'undefined') {
      return Promise.reject(new Error('JSONP mode requires a browser document'));
    }
    if (method !== 'GET' && method !== 'HEAD') {
      return Promise.reject(new Error(`JSONP mode supports only GET/HEAD, got ${method}`));
    }

    const callbackHeader = headers.get('x-wb-jsonp-callback-param');
    const param = callbackHeader || callbackParam || 'callback';
    const requestUrl = new URL(url, this.targetBase || window.location.href);
    const callbackName = `__wt_jsonp_${Date.now()}_${Math.random().toString(36).slice(2)}`;
    requestUrl.searchParams.set(param, callbackName);

    return new Promise((resolve, reject) => {
      const script = document.createElement('script');
      let timer = null;
      const cleanup = () => {
        if (timer) clearTimeout(timer);
        delete window[callbackName];
        script.remove();
      };

      window[callbackName] = (payload) => {
        cleanup();
        resolve(new Response(JSON.stringify(payload), {
          status: 200,
          headers: { 'content-type': 'application/json; charset=utf-8', 'x-wb-transport': 'jsonp' }
        }));
      };

      script.onerror = () => {
        cleanup();
        reject(new Error(`JSONP request failed: ${requestUrl.href}`));
      };
      timer = setTimeout(() => {
        cleanup();
        reject(new Error(`JSONP request timed out after ${timeoutMs}ms: ${requestUrl.href}`));
      }, timeoutMs);

      script.async = true;
      script.src = requestUrl.href;
      document.head.appendChild(script);
    });
  }

  createWebSocket(url, protocols) {
    if (!this.rpc) throw new Error('call init() before createWebSocket()');
    const stream = this.rpc.openStream({ type: 'websocket', url, protocols });
    return new WbWebSocketShim(stream);
  }
}

class WbWebSocketShim extends EventTarget {
  constructor(stream) {
    super();
    this.stream = stream;
    this.readyState = WebSocket.CONNECTING;
    this.binaryType = 'arraybuffer';
    stream.addEventListener('open', () => {
      this.readyState = WebSocket.OPEN;
      this.dispatchEvent(new Event('open'));
    });
    stream.addEventListener('data', (event) => {
      this.dispatchEvent(new MessageEvent('message', { data: event.detail }));
    });
    stream.addEventListener('close', () => {
      this.readyState = WebSocket.CLOSED;
      this.dispatchEvent(new CloseEvent('close'));
    });
    stream.addEventListener('error', (event) => {
      this.dispatchEvent(new ErrorEvent('error', { message: event.detail || 'WB websocket stream error' }));
    });
  }

  send(data) {
    this.stream.send(data instanceof ArrayBuffer ? new Uint8Array(data) : data);
  }

  close() {
    this.readyState = WebSocket.CLOSING;
    this.stream.close();
  }
}

function concat(chunks) {
  const total = chunks.reduce((sum, chunk) => sum + chunk.byteLength, 0);
  const out = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return out;
}
