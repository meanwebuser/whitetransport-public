import { createWispOverWbWebSocketFactory } from './wisp-over-wb-adapter.js';
import { createWbStreamLiveKitDataChannel } from './wbstream-livekit-joiner.js';

/**
 * Installs a WebSocket constructor override for Wisp URL only.
 * Existing Scramjet/libcurl transport can still ask for ws(s)://.../wisp/,
 * but the socket is backed by WB WebRTC instead of network WebSocket.
 */
export function installWispOverWb({
  match = /\/wisp\/?$/i,
  dataChannelFactory,
  room,
  roomDiscoveryUrl = '/web/_wt/current-room?source=ok&count=50',
  displayName = 'iPhone',
  fetchImpl = fetch,
  accessToken,
  topic = 'wb-transport',
  onStatus = (...args) => console.info('[wisp-over-wb]', ...args),
} = {}) {
  const NativeWebSocket = window.WebSocket;
  let resolvedRoom = room;
  let resolvedRoomPromise = null;

  async function resolveRoom() {
    if (resolvedRoom) return resolvedRoom;
    if (!roomDiscoveryUrl) throw new Error('WB room is required: pass room or roomDiscoveryUrl');
    if (!resolvedRoomPromise) {
      resolvedRoomPromise = (async () => {
        onStatus({ stage: 'discover-room', url: roomDiscoveryUrl });
        const resp = await fetchImpl(roomDiscoveryUrl, { cache: 'no-store', headers: { Accept: 'application/json' } });
        const json = await resp.json().catch(() => ({}));
        if (!resp.ok || !json.room) {
          throw new Error(`WB room discovery failed: HTTP ${resp.status}: ${JSON.stringify(json).slice(0, 300)}`);
        }
        resolvedRoom = json.room;
        onStatus({ stage: 'room-discovered', room: resolvedRoom, source: json.source });
        return resolvedRoom;
      })();
    }
    return resolvedRoomPromise;
  }

  const factory = dataChannelFactory || (async () => {
    const currentRoom = await resolveRoom();
    return createWbStreamLiveKitDataChannel({ room: currentRoom, displayName, fetchImpl, accessToken, topic, onStatus });
  });
  const WbWebSocket = createWispOverWbWebSocketFactory(factory, { room: resolvedRoom || roomDiscoveryUrl });

  window.WebSocket = function PatchedWebSocket(url, protocols) {
    const textUrl = String(url);
    if (match.test(textUrl)) return new WbWebSocket(url, protocols);
    return new NativeWebSocket(url, protocols);
  };
  window.WebSocket.prototype = NativeWebSocket.prototype;
  Object.assign(window.WebSocket, NativeWebSocket);

  return () => {
    window.WebSocket = NativeWebSocket;
  };
}
