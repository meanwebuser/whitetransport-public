/**
 * YTP Retransmit — conservative retransmission of unacknowledged envelopes.
 *
 * RTO (retransmission timeout) should be large:
 *   fast provider: 3–10 seconds
 *   slow provider: 30–120 seconds
 *
 * We use exponential backoff with a cap.
 */

import type { OutboundFrame } from '../providers/provider';

export interface InflightEntry {
  frame: OutboundFrame;
  sentAt: number;
  lastRetransmitAt: number;
  retries: number;
  maxRetries: number;
}

export class RetransmitManager {
  private inflight: Map<number, InflightEntry> = new Map(); // seq -> entry
  private baseRtoMs: number;
  private maxRtoMs: number;

  constructor(baseRtoMs = 5000, maxRtoMs = 120_000) {
    this.baseRtoMs = baseRtoMs;
    this.maxRtoMs = maxRtoMs;
  }

  /** Track a newly sent envelope */
  track(seq: number, frame: OutboundFrame, maxRetries = 5): void {
    const now = Date.now();
    this.inflight.set(seq, {
      frame,
      sentAt: now,
      lastRetransmitAt: now,
      retries: 0,
      maxRetries,
    });
  }

  /** Acknowledge received seq numbers */
  ack(receivedUpTo: number, missing: number[] = []): void {
    // Remove everything up to receivedUpTo that's NOT in missing
    const missingSet = new Set(missing);
    for (const [seq] of this.inflight) {
      if (seq <= receivedUpTo && !missingSet.has(seq)) {
        this.inflight.delete(seq);
      }
    }
  }

  /** Get frames that need retransmission */
  getRetransmits(): InflightEntry[] {
    const now = Date.now();
    const result: InflightEntry[] = [];

    for (const entry of this.inflight.values()) {
      const rto = this.computeRto(entry.retries);
      if (now - entry.lastRetransmitAt >= rto) {
        if (entry.retries < entry.maxRetries) {
          entry.retries++;
          entry.lastRetransmitAt = now;
          result.push(entry);
        } else {
          // Max retries exceeded — give up
        }
      }
    }

    return result;
  }

  /** Exponential backoff RTO */
  private computeRto(retries: number): number {
    const rto = this.baseRtoMs * Math.pow(2, retries);
    return Math.min(rto, this.maxRtoMs);
  }

  get inflightCount(): number {
    return this.inflight.size;
  }

  /** Remove expired entries */
  purgeExpired(): number {
    const now = Date.now();
    let purged = 0;
    for (const [seq, entry] of this.inflight) {
      if (entry.frame.deadline < now) {
        this.inflight.delete(seq);
        purged++;
      }
    }
    return purged;
  }
}
