/**
 * YTP Identity — long-term node identity and ephemeral key management.
 *
 * Uses X25519 for key exchange and Ed25519 for signing.
 * In production this wraps libsodium; for the skeleton we use Node crypto.
 */

import { randomBytes, createHash } from 'crypto';

export interface NodeIdentity {
  nodeId: string;          // short public identifier
  publicKeyEd: string;     // base64url Ed25519 public key
  secretKeyEd: string;     // base64url Ed25519 secret key (stored in OS vault)
  publicKeyX: string;      // base64url X25519 public key (for DH)
  secretKeyX: string;      // base64url X25519 secret key
}

export function generateNodeId(): string {
  return createHash('blake2b256') // fallback to sha256 if blake2 not available
    .update(randomBytes(32))
    .digest('hex')
    .slice(0, 16);
}

/**
 * Generate a new node identity.
 * In production, secretKey goes to OS credential vault, not this object.
 */
export function generateIdentity(): NodeIdentity {
  // Placeholder — real implementation uses libsodium crypto_sign_keypair + crypto_sign_ed25519_pk_to_curve25519
  const seed = randomBytes(32);
  const nodeId = generateNodeId();
  return {
    nodeId,
    publicKeyEd: Buffer.from(randomBytes(32)).toString('base64url'),
    secretKeyEd: Buffer.from(randomBytes(64)).toString('base64url'),
    publicKeyX: Buffer.from(randomBytes(32)).toString('base64url'),
    secretKeyX: Buffer.from(randomBytes(32)).toString('base64url'),
  };
}

/**
 * Derive a short session tag from two node IDs (deterministic ordering).
 */
export function sessionTag(myNodeId: string, peerNodeId: string): string {
  const sorted = [myNodeId, peerNodeId].sort();
  return createHash('sha256')
    .update(sorted.join(':'))
    .digest('hex')
    .slice(0, 16);
}
