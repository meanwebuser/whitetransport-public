/**
 * YTP LongPollReceiver — uses long polling for near-real-time message delivery.
 *
 * Calls channel.poll(cursor, timeout) with a long timeout.
 * The channel internally handles VK Long Poll / TG getUpdates / OK Long Poll.
 *
 * Latency: 1-3 seconds.
 * Requires a persistent connection (not suitable for serverless/Vercel).
 */

import type { Channel, ChannelMessage, Receiver } from './compose';
import type { ProviderCursor } from './provider';

export interface LongPollReceiverConfig {
  /** Long poll timeout in seconds. Default: 25 */
  timeout?: number;
  label?: string;
}

export class LongPollReceiver implements Receiver {
  readonly id: string;

  private config: LongPollReceiverConfig;
  private channel: Channel | null = null;

  constructor(config: LongPollReceiverConfig = {}) {
    this.config = config;
    this.id = config.label ? `longpoll-${config.label}` : 'longpoll';
  }

  async init(channel: Channel): Promise<void> {
    this.channel = channel;

    if (!channel.caps().supportsLongPoll) {
      console.warn(`[LongPollReceiver:${this.id}] Channel ${channel.id} doesn't advertise long poll support — using timer fallback`);
    }

    console.log(`[LongPollReceiver:${this.id}] Long poll timeout=${this.config.timeout ?? 25}s`);
  }

  async stop(): Promise<void> {
    this.channel = null;
  }

  async scan(cursor: ProviderCursor): Promise<{
    messages: ChannelMessage[];
    nextCursor: ProviderCursor;
  }> {
    if (!this.channel) return { messages: [], nextCursor: cursor };

    const timeout = this.config.timeout ?? 25;
    return this.channel.poll(cursor, timeout);
  }

  recommendedIntervalMs(): number {
    // With long poll, we can poll more frequently since the server holds the connection
    return 1500;
  }
}
