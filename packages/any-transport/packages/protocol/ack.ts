/**
 * YTP Ack — lightweight acknowledgement state.
 *
 * Instead of ACK-ing every single envelope, the receiver periodically
 * sends an AckState that covers a range plus a bitmap of gaps.
 */

export interface AckState {
  direction: 'a2b' | 'b2a';
  /** All seq numbers up to (and including) this have been received */
  receivedUpTo: number;
  /** Specific seq numbers that are missing in [0..receivedUpTo] */
  missing: number[];
  /** Optional compact bitmap: bit i === 1 means receivedUpTo - i is present */
  bitmap?: string;
}

export function ackStateToOp(ack: AckState) {
  return {
    op: 'ack-state' as const,
    id: crypto.randomUUID(),
    direction: ack.direction,
    receivedUpTo: ack.receivedUpTo,
    missing: ack.missing,
    bitmap: ack.bitmap,
  };
}

/**
 * Compact bitmap encoder — converts an array of "received" seq numbers
 * into a base64url bitmap string relative to `receivedUpTo`.
 */
export function encodeBitmap(receivedUpTo: number, received: Set<number>): string {
  const bytes: number[] = [];
  for (let i = 0; i <= receivedUpTo; i++) {
    const byteIdx = Math.floor(i / 8);
    const bitIdx = i % 8;
    if (received.has(i)) {
      bytes[byteIdx] = (bytes[byteIdx] ?? 0) | (1 << bitIdx);
    }
  }
  return Buffer.from(bytes).toString('base64url');
}
