/**
 * YTP CryptoBox — encrypt / decrypt Bundles.
 *
 * Uses XChaCha20-Poly1305 (preferred) or AES-256-GCM.
 * Skeleton uses Node crypto createCipheriv for AES-GCM as a stand-in.
 */

import { randomBytes, createCipheriv, createDecipheriv, createHash } from 'crypto';
import type { Bundle } from '../protocol/bundle';
import type { Envelope } from '../protocol/envelope';
import { envelopeToWire } from '../protocol/envelope';

const NONCE_SIZE = 24; // XChaCha20 uses 24 bytes; AES-GCM uses 12
const TAG_SIZE = 16;

export interface SealedEnvelope {
  nonce: string;       // base64url
  ciphertext: string;  // base64url
  tag: string;         // base64url
}

/**
 * Encrypt a Bundle into sealed envelope fields.
 * Returns (nonce, ciphertext, tag) as base64url strings.
 */
export function encryptBundle(
  key: Buffer,
  bundle: Bundle,
  aad?: Buffer,
): SealedEnvelope {
  const nonce = randomBytes(12); // AES-GCM nonce
  const plaintext = Buffer.from(JSON.stringify(bundle), 'utf-8');

  const cipher = createCipheriv('aes-256-gcm', key, nonce, aad ? { authTagLength: TAG_SIZE } : undefined);
  if (aad) cipher.setAAD(aad);

  const encrypted = Buffer.concat([cipher.update(plaintext), cipher.final()]);
  const tag = cipher.getAuthTag();

  return {
    nonce: nonce.toString('base64url'),
    ciphertext: encrypted.toString('base64url'),
    tag: tag.toString('base64url'),
  };
}

/**
 * Decrypt a sealed envelope back into a Bundle.
 */
export function decryptBundle(
  key: Buffer,
  sealed: SealedEnvelope,
  aad?: Buffer,
): Bundle | null {
  try {
    const nonce = Buffer.from(sealed.nonce, 'base64url');
    const ciphertext = Buffer.from(sealed.ciphertext, 'base64url');
    const tag = Buffer.from(sealed.tag, 'base64url');

    const decipher = createDecipheriv('aes-256-gcm', key, nonce, aad ? { authTagLength: TAG_SIZE } : undefined);
    if (aad) decipher.setAAD(aad);
    decipher.setAuthTag(tag);

    const decrypted = Buffer.concat([decipher.update(ciphertext), decipher.final()]);
    const raw = JSON.parse(decrypted.toString('utf-8'));
    // Validate minimal structure
    if (!raw || !Array.isArray(raw.operations)) return null;
    return raw as Bundle;
  } catch {
    return null;
  }
}

/**
 * Compute a content hash for bundle dedup / checkpoint state.
 */
export function hashBundle(bundle: Bundle): string {
  const json = JSON.stringify(bundle.operations);
  return createHash('sha256').update(json).digest('hex').slice(0, 32);
}
