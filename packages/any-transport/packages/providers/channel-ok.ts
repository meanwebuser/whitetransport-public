/**
 * YTP OKChannel — Odnoklassniki API client for the composable provider system.
 *
 * Handles:
 *   - sendMessage → messages.send (with MD5 request signing)
 *   - poll → messages.getHistory (timer) or messages.getLongPollHistory (longpoll)
 *   - uploadDocument → docs.getUploadUrl → upload → docs.commit
 *   - uploadPhoto → photosV2.getUploadUrl → upload → photosV2.commit
 *   - downloadAttachment → fetch URL → buffer
 */

import { createHash } from 'crypto';
import type { Channel, ChannelMessage, ChannelAttachment, ChannelCapabilities } from './compose';

export interface OKChannelConfig {
  accessToken: string;
  applicationKey: string;
  sessionSecretKey: string;
  chatId: string;           // e.g. 'chat:${WT_OK_CHAT_ID}'
  recipientId?: string;
  label?: string;
}

export class OKChannel implements Channel {
  readonly id: string;

  private config: OKChannelConfig;

  constructor(config: OKChannelConfig) {
    this.config = config;
    this.id = config.label ? `ok-${config.label}` : 'ok';
  }

  // ── Lifecycle ──────────────────────────────────────────────────────────

  async init(): Promise<void> {
    const userInfo = await this.callApi('users.getCurrentUser', {});
    if (userInfo.uid) {
      console.log(`[OKChannel:${this.id}] Authenticated as uid=${userInfo.uid}`);
    } else {
      throw new Error(`OK auth failed: ${JSON.stringify(userInfo)}`);
    }
  }

  async destroy(): Promise<void> {}

  // ── Send ───────────────────────────────────────────────────────────────

  async sendMessage(text: string, attachment?: string): Promise<{ messageId: string; timestamp: number }> {
    const recipientId = this.config.recipientId || this.config.chatId.replace('chat:', '');
    const params: Record<string, any> = {
      chat: this.config.chatId,
      recipient_id: recipientId,
      message: text,
    };
    if (attachment) {
      params.attachment = attachment;
    }

    const resp = await this.callApi('messages.send', params);
    if (resp.error_code) {
      throw new Error(`OK sendMessage failed: ${resp.error_msg}`);
    }

    return {
      messageId: String(resp),
      timestamp: Date.now(),
    };
  }

  // ── Poll ───────────────────────────────────────────────────────────────

  async poll(since: string | number | null, timeout: number): Promise<{
    messages: ChannelMessage[];
    nextCursor: string | number | null;
  }> {
    const sinceId = since ? Number(since) : 0;

    try {
      let allMessages: ChannelMessage[] = [];

      // If timeout > 0, try long poll first
      if (timeout > 0) {
        try {
          const lpResult = await this.callApi('messages.getLongPollHistory', {
            chat: this.config.chatId,
            last_msg_id: sinceId > 0 ? sinceId : undefined,
          });

          if (lpResult.messages) {
            for (const msg of lpResult.messages) {
              allMessages.push(this.parseMessage(msg));
            }
          }
        } catch {
          // Long poll failed, fall through to getHistory
        }
      }

      // Always also check getHistory
      const history = await this.callApi('messages.getHistory', {
        chat: this.config.chatId,
        count: 50,
        from_msg_id: sinceId > 0 ? sinceId : undefined,
      });

      if (history.messages) {
        for (const msg of history.messages) {
          if (msg.messageId > sinceId) {
            const alreadyHave = allMessages.find(m => m.id === String(msg.messageId));
            if (!alreadyHave) {
              allMessages.push(this.parseMessage(msg));
            }
          }
        }
      }

      let maxId = sinceId;
      for (const m of allMessages) {
        maxId = Math.max(maxId, Number(m.id));
      }

      // Deduplicate
      const seen = new Set<string>();
      const deduped = allMessages.filter(m => {
        if (seen.has(m.id)) return false;
        seen.add(m.id);
        return true;
      });

      deduped.sort((a, b) => a.timestamp - b.timestamp);

      return { messages: deduped, nextCursor: String(maxId) };
    } catch (err) {
      console.error(`[OKChannel:${this.id}] Poll error:`, err);
      return { messages: [], nextCursor: since };
    }
  }

  // ── Document Upload ────────────────────────────────────────────────────

  async uploadDocument(data: Buffer, filename: string): Promise<string> {
    const uploadInfo = await this.callApi('docs.getUploadUrl', {
      count: 1,
      group_id: this.config.chatId.includes('C') ? this.config.chatId.replace('chat:C', '') : undefined,
    });

    if (!uploadInfo.upload_url) {
      throw new Error(`OK doc upload URL failed: ${JSON.stringify(uploadInfo)}`);
    }

    const FormData = await import('form-data');
    const form = new FormData.default();
    form.append('file1', data, { filename, contentType: 'application/octet-stream' });

    const uploadResp = await fetch(uploadInfo.upload_url, {
      method: 'POST',
      body: form as any,
      headers: (form as any).getHeaders(),
    });

    const uploadResult = await uploadResp.json() as any;
    if (!uploadResult.docs?.[0]) {
      throw new Error(`OK doc upload failed: ${JSON.stringify(uploadResult)}`);
    }

    // Commit the uploaded document
    const commitResult = await this.callApi('docs.commit', {
      doc_id: uploadResult.docs[0].id,
      token: uploadResult.docs[0].token,
    });

    const docId = commitResult?.id;
    if (!docId) {
      throw new Error(`OK doc commit failed: ${JSON.stringify(commitResult)}`);
    }

    return JSON.stringify([{ type: 'doc', id: docId }]);
  }

  // ── Photo Upload ───────────────────────────────────────────────────────

  async uploadPhoto(data: Buffer, filename: string): Promise<string> {
    const uploadInfo = await this.callApi('photosV2.getUploadUrl', {
      count: 1,
      gid: this.config.chatId.includes('C') ? this.config.chatId.replace('chat:C', '') : undefined,
    });

    if (!uploadInfo.upload_url) {
      throw new Error(`OK photo upload URL failed: ${JSON.stringify(uploadInfo)}`);
    }

    const FormData = await import('form-data');
    const form = new FormData.default();
    form.append('pic1', data, { filename, contentType: 'image/png' });

    const uploadResp = await fetch(uploadInfo.upload_url, {
      method: 'POST',
      body: form as any,
      headers: (form as any).getHeaders(),
    });

    const uploadResult = await uploadResp.json() as any;
    if (!uploadResult.photos?.[0]) {
      throw new Error(`OK photo upload failed: ${JSON.stringify(uploadResult)}`);
    }

    const commitResult = await this.callApi('photosV2.commit', {
      photo_id: uploadResult.photos[0].photo_id,
      token: uploadResult.photos[0].token,
    });

    const photoId = commitResult?.photos?.[0]?.id;
    if (!photoId) {
      throw new Error(`OK photo commit failed: ${JSON.stringify(commitResult)}`);
    }

    return JSON.stringify([{ type: 'photo', id: photoId }]);
  }

  // ── Download Attachment ────────────────────────────────────────────────

  async downloadAttachment(attachment: ChannelAttachment): Promise<Buffer> {
    if (!attachment.url) throw new Error('No attachment URL');
    const resp = await fetch(attachment.url);
    return Buffer.from(await resp.arrayBuffer());
  }

  // ── Capabilities ──────────────────────────────────────────────────────

  caps(): ChannelCapabilities {
    return {
      maxTextBytes: 4000,
      supportsDocuments: true,
      supportsPhotos: true,
      supportsLongPoll: true,
      supportsWebhook: true,
      minSendIntervalMs: 250,
      maxBurst: 5,
    };
  }

  // ── Private: OK API with MD5 signing ──────────────────────────────────

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

    // MD5 signature
    const sortedKeys = Object.keys(allParams).sort();
    const sigString = sortedKeys.map(k => `${k}=${allParams[k]}`).join('');
    const sig = createHash('md5')
      .update(sigString + this.config.sessionSecretKey)
      .digest('hex')
      .toLowerCase();

    allParams.sig = sig;

    const url = new URL('https://api.ok.ru/fb.do');
    for (const [key, value] of Object.entries(allParams)) {
      url.searchParams.append(key, value);
    }

    const resp = await fetch(url.toString(), {
      method: 'GET',
      headers: { 'Accept': 'application/json' },
    });

    return resp.json();
  }

  // ── Private: Parse message ─────────────────────────────────────────────

  private parseMessage(msg: any): ChannelMessage {
    const attachments: ChannelAttachment[] = [];

    if (msg.attachment) {
      const att = msg.attachment;
      if (att.type === 'doc' && att.doc) {
        attachments.push({
          type: 'doc',
          id: String(att.doc.id),
          url: att.doc.url,
          raw: att.doc,
        });
      } else if (att.type === 'photo' && att.photo) {
        const sizes = att.photo.sizes || [];
        const largest = sizes.reduce((best: any, s: any) =>
          (!best || s.width * s.height > best.width * best.height) ? s : best, null);

        attachments.push({
          type: 'photo',
          id: String(att.photo.id),
          url: largest?.url,
          raw: att.photo,
        });
      }
    }

    return {
      id: String(msg.messageId),
      timestamp: msg.date * 1000,
      text: msg.text || '',
      fromSelf: msg.senderId === msg.authorId,
      attachments,
    };
  }
}
