/**
 * YTP Envelope — outer wrapper for every message on the provider log.
 *
 * The envelope is the only thing the provider ever sees.  All fields
 * except ciphertext/tag are transmitted in cleartext so that the
 * receiving node can route / dedup / discard before decryption.
 */

export const YTP_MAGIC = 'YT' as const;
export const YTP_VERSION = 1;

// ── Direction ──────────────────────────────────────────────────────────
export type Direction = 'a2b' | 'b2a';

// ── Envelope kind ──────────────────────────────────────────────────────
export type EnvelopeKind =
  | 'bundle'       // carries one or more operations
  | 'ack'          // ack-state bitmap
  | 'checkpoint'   // logical GC marker
  | 'key-update';  // key-rotation trigger

// ── Priority ───────────────────────────────────────────────────────────
export type Priority = 0 | 1 | 2 | 3 | 4;
// 0 = control (ACK, CLOSE, KEY_UPDATE)
// 1 = interactive (terminal, small HTTP)
// 2 = normal
// 3 = bulk (files, large responses)
// 4 = maintenance (checkpoint, stats)

// ── Envelope ───────────────────────────────────────────────────────────
export interface Envelope {
  magic: typeof YTP_MAGIC;
  version: typeof YTP_VERSION;
  sessionId: string;
  epochId: number;
  direction: Direction;
  seq: number;           // monotonic inside (session, epoch, direction)
  createdAt: number;     // epoch-ms, set by sender
  expiresAt: number;     // epoch-ms, deadline for processing
  priority: Priority;
  kind: EnvelopeKind;
  nonce: string;         // base64url — 24 bytes for XChaCha20-Poly1305
  ciphertext: string;    // base64url — encrypted Bundle / AckState / …
  tag: string;           // base64url — 16-byte AEAD tag
}

// ── Wire helpers ───────────────────────────────────────────────────────
export function envelopeToWire(env: Envelope): string {
  // Compact text line — safe for any chat API
  // YT1:<session>.<epoch>.<dir>.<seq>.<pri>.<kind>.<nonce>.<ciphertext>.<tag>.<expires>
  const dir = env.direction === 'a2b' ? 'A' : 'B';
  const kindCode: Record<EnvelopeKind, string> = {
    bundle: 'D',
    ack: 'K',
    checkpoint: 'C',
    'key-update': 'U',
  };
  return [
    `${YTP_MAGIC}${YTP_VERSION}`,
    env.sessionId,
    env.epochId,
    dir,
    env.seq,
    env.priority,
    kindCode[env.kind],
    env.nonce,
    env.ciphertext,
    env.tag,
    env.expiresAt,
  ].join('.');
}

export function wireToEnvelope(wire: string): Envelope | null {
  const parts = wire.split('.');
  if (parts.length !== 12) return null;
  const [magicVer, sessionId, epochId, dirCode, seq, priority, kindCode, nonce, ciphertext, tag, , expiresAt] = parts;
  if (!magicVer.startsWith(YTP_MAGIC)) return null;
  const dir: Direction = dirCode === 'A' ? 'a2b' : 'b2a';
  const kindMap: Record<string, EnvelopeKind> = { D: 'bundle', K: 'ack', C: 'checkpoint', U: 'key-update' };
  const kind = kindMap[kindCode];
  if (!kind) return null;
  return {
    magic: YTP_MAGIC,
    version: YTP_VERSION,
    sessionId,
    epochId: Number(epochId),
    direction: dir,
    seq: Number(seq),
    createdAt: Date.now(), // approximated; real ts is inside ciphertext
    expiresAt: Number(expiresAt),
    priority: Number(priority) as Priority,
    kind,
    nonce,
    ciphertext,
    tag,
  };
}
