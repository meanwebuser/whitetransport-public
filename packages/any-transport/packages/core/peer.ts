/**
 * YTP Peer — represents a remote node we communicate with.
 */

export interface PeerInfo {
  nodeId: string;
  publicKeyEd: string;
  publicKeyX: string;
  providers: PeerProviderInfo[];
  pairedAt: number;
  lastSeen: number | null;
}

export interface PeerProviderInfo {
  type: string;     // 'telegram', 'vk', 'ok', etc.
  chatId?: string;
  botUsername?: string;
}

/**
 * Generate an invite code for peer pairing.
 * The invite contains the node's public key and provider info,
 * encoded as base64url JSON.
 */
export function generateInviteCode(
  nodeId: string,
  publicKeyEd: string,
  publicKeyX: string,
  providers: PeerProviderInfo[],
): string {
  const payload = {
    yt: 'invite-v1',
    node_id: nodeId,
    pubkey_ed: publicKeyEd,
    pubkey_x: publicKeyX,
    providers,
  };
  const json = JSON.stringify(payload);
  return 'ytp://invite/' + Buffer.from(json, 'utf-8').toString('base64url');
}

/**
 * Parse an invite code back into peer info.
 */
export function parseInviteCode(code: string): PeerInfo | null {
  try {
    const prefix = 'ytp://invite/';
    if (!code.startsWith(prefix)) return null;

    const base64 = code.slice(prefix.length);
    const json = Buffer.from(base64, 'base64url').toString('utf-8');
    const payload = JSON.parse(json);

    if (payload.yt !== 'invite-v1') return null;

    return {
      nodeId: payload.node_id,
      publicKeyEd: payload.pubkey_ed,
      publicKeyX: payload.pubkey_x,
      providers: payload.providers ?? [],
      pairedAt: Date.now(),
      lastSeen: null,
    };
  } catch {
    return null;
  }
}
