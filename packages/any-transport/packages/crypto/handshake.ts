/**
 * YTP Handshake — key exchange between two nodes.
 *
 * Flow:
 *   A -> B:  HELLO  { nodeId, protocolVersion, ephemeralPubKeyX, providersSupported }
 *   B -> A:  HELLO_ACK { nodeId, ephemeralPubKeyX, chosenCipher }
 *   A -> B:  KEY_CONFIRM  (encrypted with derived key)
 *   B -> A:  READY
 *
 * After READY, both sides derive session keys via HKDF.
 */

import { randomBytes, createHash, createHmac } from 'crypto';

export interface HelloMessage {
  type: 'hello';
  nodeId: string;
  protocolVersion: number;
  ephemeralPubKey: string; // base64url X25519
  providersSupported: string[];
  capabilities: string[];
}

export interface HelloAckMessage {
  type: 'hello-ack';
  nodeId: string;
  ephemeralPubKey: string; // base64url X25519
  chosenCipher: 'xchacha20-poly1305' | 'aes-256-gcm';
  maxFrameBytes: number;
}

export interface KeyConfirmMessage {
  type: 'key-confirm';
  confirmHmac: string; // HMAC over shared secret to prove key derivation
}

export interface ReadyMessage {
  type: 'ready';
  accepted: boolean;
}

export type HandshakeMessage = HelloMessage | HelloAckMessage | KeyConfirmMessage | ReadyMessage;

// ── Key derivation ─────────────────────────────────────────────────────

export interface SessionKeys {
  encryptKey: Buffer;   // 32 bytes — for sending
  decryptKey: Buffer;   // 32 bytes — for receiving
  macKey: Buffer;       // 32 bytes — for metadata MAC
}

/**
 * Derive session keys from DH shared secret.
 * In production: X25519 DH → HKDF-SHA256 with session-specific info.
 */
export function deriveSessionKeys(
  sharedSecret: Buffer,
  sessionId: string,
  direction: 'a2b' | 'b2a',
): SessionKeys {
  // Simplified HKDF
  const salt = createHash('sha256').update('ytp-hkdf-salt-v1').digest();
  const info = Buffer.from(`ytp-session:${sessionId}:${direction}`);

  const prk = createHmac('sha256', salt).update(sharedSecret).digest();
  const okm = createHmac('sha256', prk).update(info).digest();

  return {
    encryptKey: okm.slice(0, 32),
    decryptKey: createHmac('sha256', prk).update(Buffer.concat([info, Buffer.from([1])])).digest().slice(0, 32),
    macKey: createHmac('sha256', prk).update(Buffer.concat([info, Buffer.from([2])])).digest().slice(0, 32),
  };
}

/**
 * Compute a confirmation HMAC to prove both sides derived the same key.
 */
export function computeConfirmHmac(macKey: Buffer, sessionId: string, nodeId: string): string {
  return createHmac('sha256', macKey)
    .update(`${sessionId}:${nodeId}`)
    .digest('base64url');
}

/**
 * Simulate X25519 DH shared secret.
 * In production: use libsodium crypto_scalarmult.
 */
export function simulateDh(mySecretKey: Buffer, peerPublicKey: Buffer): Buffer {
  // Placeholder: real implementation uses X25519
  return createHash('sha256')
    .update(Buffer.concat([mySecretKey, peerPublicKey]))
    .digest();
}
