/**
 * YTP MemoryProvider — in-memory provider for local loopback testing.
 *
 * Milestone 1: verify that the core protocol works before touching
 * any real chat API.
 */

import type { Provider, ProviderCursor, ProviderMessage, OutboundFrame, AppendResult, ProviderCapabilities, RateHint } from './provider';

export class MemoryProvider implements Provider {
  readonly id = 'memory';

  private messages: ProviderMessage[] = [];
  private nextId = 1;

  async start(): Promise<void> {
    // no-op
  }

  async stop(): Promise<void> {
    this.messages = [];
  }

  async append(frame: OutboundFrame): Promise<AppendResult> {
    const id = String(this.nextId++);
    const msg: ProviderMessage = {
      id,
      timestamp: Date.now(),
      text: frame.text,
      fromSelf: false, // memory provider doesn't distinguish
    };
    this.messages.push(msg);
    return { messageId: id, timestamp: msg.timestamp };
  }

  async scan(cursor: ProviderCursor): Promise<{
    messages: ProviderMessage[];
    nextCursor: ProviderCursor;
  }> {
    const offset = cursor ? Number(cursor) : 0;
    const newMessages = this.messages.slice(offset);
    return {
      messages: newMessages,
      nextCursor: String(this.messages.length),
    };
  }

  capabilities(): ProviderCapabilities {
    return {
      maxTextBytes: 65536,
      supportsAttachments: false,
      supportsEdit: false,
      supportsDelete: false,
      supportsMessageIds: true,
      supportsServerTimestamp: true,
      minSafeSendIntervalMs: 0,
      recommendedPollIntervalMs: 50,
    };
  }

  rateHint(): RateHint {
    return {
      minIntervalMs: 0,
      burst: 100,
      mode: 'aggressive',
    };
  }

  /** Test helper: clear all messages */
  clear(): void {
    this.messages = [];
    this.nextId = 1;
  }
}
