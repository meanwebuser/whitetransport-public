const API_BASE = 'https://stream.wb.ru';
const DEFAULT_DISPLAY_NAME = 'iPhone';

function apiUrl(path) {
  return `${API_BASE}${path}`;
}

async function parseJsonResponse(resp, label) {
  const text = await resp.text();
  if (!resp.ok) throw new Error(`${label}: HTTP ${resp.status}: ${text}`);
  try { return JSON.parse(text); }
  catch (err) { throw new Error(`${label}: invalid JSON: ${err.message}: ${text.slice(0, 200)}`); }
}

function bearerHeaders(accessToken, extra = {}) {
  const headers = { ...extra };
  if (accessToken) headers.Authorization = `Bearer ${accessToken}`;
  return headers;
}

export function parseWbStreamRoomID(input = '') {
  const value = String(input || '').trim();
  if (!value) return '';
  if (value.startsWith('wbstream://')) return value.slice('wbstream://'.length).replace(/^\/+|\/+$/g, '');
  try {
    const url = new URL(value);
    const parts = url.pathname.split('/').filter(Boolean);
    const index = parts.indexOf('room');
    if (index !== -1 && parts[index + 1]) return parts[index + 1];
  } catch {}
  return value.replace(/^\/+|\/+$/g, '');
}

export async function registerGuest({ displayName = DEFAULT_DISPLAY_NAME, fetchImpl = fetch } = {}) {
  const resp = await fetchImpl(apiUrl('/auth/api/v1/auth/user/guest-register'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      displayName,
      device: {
        deviceName: 'iPhone',
        deviceType: 'PARTICIPANT_DEVICE_TYPE_WEB_DESKTOP',
      },
    }),
  });
  const json = await parseJsonResponse(resp, 'guest-register');
  if (!json.accessToken) throw new Error('guest-register: empty accessToken');
  return json.accessToken;
}

export async function joinRoom({ accessToken, roomID, fetchImpl = fetch }) {
  const resp = await fetchImpl(apiUrl(`/api-room/api/v1/room/${encodeURIComponent(roomID)}/join`), {
    method: 'POST',
    headers: bearerHeaders(accessToken, { 'Content-Type': 'application/json' }),
    body: '{}',
  });
  await parseJsonResponse(resp, 'join-room');
}

export async function getConnectionDetails({ accessToken, roomID, displayName = DEFAULT_DISPLAY_NAME, fetchImpl = fetch }) {
  const url = apiUrl(`/api-room-manager/v2/room/${encodeURIComponent(roomID)}/connection-details?deviceType=PARTICIPANT_DEVICE_TYPE_WEB_DESKTOP&displayName=${encodeURIComponent(displayName)}`);
  const resp = await fetchImpl(url, { headers: bearerHeaders(accessToken) });
  const json = await parseJsonResponse(resp, 'connection-details');
  if (!json.roomToken || !json.serverUrl) throw new Error(`connection-details: missing roomToken/serverUrl`);
  return { roomToken: json.roomToken, serverUrl: json.serverUrl };
}

export async function authAndGetToken({ room, displayName = DEFAULT_DISPLAY_NAME, accessToken, fetchImpl = fetch } = {}) {
  const roomID = parseWbStreamRoomID(room);
  if (!roomID) throw new Error('room is required for browser joiner');
  const token = accessToken || await registerGuest({ displayName, fetchImpl });
  await joinRoom({ accessToken: token, roomID, fetchImpl });
  const details = await getConnectionDetails({ accessToken: token, roomID, displayName, fetchImpl });
  return { roomID, accessToken: token, ...details };
}
