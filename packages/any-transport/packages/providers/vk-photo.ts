/**
 * YTP VKPhotoProvider — VK photo upload transport (JPEG re-encoded).
 *
 * WARNING: VK re-encodes photos to JPEG, which DESTROYS raw pixel data!
 * This provider uses JPEG-resistant steganography to encode data in the
 * DCT domain of JPEG images, surviving re-encoding.
 *
 * For reliable data transport, use VKDocumentProvider instead — it uploads
 * documents which are NOT re-encoded.
 *
 * Photo transport is useful when:
 *   - Document uploads are blocked/restricted
 *   - You want messages to look like normal photo messages
 *   - Steganographic cover is needed
 *
 * Current implementation: encodes data in message text + attaches a
 * cover image. Full DCT-domain steganography is TODO.
 *
 * Rate limits: 3 req/s per token (same as text).
 */

import type { Provider, ProviderCursor, ProviderMessage, OutboundFrame, AppendResult, ProviderCapabilities, RateHint } from './provider';
import { getImageStats } from './image-codec';

interface VKPhotoConfig {
  accessToken: string;
  peerId: string;
  apiVersion?: string;
  label?: string;
}

interface VKLongPollServer {
  server: string;
  key: string;
  ts: string;
}

export class VKPhotoProvider implements Provider {
  readonly id: string;

  private config: VKPhotoConfig;
  private apiVersion: string;
  private longPollServer: VKLongPollServer | null = null;
  private lastMessageId = 0;
  private messageBuffer: ProviderMessage[] = [];
  private userId: number | null = null;

  constructor(config: VKPhotoConfig) {
    this.config = config;
    this.apiVersion = config.apiVersion || '5.131';
    this.id = config.label ? `vk-photo-${config.label}` : 'vk-photo';
  }

  async start(): Promise<void> {
    const userInfo = await this.callApi('users.get', {});
    if (userInfo.response && userInfo.response[0]) {
      this.userId = userInfo.response[0].id;
      console.log(`[VKPhotoProvider:${this.id}] Authenticated as user_id=${this.userId}`);
    } else {
      throw new Error(`VK auth failed: ${JSON.stringify(userInfo)}`);
    }

    await this.updateLongPollServer();

    const history = await this.callApi('messages.getHistory', {
      peer_id: this.config.peerId,
      count: 1,
    });
    if (history.response?.items?.length > 0) {
      this.lastMessageId = history.response.items[0].id;
    }

    console.log(`[VKPhotoProvider:${this.id}] Photo mode (JPEG re-encoded — use VKDocumentProvider for reliable transport)`);
  }

  async stop(): Promise<void> {}

  async append(frame: OutboundFrame): Promise<AppendResult> {
    // Photo upload + message text
    // Since VK re-encodes photos to JPEG, we send data as text in the
    // message body and attach a visual cover photo for steganographic cover.
    // TODO: Implement DCT-domain steganography for JPEG-resistant encoding

    const chunks = this.splitMessage(frame.text, 4090);

    let lastResult: AppendResult | null = null;

    for (let i = 0; i < chunks.length; i++) {
      const chunk = chunks[i];

      // Try to upload a cover photo
      let attachment = '';
      try {
        const uploadInfo = await this.callApi('photos.getMessagesUploadServer', {
          peer_id: this.config.peerId,
        });

        if (uploadInfo.response?.upload_url) {
          // Generate a small cover image (1x1 pixel PNG)
          const coverPng = this.generateCoverImage();
          const uploadResult = await this.uploadPhoto(uploadInfo.response.upload_url, coverPng, `cover_${Date.now()}.png`);

          if (uploadResult.photo && uploadResult.server && uploadResult.hash) {
            const saveResult = await this.callApi('photos.saveMessagesPhoto', {
              photo: uploadResult.photo,
              server: uploadResult.server,
              hash: uploadResult.hash,
            });

            if (saveResult.response?.[0]) {
              attachment = `photo${saveResult.response[0].owner_id}_${saveResult.response[0].id}`;
            }
          }
        }
      } catch (err: any) {
        // Cover photo upload failed — send as text-only
        console.warn(`[VKPhotoProvider:${this.id}] Cover photo failed: ${err.message}`);
      }

      // Send message with data in text + optional cover photo
      const label = chunks.length > 1 ? `[YTP:IMG:${i}/${chunks.length}] ` : '[YTP:IMG] ';
      const sendParams: Record<string, any> = {
        peer_id: this.config.peerId,
        message: label + chunk,
        random_id: Math.floor(Math.random() * 2147483647),
      };
      if (attachment) {
        sendParams.attachment = attachment;
      }

      const sendResult = await this.callApi('messages.send', sendParams);

      if (sendResult.error) {
        throw new Error(`VK send failed: ${sendResult.error.error_msg}`);
      }

      lastResult = {
        messageId: String(sendResult.response),
        timestamp: Date.now(),
      };

      if (i < chunks.length - 1) {
        await this.sleep(350);
      }
    }

    return lastResult!;
  }

  async scan(cursor: ProviderCursor): Promise<{
    messages: ProviderMessage[];
    nextCursor: ProviderCursor;
  }> {
    const sinceId = cursor ? Number(cursor) : this.lastMessageId;

    try {
      if (this.longPollServer) {
        const updates = await this.longPoll(2);
        for (const update of updates) {
          if (update.type === 4 && update.object) {
            const msg = update.object;
            if (msg.peer_id === Number(this.config.peerId) || msg.from_id === Number(this.config.peerId)) {
              this.messageBuffer.push({
                id: String(msg.id),
                timestamp: msg.date * 1000,
                text: msg.text || '',
                fromSelf: msg.from_id === this.userId,
              });
              this.lastMessageId = Math.max(this.lastMessageId, msg.id);
            }
          }
        }
      }

      const history = await this.callApi('messages.getHistory', {
        peer_id: this.config.peerId,
        count: 20,
      });

      if (history.response?.items) {
        for (const msg of history.response.items) {
          if (msg.id > sinceId && !this.messageBuffer.find(m => m.id === String(msg.id))) {
            this.messageBuffer.push({
              id: String(msg.id),
              timestamp: msg.date * 1000,
              text: msg.text || '',
              fromSelf: msg.from_id === this.userId,
            });
            this.lastMessageId = Math.max(this.lastMessageId, msg.id);
          }
        }
      }

      const newMessages = this.messageBuffer.filter(m => Number(m.id) > sinceId);
      this.messageBuffer = [];

      const seen = new Set<string>();
      const deduped = newMessages.filter(m => {
        if (seen.has(m.id)) return false;
        seen.add(m.id);
        return true;
      });

      return { messages: deduped, nextCursor: String(this.lastMessageId) };
    } catch (err) {
      console.error(`[VKPhotoProvider:${this.id}] Scan error:`, err);
      return { messages: [], nextCursor: cursor };
    }
  }

  capabilities(): ProviderCapabilities {
    return {
      maxTextBytes: 4096,
      supportsAttachments: true,
      supportsEdit: true,
      supportsDelete: true,
      supportsMessageIds: true,
      supportsServerTimestamp: true,
      minSafeSendIntervalMs: 350,
      recommendedPollIntervalMs: 2000,
    };
  }

  rateHint(): RateHint {
    return {
      minIntervalMs: 350,
      burst: 3,
      mode: 'moderate',
    };
  }

  // ── Cover Image Generator ──────────────────────────────────────────────

  private generateCoverImage(): Buffer {
    // Minimal 1x1 white PNG
    // PNG signature + IHDR + IDAT + IEND
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

  // ── Photo Upload Helper ────────────────────────────────────────────────

  private async uploadPhoto(uploadUrl: string, imageData: Buffer, filename: string): Promise<any> {
    const FormData = await import('form-data');
    const form = new FormData.default();
    form.append('photo', imageData, { filename, contentType: 'image/png' });

    const resp = await fetch(uploadUrl, {
      method: 'POST',
      body: form as any,
      headers: (form as any).getHeaders(),
    });

    return resp.json();
  }

  // ── VK API ─────────────────────────────────────────────────────────────

  private async callApi(method: string, params: Record<string, any>): Promise<any> {
    const url = new URL(`https://api.vk.com/method/${method}`);
    url.searchParams.append('access_token', this.config.accessToken);
    url.searchParams.append('v', this.apiVersion);
    for (const [key, value] of Object.entries(params)) {
      if (value !== undefined && value !== null) {
        url.searchParams.append(key, String(value));
      }
    }
    const resp = await fetch(url.toString(), { method: 'GET', headers: { 'Accept': 'application/json' } });
    return resp.json();
  }

  private async updateLongPollServer(): Promise<void> {
    const resp = await this.callApi('messages.getLongPollServer', { need_pts: 1, lp_version: 3 });
    if (resp.response) this.longPollServer = resp.response;
  }

  private async longPoll(timeout: number = 2): Promise<any[]> {
    if (!this.longPollServer) return [];
    try {
      const url = `https://${this.longPollServer.server}?act=a_check&key=${encodeURIComponent(this.longPollServer.key)}&ts=${this.longPollServer.ts}&wait=${timeout}&mode=2&version=3`;
      const resp = await fetch(url, { signal: AbortSignal.timeout((timeout + 5) * 1000) });
      const data = await resp.json() as any;
      if (data.failed) { await this.updateLongPollServer(); return []; }
      if (data.ts) this.longPollServer.ts = data.ts;
      return (data.updates || []).map((u: any[]) => ({
        type: u[0], object: {
          id: u[1], flags: u[2],
          from_id: u[6] ? Number(u[6].split(':')[1]) || u[3] : u[3],
          date: u[4], text: u[5] || '', peer_id: u[3], attachments: u[7] || [],
        },
      }));
    } catch { return []; }
  }

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
