/**
 * URI parser for direct connection protocols supported by WhiteTransport.
 *
 * Supported schemes:
 *   wbstream://roomID[?params]
 *   dion://roomID[?params]
 *   telemost://roomID[?params]
 *   vless://uuid@host:port[?params]
 *   ssh://[user[:password]@]host[:port]
 */

export type DirectProtocol = 'wbstream' | 'dion' | 'telemost' | 'vless' | 'ssh';

export interface ParsedDirectConnection {
  protocol: DirectProtocol;
  /** Human-readable label derived from the URI. */
  label: string;
  /** Raw URI as entered. */
  rawUri: string;
  /** Target host or room ID. */
  host: string;
  /** Target port (if applicable). */
  port?: number;
  /** User (SSH). */
  user?: string;
  /** UUID / password / extra params stored as key-value pairs. */
  params: Record<string, string>;
}

const SUPPORTED_SCHEMES: DirectProtocol[] = ['wbstream', 'dion', 'telemost', 'vless', 'ssh'];

/** Detect protocol from raw input string. */
export function detectProtocol(input: string): DirectProtocol | null {
  const lower = input.trim().toLowerCase();
  for (const scheme of SUPPORTED_SCHEMES) {
    if (lower.startsWith(`${scheme}://`)) return scheme;
  }
  return null;
}

/** Parse a URI string into a structured direct-connection descriptor. */
export function parseDirectUri(uri: string): ParsedDirectConnection | null {
  const trimmed = uri.trim();
  const protocol = detectProtocol(trimmed);
  if (!protocol) return null;

  try {
    switch (protocol) {
      case 'ssh':
        return parseSsh(trimmed);
      case 'vless':
        return parseVless(trimmed);
      case 'wbstream':
      case 'dion':
      case 'telemost':
        return parseRoomUri(trimmed, protocol);
      default:
        return null;
    }
  } catch {
    return null;
  }
}

function parseRoomUri(uri: string, protocol: DirectProtocol): ParsedDirectConnection {
  const withoutScheme = uri.slice(`${protocol}://`.length);
  const [hostPart, queryPart] = withoutScheme.split('?');
  const host = decodeURIComponent(hostPart || '').replace(/\/+$/, '');
  const params = parseQuery(queryPart || '');
  return {
    protocol,
    label: `${protocolLabel(protocol)}: ${host}`,
    rawUri: uri,
    host,
    params,
  };
}

function parseVless(uri: string): ParsedDirectConnection {
  // vless://uuid@host:port?type=tcp&security=tls&path=%2Fws#name
  const withoutScheme = uri.slice('vless://'.length);
  const [authorityAndPath, fragment] = withoutScheme.split('#');
  const [authority, queryPart] = authorityAndPath.split('?');
  const [uuid, hostPort] = authority.split('@');
  const { host, port } = splitHostPort(hostPort || '', 443);
  const params = parseQuery(queryPart || '');
  if (uuid) params['uuid'] = uuid;
  const label = fragment ? decodeURIComponent(fragment) : `VLESS: ${host}:${port}`;
  return { protocol: 'vless', label, rawUri: uri, host, port, params };
}

function parseSsh(uri: string): ParsedDirectConnection {
  // ssh://user:password@host:port  or  ssh://host
  const withoutScheme = uri.slice('ssh://'.length);
  let user: string | undefined;
  let password: string | undefined;
  let hostPortPart = withoutScheme;

  const atIdx = withoutScheme.lastIndexOf('@');
  if (atIdx >= 0) {
    const userPart = withoutScheme.slice(0, atIdx);
    hostPortPart = withoutScheme.slice(atIdx + 1);
    const colonIdx = userPart.indexOf(':');
    if (colonIdx >= 0) {
      user = decodeURIComponent(userPart.slice(0, colonIdx));
      password = decodeURIComponent(userPart.slice(colonIdx + 1));
    } else {
      user = decodeURIComponent(userPart);
    }
  }

  const { host, port } = splitHostPort(hostPortPart, 22);
  const params: Record<string, string> = {};
  if (user) params['user'] = user;
  if (password) params['password'] = password;

  return {
    protocol: 'ssh',
    label: `SSH: ${user ? `${user}@` : ''}${host}${port !== 22 ? `:${port}` : ''}`,
    rawUri: uri,
    host,
    port,
    user,
    params,
  };
}

// ── Helpers ──────────────────────────────────────────────────────────

function splitHostPort(raw: string, defaultPort: number): { host: string; port: number } {
  const cleaned = raw.replace(/\/+$/, '').trim();
  // IPv6: [::1]:port
  if (cleaned.startsWith('[')) {
    const bracketEnd = cleaned.indexOf(']');
    if (bracketEnd > 0) {
      const host = cleaned.slice(1, bracketEnd);
      const rest = cleaned.slice(bracketEnd + 1);
      const portMatch = rest.match(/^:(\d+)/);
      return { host, port: portMatch ? parseInt(portMatch[1], 10) : defaultPort };
    }
  }
  const lastColon = cleaned.lastIndexOf(':');
  if (lastColon > 0 && !cleaned.slice(lastColon + 1).includes(':')) {
    const maybePort = parseInt(cleaned.slice(lastColon + 1), 10);
    if (!Number.isNaN(maybePort) && maybePort > 0 && maybePort <= 65535) {
      return { host: cleaned.slice(0, lastColon), port: maybePort };
    }
  }
  return { host: cleaned, port: defaultPort };
}

function parseQuery(qs: string): Record<string, string> {
  const params: Record<string, string> = {};
  if (!qs) return params;
  for (const pair of qs.split('&')) {
    const [key, val] = pair.split('=');
    if (key) {
      params[decodeURIComponent(key)] = val ? decodeURIComponent(val) : '';
    }
  }
  return params;
}

function protocolLabel(p: DirectProtocol): string {
  switch (p) {
    case 'wbstream': return 'WBStream';
    case 'dion': return 'DION';
    case 'telemost': return 'Telemost';
    case 'vless': return 'VLESS';
    case 'ssh': return 'SSH';
  }
}

/** Return a short protocol badge string for UI display. */
export function protocolBadge(p: DirectProtocol): string {
  switch (p) {
    case 'wbstream': return 'WB';
    case 'dion': return 'DION';
    case 'telemost': return 'Telemost';
    case 'vless': return 'VLESS';
    case 'ssh': return 'SSH';
  }
}
