/**
 * YTP OKDocumentProvider — OK document/file upload transport for maximum bandwidth.
 *
 * Unlike OKPhotoProvider which uploads via photosV2 (and may be re-encoded),
 * this provider uploads data as a file/document attachment which preserves
 * data exactly as-is.
 *
 * Strategy: Encode binary data into PNG pixels → upload as OK file/doc →
 * recipient downloads doc → decodes PNG → extracts data.
 *
 * A 256x256 PNG carries ~192KB of data.
 * A 1024x1024 PNG carries ~3MB of data.
 *
 * Flow:
 *   1. Encode data into PNG pixel data (image-codec)
 *   2. Upload via OK docs upload endpoint
 *   3. Send message with doc attachment
 *   4. On receive: download doc, decode PNG, extract data
 *
 * Rate limits: ~2-3 req/s, but ~50x more data per request vs text.
 * Effective throughput (256x256): ~480 KB/s per token
 */

import { createHash } from 'crypto';
import type { Provider, ProviderCursor, ProviderMessage, OutboundFrame, AppendResult, ProviderCapabilities, RateHint } from './provider';
import {
  splitIntoChunks, encodeToPNG, decodeDataFromPNG, getImageStats
} from './image-codec';

interface OKDocumentConfig {
  accessToken: string;
  applicationKey: string;
  sessionSecretKey: string;
  chatId: string;
  recipientId?: string;
  label?: string;
  imageWidth?: number;     // default 256
  imageHeight?: number;    // default 256
}

export class OKDocumentProvider implements Provider {
  readonly id: string;

  private config: OKDocumentConfig;
  private lastMessageId = 0;
  private messageBuffer: ProviderMessage[] = [];
  private isRunning = false;
  private imageWidth: number;
  private imageHeight: number;

  constructor(config: OKDocumentConfig) {
    this.config = config;
    this.id = config.label ? `ok-doc-${config.label}` : 'ok-doc';
    this.imageWidth = config.imageWidth || 256;
    this.imageHeight = config.imageHeight || 256;
  }

  private get apiUrl(): string {
    return 'https://api.ok.ru/fb.do';
  }

  async start(): Promise<void> {
    const userInfo = await this.callApi('users.getCurrentUser', {});
    if (userInfo.uid) {
      console.log(`[OKDocumentProvider:${this.id}] Authenticated as uid=${userInfo.uid}`);
    } else {
      throw new Error(`OK auth failed: ${JSON.stringify(userInfo)}`);
    }

    const messages = await this.callApi('messages.getHistory', {
      chat: this.config.chatId,
      count: 1,
    });

    if (messages.messages && messages.messages.length > 0) {
      this.lastMessageId = messages.messages[0].messageId;
      console.log(`[OKDocumentProvider:${this.id}] Last message ID: ${this.lastMessageId}`);
    }

    const stats = getImageStats(this.imageWidth, this.imageHeight);
    console.log(`[OKDocumentProvider:${this.id}] Document mode: ${stats.maxPayloadKB}KB per image (${this.imageWidth}x${this.imageHeight})`);

    this.isRunning = true;
  }

  async stop(): Promise<void> {
    this.isRunning = false;
  }

  async append(frame: OutboundFrame): Promise<AppendResult> {
    const payload = Buffer.from(frame.text, 'utf-8');
    const chunks = splitIntoChunks(payload, this.imageWidth, this.imageHeight);

    let lastResult: AppendResult | null = null;

    for (let i = 0; i < chunks.length; i++) {
      const chunk = chunks[i];
      const pngBuffer = encodeToPNG(chunk);

      try {
        // Step 1: Get file upload URL from OK
        const uploadInfo = await this.callApi('docs.getUploadUrl', {
          count: 1,
          group_id: this.config.chatId.includes('C') ? this.config.chatId.replace('chat:C', '') : undefined,
        });

        if (!uploadInfo.upload_url) {
          console.warn(`[OKDocumentProvider:${this.id}] Doc upload URL failed, falling back to text`);
          return this.appendViaText(frame);
        }

        // Step 2: Upload document to OK server
        const FormData = await import('form-data');
        const form = new FormData.default();
        form.append('file1', pngBuffer, {
          filename: `ytp_${Date.now()}_${i}.png`,
          contentType: 'image/png',
        });

        const uploadResp = await fetch(uploadInfo.upload_url, {
          method: 'POST',
          body: form as any,
          headers: (form as any).getHeaders(),
        });

        const uploadResult = await uploadResp.json() as any;

        if (!uploadResult.docs || !uploadResult.docs[0]) {
          console.warn(`[OKDocumentProvider:${this.id}] Doc upload failed, falling back to text`);
          return this.appendViaText(frame);
        }

        const docToken = uploadResult.docs[0].token;

        // Step 3: Commit the uploaded document
        const commitResult = await this.callApi('docs.commit', {
          doc_id: uploadResult.docs[0].id,
          token: docToken,
        });

        const docId = commitResult?.id;

        // Step 4: Send message with doc attachment
        const label = `[YTP:DOC:${i}/${chunks.length}]`;
        const sendParams: Record<string, any> = {
          chat: this.config.chatId,
          recipient_id: this.config.recipientId || this.config.chatId.replace('chat:', ''),
          message: label,
        };

        if (docId) {
          sendParams.attachment = JSON.stringify([{
            type: 'doc',
            id: docId,
          }]);
        }

        const sendResp = await this.callApi('messages.send', sendParams);

        if (sendResp.error_code) {
          throw new Error(`OK send failed: ${sendResp.error_msg}`);
        }

        lastResult = {
          messageId: String(sendResp),
          timestamp: Date.now(),
        };

        if (i < chunks.length - 1) {
          await this.sleep(400);
        }
      } catch (err: any) {
        console.warn(`[OKDocumentProvider:${this.id}] Doc upload error: ${err.message}, falling back to text`);
        return this.appendViaText(frame);
      }
    }

    return lastResult!;
  }

  // ── Text Transport (fallback) ──────────────────────────────────────────

  private async appendViaText(frame: OutboundFrame): Promise<AppendResult> {
    const recipientId = this.config.recipientId || this.config.chatId.replace('chat:', '');
    const chunks = this.splitMessage(frame.text, 3900);

    let lastResult: AppendResult | null = null;

    for (const chunk of chunks) {
      const resp = await this.callApi('messages.send', {
        chat: this.config.chatId,
        recipient_id: recipientId,
        message: chunk,
      });

      if (resp.error_code) {
        throw new Error(`OK sendMessage failed: ${resp.error_msg}`);
      }

      lastResult = {
        messageId: String(resp),
        timestamp: Date.now(),
      };

      if (chunks.length > 1) {
        await this.sleep(250);
      }
    }

    return lastResult!;
  }

  // ── Scan ───────────────────────────────────────────────────────────────

  async scan(cursor: ProviderCursor): Promise<{
    messages: ProviderMessage[];
    nextCursor: ProviderCursor;
  }> {
    const sinceId = cursor ? Number(cursor) : this.lastMessageId;

    try {
      const lpResult = await this.callApi('messages.getLongPollHistory', {
        chat: this.config.chatId,
        last_msg_id: sinceId > 0 ? sinceId : undefined,
      });

      if (lpResult.messages) {
        for (const msg of lpResult.messages) {
          let text = msg.text || '';
          if (msg.attachment) {
            try {
              const docData = await this.extractDocData(msg);
              if (docData) text = docData;
            } catch {}
          }
          this.messageBuffer.push({
            id: String(msg.messageId),
            timestamp: msg.date * 1000,
            text,
            fromSelf: msg.senderId === msg.authorId,
          });
          this.lastMessageId = Math.max(this.lastMessageId, msg.messageId);
        }
      }

      const history = await this.callApi('messages.getHistory', {
        chat: this.config.chatId,
        count: 20,
        from_msg_id: sinceId > 0 ? sinceId : undefined,
      });

      if (history.messages) {
        for (const msg of history.messages) {
          if (msg.messageId > sinceId && !this.messageBuffer.find(m => m.id === String(msg.messageId))) {
            let text = msg.text || '';

            if (msg.attachment) {
              try {
                const docData = await this.extractDocData(msg);
                if (docData) text = docData;
              } catch {}
            }

            this.messageBuffer.push({
              id: String(msg.messageId),
              timestamp: msg.date * 1000,
              text,
              fromSelf: msg.authorId === msg.senderId,
            });
            this.lastMessageId = Math.max(this.lastMessageId, msg.messageId);
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
      console.error(`[OKDocumentProvider:${this.id}] Scan error:`, err);
      return { messages: [], nextCursor: cursor };
    }
  }

  capabilities(): ProviderCapabilities {
    const stats = getImageStats(this.imageWidth, this.imageHeight);
    return {
      maxTextBytes: stats.maxPayloadPerImage,
      supportsAttachments: true,
      supportsEdit: false,
      supportsDelete: true,
      supportsMessageIds: true,
      supportsServerTimestamp: true,
      minSafeSendIntervalMs: 400,
      recommendedPollIntervalMs: 2000,
    };
  }

  rateHint(): RateHint {
    return {
      minIntervalMs: 400,
      burst: 3,
      mode: 'aggressive',
    };
  }

  // ── Document decode ────────────────────────────────────────────────────

  private async extractDocData(msg: any): Promise<string | null> {
    if (!msg.attachment) return null;

    try {
      const att = msg.attachment;
      if (att.type === 'doc' && att.doc) {
        const docUrl = att.doc.url;
        if (!docUrl) return null;

        const resp = await fetch(docUrl);
        const buffer = Buffer.from(await resp.arrayBuffer());

        try {
          const decoded = decodeDataFromPNG(buffer);
          if (decoded.crcOk) {
            console.log(`[OKDocumentProvider:${this.id}] Decoded: chunk ${decoded.chunkIndex}/${decoded.totalChunks}, ${decoded.payload.length}B`);
            return decoded.payload.toString('utf-8');
          }
        } catch {
          return buffer.toString('utf-8');
        }
      }
    } catch {}

    return null;
  }

  // ── OK API call with MD5 signing ────────────────────────────────────────

  private async callApi(method: string, params: Record<string, any>): Promise<any> {
    const allParams: Record<string, string> = {
      application_key: this.config.applicationKey,
      method,
      ...(this.config.accessToken ? { access_token: this.config.accessToken } : {}),
      format: 'json',
    };

    for (const [key, value] of Object.entries(params)) {
      if (value !== undefined && value !== null) {
        allParams[key] = String(value);
      }
    }

    const sortedKeys = Object.keys(allParams).sort();
    const sigString = sortedKeys.map(k => `${k}=${allParams[k]}`).join('');
    const sig = createHash('md5')
      .update(sigString + this.config.sessionSecretKey)
      .digest('hex')
      .toLowerCase();

    allParams.sig = sig;

    const url = new URL(this.apiUrl);
    for (const [key, value] of Object.entries(allParams)) {
      url.searchParams.append(key, value);
    }

    const resp = await fetch(url.toString(), {
      method: 'GET',
      headers: { 'Accept': 'application/json' },
    });

    return resp.json();
  }

  // ── Helpers ────────────────────────────────────────────────────────────

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
