/**
 * YTP TextEncoder — encode data as plain text in message body.
 *
 * Simple, works everywhere. Splits long messages into chunks.
 * Bandwidth: ~4KB per message (limited by API text limits).
 */

import type { Channel, Encoder } from './compose';
import type { OutboundFrame, AppendResult, ProviderMessage } from './provider';

export interface TextEncoderConfig {
  /** Maximum bytes per message before splitting. Default: auto-detect from channel */
  maxChunkBytes?: number;
  /** Chunk header format. Default: '[YTP:{part}] ' */
  headerFormat?: string;
  label?: string;
}

export class TextEncoder implements Encoder {
  readonly id: string;

  private config: TextEncoderConfig;

  constructor(config: TextEncoderConfig = {}) {
    this.config = config;
    this.id = config.label ? `text-${config.label}` : 'text';
  }

  async encode(frame: OutboundFrame, channel: Channel): Promise<AppendResult> {
    const maxBytes = this.config.maxChunkBytes || channel.caps().maxTextBytes - 20;
    const chunks = this.splitMessage(frame.text, maxBytes);

    let lastResult: AppendResult | null = null;

    for (let i = 0; i < chunks.length; i++) {
      const result = await channel.sendMessage(chunks[i]);

      lastResult = {
        messageId: result.messageId,
        timestamp: result.timestamp,
      };

      // Rate limit between chunks
      if (i < chunks.length - 1) {
        await this.sleep(channel.caps().minSendIntervalMs);
      }
    }

    return lastResult!;
  }

  async decode(raw: import('./compose').ChannelMessage, _channel: Channel): Promise<ProviderMessage> {
    // For text encoding, the message text IS the data
    return {
      id: raw.id,
      timestamp: raw.timestamp,
      text: raw.text,
      fromSelf: raw.fromSelf,
    };
  }

  maxPayloadBytes(): number {
    return this.config.maxChunkBytes || 4076; // 4096 - header
  }

  // ── Private ────────────────────────────────────────────────────────────

  private splitMessage(text: string, maxLen: number): string[] {
    if (text.length <= maxLen) return [text];

    const chunks: string[] = [];
    let offset = 0;
    let partIdx = 0;

    while (offset < text.length) {
      const header = `[YTP:${partIdx}] `;
      const chunkSize = Math.min(maxLen - header.length, text.length - offset);
      chunks.push(header + text.slice(offset, offset + chunkSize));
      offset += chunkSize;
      partIdx++;
    }

    return chunks;
  }

  private sleep(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms));
  }
}
