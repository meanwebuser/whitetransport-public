/**
 * YTP TimerReceiver — polls the channel on a fixed interval.
 *
 * Simple, works everywhere. No long polling, no webhooks needed.
 * Just calls channel.poll(cursor, 0) on a timer.
 *
 * Latency = intervalMs (default 3000ms).
 * Good enough for delay-tolerant transport.
 */

import type { Channel, ChannelMessage, Receiver } from './compose';
import type { ProviderCursor } from './provider';

export interface TimerReceiverConfig {
  intervalMs?: number;  // default 3000
  label?: string;
}

export class TimerReceiver implements Receiver {
  readonly id: string;

  private config: TimerReceiverConfig;
  private channel: Channel | null = null;

  constructor(config: TimerReceiverConfig = {}) {
    this.config = config;
    this.id = config.label ? `timer-${config.label}` : 'timer';
  }

  async init(channel: Channel): Promise<void> {
    this.channel = channel;
    console.log(`[TimerReceiver:${this.id}] Will poll every ${this.recommendedIntervalMs()}ms`);
  }

  async stop(): Promise<void> {
    this.channel = null;
  }

  async scan(cursor: ProviderCursor): Promise<{
    messages: ChannelMessage[];
    nextCursor: ProviderCursor;
  }> {
    if (!this.channel) return { messages: [], nextCursor: cursor };

    // timeout=0 → instant return, no long poll
    return this.channel.poll(cursor, 0);
  }

  recommendedIntervalMs(): number {
    return this.config.intervalMs ?? 3000;
  }
}
