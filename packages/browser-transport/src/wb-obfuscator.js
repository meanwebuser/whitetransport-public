import { XChaCha20Poly1305 } from '@stablelib/xchacha20poly1305';
import { hash } from '@stablelib/sha256';
import { randomBytes } from '@stablelib/random';

const NONCE_LEN = 24;

export function deriveSecretFromJoinLink(joinLink = '') {
  let s = String(joinLink || '').trim().replace(/\/+$/g, '');
  const q = s.indexOf('?');
  if (q >= 0) s = s.slice(0, q);
  const h = s.indexOf('#');
  if (h >= 0) s = s.slice(0, h);
  const slash = s.lastIndexOf('/');
  if (slash >= 0) s = s.slice(slash + 1);
  if (s.startsWith('wbstream://')) s = s.slice('wbstream://'.length);
  return new TextEncoder().encode(s);
}

export class WbObfuscator {
  constructor(secret) {
    if (!secret || secret.byteLength === 0) throw new Error('WbObfuscator requires non-empty secret');
    const key = hash(secret instanceof Uint8Array ? secret : new Uint8Array(secret));
    this.aead = new XChaCha20Poly1305(key);
  }

  static fromJoinLink(joinLink) {
    return new WbObfuscator(deriveSecretFromJoinLink(joinLink));
  }

  encryptPayload(plaintext) {
    const pt = plaintext instanceof Uint8Array ? plaintext : new Uint8Array(plaintext);
    const nonce = randomBytes(NONCE_LEN);
    const sealed = this.aead.seal(nonce, pt);
    const out = new Uint8Array(nonce.byteLength + sealed.byteLength);
    out.set(nonce, 0);
    out.set(sealed, nonce.byteLength);
    return out;
  }

  decryptPayload(data) {
    const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
    if (bytes.byteLength < NONCE_LEN + 16) return null;
    const nonce = bytes.slice(0, NONCE_LEN);
    const ciphertext = bytes.slice(NONCE_LEN);
    try {
      return this.aead.open(nonce, ciphertext);
    } catch {
      return null;
    }
  }
}
