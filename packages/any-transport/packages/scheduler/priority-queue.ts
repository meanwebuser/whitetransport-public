/**
 * YTP Priority Queue — orders outbound operations by priority.
 *
 * P0 = control (ACK, CLOSE, KEY_UPDATE)
 * P1 = interactive (terminal, small HTTP)
 * P2 = normal
 * P3 = bulk (files, large responses)
 * P4 = maintenance (checkpoint, stats)
 */

import type { OutboundFrame } from '../providers/provider';

interface QueueEntry {
  frame: OutboundFrame;
  enqueuedAt: number;
  retries: number;
}

export class PriorityQueue {
  private queues: Map<number, QueueEntry[]> = new Map();

  constructor() {
    for (let p = 0; p <= 4; p++) {
      this.queues.set(p, []);
    }
  }

  enqueue(frame: OutboundFrame): void {
    const priority = Math.max(0, Math.min(4, frame.priority));
    const queue = this.queues.get(priority)!;
    queue.push({ frame, enqueuedAt: Date.now(), retries: 0 });
  }

  dequeue(): QueueEntry | null {
    // Always pick from highest priority (lowest number) first
    for (let p = 0; p <= 4; p++) {
      const queue = this.queues.get(p)!;
      if (queue.length > 0) {
        return queue.shift()!;
      }
    }
    return null;
  }

  /** Re-queue for retransmission */
  requeue(entry: QueueEntry): void {
    entry.retries++;
    const priority = Math.max(0, Math.min(4, entry.frame.priority));
    this.queues.get(priority)!.push(entry);
  }

  get size(): number {
    let total = 0;
    for (const queue of this.queues.values()) {
      total += queue.length;
    }
    return total;
  }

  get stats(): Record<number, number> {
    const result: Record<number, number> = {};
    for (const [p, queue] of this.queues) {
      result[p] = queue.length;
    }
    return result;
  }

  /** Remove expired entries */
  purgeExpired(): number {
    const now = Date.now();
    let purged = 0;
    for (const queue of this.queues.values()) {
      const before = queue.length;
      const kept = queue.filter(e => e.frame.deadline > now);
      queue.length = 0;
      queue.push(...kept);
      purged += before - queue.length;
    }
    return purged;
  }
}
