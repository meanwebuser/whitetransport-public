/**
 * YTP PhotoEncoder — encode data as photo attachments with steganographic cover.
 *
 * WARNING: VK and OK re-encode photos to JPEG, which DESTROYS raw pixel data!
 * This encoder sends data in message text + attaches a visual cover photo.
 * Full DCT-domain steganography (JPEG-resistant) is NOT yet implemented.
 *
 * For reliable data transport, use DocumentEncoder instead.
 * PhotoEncoder is useful when:
 *   - Document uploads are blocked/restricted
 *   - You want messages to look like normal photo messages
 *   - Steganographic cover is needed
 */

import type { Channel, Encoder } from './compose';
import type { OutboundFrame, AppendResult, ProviderMessage } from './provider';

export interface PhotoEncoderConfig {
  label?: string;
}

export class PhotoEncoder implements Encoder {
  readonly id: string;

  private config: PhotoEncoderConfig;

  constructor(config: PhotoEncoderConfig = {}) {
    this.config = config;
    this.id = config.label ? `photo-${config.label}` : 'photo';
  }

  async encode(frame: OutboundFrame, channel: Channel): Promise<AppendResult> {
    const maxBytes = channel.caps().maxTextBytes - 20;

    // Split text into chunks
    const chunks: string[] = [];
    if (frame.text.length <= maxBytes) {
      chunks.push(frame.text);
    } else {
      let offset = 0;
      let partIdx = 0;
      while (offset < frame.text.length) {
        const header = `[YTP:IMG:${partIdx}] `;
        const chunkSize = Math.min(maxBytes - header.length, frame.text.length - offset);
        chunks.push(header + frame.text.slice(offset, offset + chunkSize));
        offset += chunkSize;
        partIdx++;
      }
    }

    let lastResult: AppendResult | null = null;

    for (let i = 0; i < chunks.length; i++) {
      let attachment: string | undefined;

      // Try to attach a cover photo
      if (channel.uploadPhoto) {
        try {
          const coverPng = this.generateCoverImage();
          attachment = await channel.uploadPhoto(coverPng, `cover_${Date.now()}.png`);
        } catch (err: any) {
          console.warn(`[PhotoEncoder:${this.id}] Cover photo failed: ${err.message}`);
        }
      }

      const result = await channel.sendMessage(chunks[i], attachment);
      lastResult = { messageId: result.messageId, timestamp: result.timestamp };

      if (i < chunks.length - 1) {
        await this.sleep(channel.caps().minSendIntervalMs);
      }
    }

    return lastResult!;
  }

  async decode(raw: import('./compose').ChannelMessage, _channel: Channel): Promise<ProviderMessage> {
    // For photo mode, data is in the text (steganographic cover is just visual)
    return {
      id: raw.id,
      timestamp: raw.timestamp,
      text: raw.text,
      fromSelf: raw.fromSelf,
    };
  }

  maxPayloadBytes(): number {
    return 4076; // 4096 - header, same as text
  }

  // ── Private ────────────────────────────────────────────────────────────

  private generateCoverImage(): Buffer {
    // Minimal 1x1 white PNG
    return Buffer.from([
      0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG sig
      0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, // IHDR
      0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // 1x1
      0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, // RGB
      0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41, // IDAT
      0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00, // compressed
      0x00, 0x00, 0x02, 0x00, 0x01, 0xE2, 0x21, 0xBC, // data
      0x33, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, // IEND
      0x44, 0xAE, 0x42, 0x60, 0x82,
    ]);
  }

  private sleep(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms));
  }
}
