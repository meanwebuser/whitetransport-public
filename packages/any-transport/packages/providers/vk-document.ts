/**
 * YTP VKDocumentProvider — VK document upload transport for maximum bandwidth.
 *
 * KEY INSIGHT: VK photos are re-encoded to JPEG, destroying pixel data.
 * VK documents (docs.upload) are NOT re-encoded — data preserved exactly!
 *
 * Strategy: Encode binary data into PNG pixels → upload as VK document →
 * recipient downloads document → decodes PNG → extracts data.
 *
 * A 256x256 PNG carries ~192KB of data.
 * A 1024x1024 PNG carries ~3MB of data.
 *
 * Flow:
 *   1. Encode data into PNG pixel data (image-codec)
 *   2. Upload via docs.getMessagesUploadServer → upload → docs.save
 *   3. Send message with doc attachment
 *   4. On receive: download doc, decode PNG, extract data
 *
 * Rate limits: 3 req/s per token, but ~50x more data per request vs text.
 * Effective throughput (256x256): ~576 KB/s per token
 * Effective throughput (1024x1024): ~9 MB/s per token
 */

import type { Provider, ProviderCursor, ProviderMessage, OutboundFrame, AppendResult, ProviderCapabilities, RateHint } from './provider';
import {
  splitIntoChunks, encodeToPNG, decodeDataFromPNG, getImageStats
} from './image-codec';

interface VKDocumentConfig {
  accessToken: string;
  peerId: string;
  apiVersion?: string;
  label?: string;
  imageWidth?: number;     // default 256
  imageHeight?: number;    // default 256
}

interface VKLongPollServer {
  server: string;
  key: string;
  ts: string;
}

export class VKDocumentProvider implements Provider {
  readonly id: string;

  private config: VKDocumentConfig;
  private apiVersion: string;
  private longPollServer: VKLongPollServer | null = null;
  private lastMessageId = 0;
  private messageBuffer: ProviderMessage[] = [];
  private userId: number | null = null;
  private imageWidth: number;
  private imageHeight: number;

  constructor(config: VKDocumentConfig) {
    this.config = config;
    this.apiVersion = config.apiVersion || '5.131';
    this.id = config.label ? `vk-doc-${config.label}` : 'vk-doc';
    this.imageWidth = config.imageWidth || 256;
    this.imageHeight = config.imageHeight || 256;
  }

  async start(): Promise<void> {
    const userInfo = await this.callApi('users.get', {});
    if (userInfo.response && userInfo.response[0]) {
      this.userId = userInfo.response[0].id;
      console.log(`[VKDocumentProvider:${this.id}] Authenticated as user_id=${this.userId}`);
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

    const stats = getImageStats(this.imageWidth, this.imageHeight);
    console.log(`[VKDocumentProvider:${this.id}] Document mode: ${stats.maxPayloadKB}KB per image (${this.imageWidth}x${this.imageHeight})`);
  }

  async stop(): Promise<void> {}

  async append(frame: OutboundFrame): Promise<AppendResult> {
    const payload = Buffer.from(frame.text, 'utf-8');
    const chunks = splitIntoChunks(payload, this.imageWidth, this.imageHeight);

    let lastResult: AppendResult | null = null;

    for (let i = 0; i < chunks.length; i++) {
      const chunk = chunks[i];
      const pngBuffer = encodeToPNG(chunk);

      // Step 1: Get document upload server
      const uploadInfo = await this.callApi('docs.getMessagesUploadServer', {
        peer_id: this.config.peerId,
        type: 'doc',
      });

      if (!uploadInfo.response?.upload_url) {
        throw new Error(`VK doc upload URL failed: ${JSON.stringify(uploadInfo)}`);
      }

      // Step 2: Upload document to VK server
      const uploadResult = await this.uploadDocument(
        uploadInfo.response.upload_url,
        pngBuffer,
        `ytp_${Date.now()}_${i}.png`
      );

      if (!uploadResult.file) {
        throw new Error(`VK doc upload failed: ${JSON.stringify(uploadResult)}`);
      }

      // Step 3: Save document to VK
      const saveResult = await this.callApi('docs.save', {
        file: uploadResult.file,
        title: `ytp_${Date.now()}_${i}.png`,
        tags: 'ytp',
      });

      if (!saveResult.response?.doc) {
        throw new Error(`VK doc save failed: ${JSON.stringify(saveResult)}`);
      }

      const docId = `doc${saveResult.response.doc.owner_id}_${saveResult.response.doc.id}`;

      // Step 4: Send message with document attachment
      const label = `[YTP:DOC:${i}/${chunks.length}]`;
      const sendResult = await this.callApi('messages.send', {
        peer_id: this.config.peerId,
        message: label,
        attachment: docId,
        random_id: Math.floor(Math.random() * 2147483647),
      });

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
              if (msg.attachments) {
                const text = await this.extractAttachmentData(msg);
                this.messageBuffer.push({
                  id: String(msg.id),
                  timestamp: msg.date * 1000,
                  text: text || msg.text || '',
                  fromSelf: msg.from_id === this.userId,
                });
              } else {
                this.messageBuffer.push({
                  id: String(msg.id),
                  timestamp: msg.date * 1000,
                  text: msg.text || '',
                  fromSelf: msg.from_id === this.userId,
                });
              }
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
            let text = msg.text || '';
            if (msg.attachments) {
              const attachmentData = await this.extractAttachmentDataFromHistory(msg);
              if (attachmentData) text = attachmentData;
            }
            this.messageBuffer.push({
              id: String(msg.id),
              timestamp: msg.date * 1000,
              text,
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
      console.error(`[VKDocumentProvider:${this.id}] Scan error:`, err);
      return { messages: [], nextCursor: cursor };
    }
  }

  capabilities(): ProviderCapabilities {
    const stats = getImageStats(this.imageWidth, this.imageHeight);
    return {
      maxTextBytes: stats.maxPayloadPerImage,
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
      mode: 'aggressive',
    };
  }

  // ── Document Upload ────────────────────────────────────────────────────

  private async uploadDocument(uploadUrl: string, fileData: Buffer, filename: string): Promise<any> {
    const FormData = await import('form-data');
    const form = new FormData.default();
    form.append('file', fileData, {
      filename,
      contentType: 'image/png',
    });

    const resp = await fetch(uploadUrl, {
      method: 'POST',
      body: form as any,
      headers: (form as any).getHeaders(),
    });

    return resp.json();
  }

  // ── Attachment Download & Decode ────────────────────────────────────────

  private async extractAttachmentData(msg: any): Promise<string | null> {
    if (!msg.attachments) return null;
    for (const att of msg.attachments) {
      if (att.type === 'doc' && att.doc) {
        try { return await this.decodeDocumentAttachment(att.doc); } catch {}
      }
    }
    return null;
  }

  private async extractAttachmentDataFromHistory(msg: any): Promise<string | null> {
    if (!msg.attachments) return null;
    for (const att of msg.attachments) {
      if (att.type === 'doc' && att.doc) {
        try { return await this.decodeDocumentAttachment(att.doc); } catch {}
      }
    }
    return null;
  }

  private async decodeDocumentAttachment(doc: any): Promise<string> {
    if (!doc.url) throw new Error('No doc URL');
    const resp = await fetch(doc.url);
    const buffer = Buffer.from(await resp.arrayBuffer());

    try {
      const decoded = decodeDataFromPNG(buffer);
      if (decoded.crcOk) {
        console.log(`[VKDocumentProvider:${this.id}] Decoded: chunk ${decoded.chunkIndex}/${decoded.totalChunks}, ${decoded.payload.length}B`);
      }
      return decoded.payload.toString('utf-8');
    } catch {
      return buffer.toString('utf-8');
    }
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

  private sleep(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms));
  }
}
