/**
 * YTP DocumentEncoder — encode data as PNG pixels in document attachments.
 *
 * Strategy: Pack raw bytes into PNG pixel RGB channels (3 bytes per pixel),
 * upload as document attachment (NOT re-encoded by VK/OK), recipient
 * downloads and decodes.
 *
 * Bandwidth:
 *   256x256 → ~192KB per message
 *   1024x1024 → ~3MB per message
 *
 * IMPORTANT: Only works with channels that support document uploads.
 * Falls back to text if uploadDocument is not available.
 */

import type { Channel, Encoder } from './compose';
import type { OutboundFrame, AppendResult, ProviderMessage } from './provider';
import { splitIntoChunks, encodeToPNG, decodeDataFromPNG, getImageStats } from './image-codec';

export interface DocumentEncoderConfig {
  /** Image width for PNG encoding. Default: 256 */
  imageWidth?: number;
  /** Image height for PNG encoding. Default: 256 */
  imageHeight?: number;
  label?: string;
}

export class DocumentEncoder implements Encoder {
  readonly id: string;

  private config: DocumentEncoderConfig;
  private imageWidth: number;
  private imageHeight: number;

  constructor(config: DocumentEncoderConfig = {}) {
    this.config = config;
    this.imageWidth = config.imageWidth || 256;
    this.imageHeight = config.imageHeight || 256;
    this.id = config.label ? `doc-${config.label}` : 'doc';
  }

  async encode(frame: OutboundFrame, channel: Channel): Promise<AppendResult> {
    const payload = Buffer.from(frame.text, 'utf-8');

    // Check if channel supports document uploads
    if (!channel.uploadDocument) {
      console.warn(`[DocumentEncoder:${this.id}] Channel doesn't support docs, falling back to text`);
      return this.encodeViaText(frame, channel);
    }

    const chunks = splitIntoChunks(payload, this.imageWidth, this.imageHeight);
    let lastResult: AppendResult | null = null;

    for (let i = 0; i < chunks.length; i++) {
      const chunk = chunks[i];
      const pngBuffer = encodeToPNG(chunk);

      // Upload document
      const attachment = await channel.uploadDocument(pngBuffer, `ytp_${Date.now()}_${i}.png`);

      // Send message with doc attachment
      const label = `[YTP:DOC:${i}/${chunks.length}]`;
      const result = await channel.sendMessage(label, attachment);

      lastResult = {
        messageId: result.messageId,
        timestamp: result.timestamp,
      };

      if (i < chunks.length - 1) {
        await this.sleep(channel.caps().minSendIntervalMs);
      }
    }

    return lastResult!;
  }

  async decode(raw: import('./compose').ChannelMessage, channel: Channel): Promise<ProviderMessage> {
    // Check if message has document attachments
    if (raw.attachments.length > 0 && channel.downloadAttachment) {
      for (const att of raw.attachments) {
        if (att.type === 'doc') {
          try {
            const buffer = await channel.downloadAttachment(att);
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
            // Not a YTP PNG — return as text
          }
        }
      }
    }

    // No decodable attachment — return text as-is
    return {
      id: raw.id,
      timestamp: raw.timestamp,
      text: raw.text,
      fromSelf: raw.fromSelf,
    };
  }

  maxPayloadBytes(): number {
    const stats = getImageStats(this.imageWidth, this.imageHeight);
    return stats.maxPayloadPerImage;
  }

  // ── Private: Text fallback ─────────────────────────────────────────────

  private async encodeViaText(frame: OutboundFrame, channel: Channel): Promise<AppendResult> {
    const maxBytes = channel.caps().maxTextBytes - 20;
    const chunks: string[] = [];

    if (frame.text.length <= maxBytes) {
      chunks.push(frame.text);
    } else {
      let offset = 0;
      let partIdx = 0;
      while (offset < frame.text.length) {
        const header = `[YTP:${partIdx}] `;
        const chunkSize = Math.min(maxBytes - header.length, frame.text.length - offset);
        chunks.push(header + frame.text.slice(offset, offset + chunkSize));
        offset += chunkSize;
        partIdx++;
      }
    }

    let lastResult: AppendResult | null = null;
    for (let i = 0; i < chunks.length; i++) {
      const result = await channel.sendMessage(chunks[i]);
      lastResult = { messageId: result.messageId, timestamp: result.timestamp };
      if (i < chunks.length - 1) await this.sleep(channel.caps().minSendIntervalMs);
    }

    return lastResult!;
  }

  private sleep(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms));
  }
}
