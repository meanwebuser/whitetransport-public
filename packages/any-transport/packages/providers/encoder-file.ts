/**
 * YTP FileEncoder — encode data as raw files on cloud storage.
 *
 * Unlike TextEncoder (which puts data in message text) or DocumentEncoder
 * (which packs data into PNG pixels as document attachments), FileEncoder
 * uploads raw binary data directly as files. This is optimal for cloud
 * storage channels (Yandex Disk, Mail.ru Cloud, SberCloud) where:
 *
 *   - There are no "messages" — just files in directories
 *   - File upload/download is the native operation
 *   - Bandwidth is limited by API rate, not message size
 *   - Files can be much larger than messaging attachments
 *
 * Strategy:
 *   - encode: Upload frame.text as a raw file via channel.uploadDocument()
 *             or channel.sendMessage() as fallback
 *   - decode: Download file content via channel.downloadAttachment()
 *             or return text as-is
 *
 * Chunking: Cloud storage allows much larger files, so we use
 * larger chunk sizes (default 256KB) for better throughput.
 */

import type { Channel, Encoder } from './compose';
import type { OutboundFrame, AppendResult, ProviderMessage } from './provider';
import { splitIntoChunks, encodeToPNG, decodeDataFromPNG } from './image-codec';

export interface FileEncoderConfig {
  /** Maximum bytes per file chunk. Default: 256KB */
  maxChunkBytes?: number;
  /** Use PNG pixel encoding for binary data (survives re-encoding). Default: false */
  usePixelEncoding?: boolean;
  /** Image dimensions for pixel encoding. Default: 512x512 (~786KB payload) */
  imageWidth?: number;
  imageHeight?: number;
  label?: string;
}

export class FileEncoder implements Encoder {
  readonly id: string;

  private config: FileEncoderConfig;
  private maxChunk: number;
  private usePixelEncoding: boolean;

  constructor(config: FileEncoderConfig = {}) {
    this.config = config;
    this.maxChunk = config.maxChunkBytes || 256 * 1024; // 256KB
    this.usePixelEncoding = config.usePixelEncoding ?? false;
    this.id = config.label ? `file-${config.label}` : 'file';
  }

  async encode(frame: OutboundFrame, channel: Channel): Promise<AppendResult> {
    const payload = Buffer.from(frame.text, 'utf-8');

    if (this.usePixelEncoding && channel.uploadDocument) {
      // Use PNG pixel encoding (data hidden in image pixels)
      return this.encodePixelMode(payload, channel);
    }

    // Raw file mode — split if needed, upload directly
    if (payload.length <= this.maxChunk || !channel.uploadDocument) {
      // Single file — use sendMessage (which uploads as text file for cloud channels)
      return channel.sendMessage(frame.text);
    }

    // Multi-chunk file upload
    const chunks = this.splitBuffer(payload, this.maxChunk);
    let lastResult: AppendResult | null = null;

    for (let i = 0; i < chunks.length; i++) {
      const chunk = chunks[i];
      const filename = `ytp_${Date.now()}_${i}.bin`;

      if (channel.uploadDocument) {
        const attachment = await channel.uploadDocument(chunk, filename);
        const result = await channel.sendMessage(
          `[YTP:FILE:${i}/${chunks.length}]`,
          attachment,
        );
        lastResult = { messageId: result.messageId, timestamp: result.timestamp };
      } else {
        // Fallback to sendMessage with base64
        const b64 = chunk.toString('base64');
        const result = await channel.sendMessage(`[YTP:B64:${i}/${chunks.length}] ${b64}`);
        lastResult = { messageId: result.messageId, timestamp: result.timestamp };
      }

      if (i < chunks.length - 1) {
        await this.sleep(channel.caps().minSendIntervalMs);
      }
    }

    return lastResult!;
  }

  async decode(raw: import('./compose').ChannelMessage, channel: Channel): Promise<ProviderMessage> {
    const text = raw.text;

    // Check if it's a base64-encoded chunk
    const b64Match = text.match(/^\[YTP:B64:(\d+)\/(\d+)\]\s*(.+)$/s);
    if (b64Match) {
      try {
        const decoded = Buffer.from(b64Match[3], 'base64').toString('utf-8');
        return {
          id: raw.id,
          timestamp: raw.timestamp,
          text: decoded,
          fromSelf: raw.fromSelf,
        };
      } catch {
        // Fall through
      }
    }

    // Check if message has document attachments with pixel-encoded data
    if (raw.attachments.length > 0 && channel.downloadAttachment) {
      for (const att of raw.attachments) {
        if (att.type === 'doc') {
          try {
            const buffer = await channel.downloadAttachment(att);
            // Try PNG pixel decoding first
            try {
              const decoded = decodeDataFromPNG(buffer);
              if (decoded.crcOk) {
                return {
                  id: raw.id,
                  timestamp: raw.timestamp,
                  text: decoded.payload.toString('utf-8'),
                  fromSelf: raw.fromSelf,
                };
              }
            } catch {
              // Not a PNG — try as raw binary
              return {
                id: raw.id,
                timestamp: raw.timestamp,
                text: buffer.toString('utf-8'),
                fromSelf: raw.fromSelf,
              };
            }
          } catch {
            // Download failed
          }
        }
      }
    }

    // Strip YTP headers from file markers
    const fileMatch = text.match(/^\[YTP:FILE:\d+\/\d+\]$/);
    if (fileMatch) {
      // The actual data is in the attachment (already handled above)
      return {
        id: raw.id,
        timestamp: raw.timestamp,
        text: '',
        fromSelf: raw.fromSelf,
      };
    }

    // Return as-is (plain text file content from cloud storage)
    return {
      id: raw.id,
      timestamp: raw.timestamp,
      text,
      fromSelf: raw.fromSelf,
    };
  }

  maxPayloadBytes(): number {
    if (this.usePixelEncoding) {
      const w = this.config.imageWidth || 512;
      const h = this.config.imageHeight || 512;
      return w * h * 3 - 48; // header overhead
    }
    return this.maxChunk;
  }

  // ── Private: Pixel encoding mode ───────────────────────────────────────

  private async encodePixelMode(payload: Buffer, channel: Channel): Promise<AppendResult> {
    const w = this.config.imageWidth || 512;
    const h = this.config.imageHeight || 512;

    const chunks = splitIntoChunks(payload, w, h);
    let lastResult: AppendResult | null = null;

    for (let i = 0; i < chunks.length; i++) {
      const pngBuffer = encodeToPNG(chunks[i]);
      const filename = `ytp_${Date.now()}_${i}.png`;

      const attachment = await channel.uploadDocument!(pngBuffer, filename);
      const result = await channel.sendMessage(
        `[YTP:PIX:${i}/${chunks.length}]`,
        attachment,
      );

      lastResult = { messageId: result.messageId, timestamp: result.timestamp };

      if (i < chunks.length - 1) {
        await this.sleep(channel.caps().minSendIntervalMs);
      }
    }

    return lastResult!;
  }

  // ── Private: Helpers ───────────────────────────────────────────────────

  private splitBuffer(data: Buffer, maxChunkSize: number): Buffer[] {
    if (data.length <= maxChunkSize) return [data];

    const chunks: Buffer[] = [];
    let offset = 0;

    while (offset < data.length) {
      const end = Math.min(offset + maxChunkSize, data.length);
      chunks.push(data.slice(offset, end));
      offset = end;
    }

    return chunks;
  }

  private sleep(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms));
  }
}
