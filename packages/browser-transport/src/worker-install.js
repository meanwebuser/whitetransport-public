/* Worker-side whitetransport wisp shim.
 *
 * epoxy/libcurl open their wisp socket inside the bare-mux SharedWorker via
 * `new WebSocket(wispUrl)`. The page-side overlay only patches the page's
 * WebSocket, so the worker socket used to bypass the WB carrier entirely.
 *
 * This entry is bundled WITHOUT livekit-client (workers + LiveKit is a risk
 * we don't need): the wisp framing runs here in WispOverWbSocket, and the
 * physical carrier is a MessagePort bridge to the page, which owns the
 * LiveKit data channel. If the page does not answer the bridge handshake
 * (whitetransport disabled, no room material, backend down), the shim falls
 * back to a native WebSocket to the real wisp endpoint — byte-for-byte the
 * pre-shim behaviour.
 */

import { WispOverWbSocket } from './wisp-over-wb-adapter.js';

const BRIDGE_CHANNEL = 'wb-wisp-shim';
const NATIVE = self.WebSocket;

function askPageForBridge(url, timeoutMs) {
	return new Promise((resolve, reject) => {
		if (typeof BroadcastChannel === 'undefined') return reject(new Error('BroadcastChannel unavailable'));
		const id = Math.random().toString(36).slice(2);
		const bc = new BroadcastChannel(BRIDGE_CHANNEL);
		const timer = setTimeout(() => { cleanup(); reject(new Error('page bridge timeout')); }, timeoutMs);
		function onMessage(ev) {
			const data = ev.data;
			if (!data || data.type !== 'wb-wisp-shim:bridge' || data.id !== id) return;
			cleanup();
			if (data.ok && data.port) resolve({ room: data.room || '', port: data.port });
			else reject(new Error(data.reason || 'bridge refused'));
		}
		function cleanup() {
			clearTimeout(timer);
			try { bc.removeEventListener('message', onMessage); } catch {}
			try { bc.close(); } catch {}
		}
		bc.addEventListener('message', onMessage);
		bc.postMessage({ type: 'wb-wisp-shim:get-bridge', id, url });
	});
}

/* Wraps a MessagePort into the transport-like interface WispOverWbSocket
 * expects from its data channel (EventTarget + send + readyState + close). */
function portToDataChannel(port) {
	const like = new EventTarget();
	like.binaryType = 'arraybuffer';
	like.readyState = 'connecting';
	like.send = (data) => port.postMessage(data);
	like.close = () => { try { port.close(); } catch {} };
	port.addEventListener('message', (ev) => {
		const data = ev.data;
		if (data && data.__wbBridge === 'open') {
			like.readyState = 'open';
			like.dispatchEvent(new Event('open'));
			return;
		}
		if (data && data.__wbBridge === 'close') {
			like.readyState = 'closed';
			like.dispatchEvent(new CloseEvent('close'));
			return;
		}
		like.dispatchEvent(new MessageEvent('message', { data }));
	});
	port.start();
	return like;
}

class WbWispProxy extends EventTarget {
	constructor(url, protocols, { timeoutMs } = {}) {
		super();
		this.url = url;
		this.binaryType = 'arraybuffer';
		this.bufferedAmount = 0;
		this.readyState = NATIVE.CONNECTING ?? 0;
		this.#inner = null;
		this.#buffer = [];
		this.#listeners = [];
		this.#userClosed = false;
		for (const name of ['onopen', 'onmessage', 'onerror', 'onclose']) {
			Object.defineProperty(this, name, {
				configurable: true,
				get: () => this[`#${name}`],
				set: (fn) => {
					this[`#${name}`] = typeof fn === 'function' ? fn : undefined;
					if (this.#inner) this.#inner[name] = this[`#${name}`];
				},
			});
		}
		askPageForBridge(url, timeoutMs || 3000).then(({ room, port }) => {
			if (this.#userClosed) return;
			const dc = portToDataChannel(port);
			/* WispOverWbSocket queues client packets until it is OPEN, so the
			 * proxy can flush its own buffer straight away. */
			this.#attach(new WispOverWbSocket(() => Promise.resolve(dc), { room }));
		}).catch(() => {
			if (this.#userClosed) return;
			this.#attach(new NATIVE(url, protocols));
		});
	}

	#inner = null;
	#buffer = [];
	#listeners = [];
	#userClosed = false;
	#onopen;
	#onmessage;
	#onerror;
	#onclose;

	#attach(inner) {
		if (this.#userClosed) {
			try { inner.close(); } catch {}
			return;
		}
		this.#inner = inner;
		this.readyState = inner.readyState;
		for (const name of ['onopen', 'onmessage', 'onerror', 'onclose']) {
			if (this[`#${name}`]) inner[name] = this[`#${name}`];
		}
		for (const [type, fn, options] of this.#listeners.splice(0)) {
			inner.addEventListener(type, fn, options);
		}
		inner.addEventListener('open', () => {
			this.readyState = NATIVE.OPEN ?? 1;
			this.#flush();
		});
		inner.addEventListener('close', () => { this.readyState = NATIVE.CLOSED ?? 3; });
		this.#flush();
	}

	#flush() {
		if (!this.#inner || this.#inner.readyState !== (NATIVE.OPEN ?? 1)) return;
		const queued = this.#buffer.splice(0);
		for (const data of queued) {
			try { this.#inner.send(data); } catch {}
		}
	}

	send(data) {
		if (this.#userClosed) throw new Error('WebSocket is closed');
		if (this.#inner) {
			try {
				this.#inner.send(data);
				return;
			} catch {
				/* inner not open yet — fall through to buffering */
			}
		}
		if (this.#buffer.length >= 512) this.#buffer.shift();
		this.#buffer.push(data);
	}

	close(code = 1000, reason = '') {
		this.#userClosed = true;
		this.readyState = NATIVE.CLOSED ?? 3;
		if (this.#inner) {
			try { this.#inner.close(code, reason); } catch {}
		}
		const event = new CloseEvent('close', { code, reason, wasClean: true });
		this.dispatchEvent(event);
		if (typeof this.#onclose === 'function') this.#onclose(event);
	}

	addEventListener(type, fn, options) {
		if (this.#inner) return this.#inner.addEventListener(type, fn, options);
		this.#listeners.push([type, fn, options]);
	}

	removeEventListener(type, fn, options) {
		if (this.#inner) return this.#inner.removeEventListener(type, fn, options);
		this.#listeners = this.#listeners.filter(([t, f, o]) => !(t === type && f === fn && o === options));
	}
}

export function patchWispSocket({ match = /\/wisp\/?(?:[?#].*)?$/i, timeoutMs = 3000 } = {}) {
	if (self.__wbWispWorkerPatched) return false;
	self.__wbWispWorkerPatched = true;
	function PatchedWebSocket(url, protocols) {
		if (typeof url === 'string' && match.test(url)) {
			return new WbWispProxy(url, protocols, { timeoutMs });
		}
		return new NATIVE(url, protocols);
	}
	PatchedWebSocket.prototype = NATIVE.prototype;
	PatchedWebSocket.CONNECTING = NATIVE.CONNECTING ?? 0;
	PatchedWebSocket.OPEN = NATIVE.OPEN ?? 1;
	PatchedWebSocket.CLOSING = NATIVE.CLOSING ?? 2;
	PatchedWebSocket.CLOSED = NATIVE.CLOSED ?? 3;
	try { Object.defineProperty(PatchedWebSocket, 'name', { value: 'WebSocket' }); } catch {}
	self.WebSocket = PatchedWebSocket;
	return true;
}

self.WhiteTransportWorker = { patchWispSocket, WbWispProxy };
