/**
 * YTP Bundle — a collection of Operations packed into one Envelope.
 *
 * Instead of sending each tiny TCP segment as a separate chat message,
 * we accumulate operations over a short window (500–3000 ms), compress,
 * encrypt, and send them as a single Bundle.
 */

import type { Operation } from './operation';

export interface Bundle {
  bundleId: string;       // BLAKE3 hash of compressed payload (post-compression)
  operations: Operation[];
  // Metadata filled by the sender
  createdAt: number;      // epoch-ms
  deadline: number;       // epoch-ms — remote should drop if past deadline
}

// ── Bundle builder ─────────────────────────────────────────────────────
export class BundleBuilder {
  private ops: Operation[] = [];
  private deadlineMs: number;

  constructor(deadlineMs = 30_000) {
    this.deadlineMs = deadlineMs;
  }

  add(op: Operation): this {
    this.ops.push(op);
    return this;
  }

  build(): Bundle {
    const now = Date.now();
    return {
      bundleId: '', // filled after compression by CryptoBox
      operations: [...this.ops],
      createdAt: now,
      deadline: now + this.deadlineMs,
    };
  }

  get size(): number {
    return this.ops.length;
  }

  clear(): void {
    this.ops = [];
  }
}

// ── Bundle parser ──────────────────────────────────────────────────────
export function parseBundle(raw: unknown): Bundle | null {
  if (!raw || typeof raw !== 'object') return null;
  const b = raw as Record<string, unknown>;
  if (typeof b.bundleId !== 'string') return null;
  if (!Array.isArray(b.operations)) return null;
  return {
    bundleId: b.bundleId,
    operations: b.operations as Operation[],
    createdAt: typeof b.createdAt === 'number' ? b.createdAt : Date.now(),
    deadline: typeof b.deadline === 'number' ? b.deadline : Date.now() + 30_000,
  };
}
