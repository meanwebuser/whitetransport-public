/**
 * YTP VKChannel — VK API client for the composable provider system.
 *
 * Handles:
 *   - sendMessage → messages.send
 *   - poll → messages.getHistory (timeout=0) or Long Poll API (timeout>0)
 *   - uploadDocument → docs.getMessagesUploadServer → upload → docs.save
 *   - uploadPhoto → photos.getMessagesUploadServer → upload → photos.saveMessagesPhoto
 *   - downloadAttachment → fetch URL → buffer
 */

import type { Channel, ChannelMessage, ChannelAttachment, ChannelCapabilities } from './compose';

export interface VKChannelConfig {
  accessToken: string;
  peerId: string;
  apiVersion?: string;
  label?: string;
}

interface VKLongPollState {
  server: string;
  key: string;
  ts: string;
}

export class VKChannel implements Channel {
  readonly id: string;

  private config: VKChannelConfig;
  private apiVersion: string;
  private userId: number | null = null;
  private longPollState: VKLongPollState | null = null;
  private longPollBuffer: ChannelMessage[] = [];

  constructor(config: VKChannelConfig) {
    this.config = config;
    this.apiVersion = config.apiVersion || '5.131';
    this.id = config.label ? `vk-${config.label}` : 'vk';
  }

  // ── Lifecycle ──────────────────────────────────────────────────────────

  async init(): Promise<void> {
    const userInfo = await this.callApi('users.get', {});
    if (userInfo.response?.[0]) {
      this.userId = userInfo.response[0].id;
      console.log(`[VKChannel:${this.id}] Authenticated as user_id=${this.userId}`);
    } else {
      throw new Error(`VK auth failed: ${JSON.stringify(userInfo)}`);
    }

    // Pre-fetch long poll server
    await this.refreshLongPollServer();
  }

  async destroy(): Promise<void> {
    this.longPollState = null;
  }

  // ── Send ───────────────────────────────────────────────────────────────

  async sendMessage(text: string, attachment?: string): Promise<{ messageId: string; timestamp: number }> {
    const params: Record<string, any> = {
      peer_id: this.config.peerId,
      message: text,
      random_id: Math.floor(Math.random() * 2147483647),
    };
    if (attachment) {
      params.attachment = attachment;
    }

    const resp = await this.callApi('messages.send', params);
    if (resp.error) {
      throw new Error(`VK sendMessage failed: ${resp.error.error_msg}`);
    }

    return {
      messageId: String(resp.response),
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
      // If timeout > 0, use Long Poll first
      if (timeout > 0 && this.longPollState) {
        const updates = await this.doLongPoll(Math.min(timeout, 25));
        for (const msg of updates) {
          this.longPollBuffer.push(msg);
        }
      }

      // Always also check getHistory as fallback/complement
      const history = await this.callApi('messages.getHistory', {
        peer_id: this.config.peerId,
        count: 50,
      });

      let maxId = sinceId;
      const allMessages: ChannelMessage[] = [];

      // Add long poll messages
      const lpNew = this.longPollBuffer.filter(m => Number(m.id) > sinceId);
      allMessages.push(...lpNew);

      // Add history messages
      if (history.response?.items) {
        for (const msg of history.response.items) {
          if (msg.id > sinceId) {
            // Deduplicate with long poll
            const alreadyHave = allMessages.find(m => m.id === String(msg.id));
            if (!alreadyHave) {
              allMessages.push(this.parseHistoryMessage(msg));
            }
            maxId = Math.max(maxId, msg.id);
          }
        }
      }

      // Update max ID from all messages
      for (const m of allMessages) {
        maxId = Math.max(maxId, Number(m.id));
      }

      // Clear consumed long poll buffer
      this.longPollBuffer = this.longPollBuffer.filter(m => Number(m.id) <= sinceId);

      // Deduplicate
      const seen = new Set<string>();
      const deduped = allMessages.filter(m => {
        if (seen.has(m.id)) return false;
        seen.add(m.id);
        return true;
      });

      // Sort by timestamp
      deduped.sort((a, b) => a.timestamp - b.timestamp);

      return { messages: deduped, nextCursor: String(maxId) };
    } catch (err) {
      console.error(`[VKChannel:${this.id}] Poll error:`, err);
      return { messages: [], nextCursor: since };
    }
  }

  // ── Document Upload ────────────────────────────────────────────────────

  async uploadDocument(data: Buffer, filename: string): Promise<string> {
    // Step 1: Get upload URL
    const uploadInfo = await this.callApi('docs.getMessagesUploadServer', {
      peer_id: this.config.peerId,
      type: 'doc',
    });

    if (!uploadInfo.response?.upload_url) {
      throw new Error(`VK doc upload URL failed: ${JSON.stringify(uploadInfo)}`);
    }

    // Step 2: Upload file
    const uploadResult = await this.uploadFile(uploadInfo.response.upload_url, data, filename);

    if (!uploadResult.file) {
      throw new Error(`VK doc upload failed: ${JSON.stringify(uploadResult)}`);
    }

    // Step 3: Save document
    const saveResult = await this.callApi('docs.save', {
      file: uploadResult.file,
      title: filename,
      tags: 'ytp',
    });

    if (!saveResult.response?.doc) {
      throw new Error(`VK doc save failed: ${JSON.stringify(saveResult)}`);
    }

    return `doc${saveResult.response.doc.owner_id}_${saveResult.response.doc.id}`;
  }

  // ── Photo Upload ───────────────────────────────────────────────────────

  async uploadPhoto(data: Buffer, filename: string): Promise<string> {
    const uploadInfo = await this.callApi('photos.getMessagesUploadServer', {
      peer_id: this.config.peerId,
    });

    if (!uploadInfo.response?.upload_url) {
      throw new Error(`VK photo upload URL failed: ${JSON.stringify(uploadInfo)}`);
    }

    const uploadResult = await this.uploadFile(uploadInfo.response.upload_url, data, filename);

    if (!uploadResult.photo || !uploadResult.server || !uploadResult.hash) {
      throw new Error(`VK photo upload failed: ${JSON.stringify(uploadResult)}`);
    }

    const saveResult = await this.callApi('photos.saveMessagesPhoto', {
      photo: uploadResult.photo,
      server: uploadResult.server,
      hash: uploadResult.hash,
    });

    if (!saveResult.response?.[0]) {
      throw new Error(`VK photo save failed: ${JSON.stringify(saveResult)}`);
    }

    return `photo${saveResult.response[0].owner_id}_${saveResult.response[0].id}`;
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
      maxTextBytes: 4096,
      supportsDocuments: true,
      supportsPhotos: true,
      supportsLongPoll: true,
      supportsWebhook: true,
      minSendIntervalMs: 350,
      maxBurst: 3,
    };
  }

  // ── Private: VK API call ──────────────────────────────────────────────

  private async callApi(method: string, params: Record<string, any>): Promise<any> {
    const url = new URL(`https://api.vk.com/method/${method}`);
    url.searchParams.append('access_token', this.config.accessToken);
    url.searchParams.append('v', this.apiVersion);

    for (const [key, value] of Object.entries(params)) {
      if (value !== undefined && value !== null) {
        url.searchParams.append(key, String(value));
      }
    }

    const resp = await fetch(url.toString(), {
      method: 'GET',
      headers: { 'Accept': 'application/json' },
    });

    return resp.json();
  }

  // ── Private: Long Poll ────────────────────────────────────────────────

  private async refreshLongPollServer(): Promise<void> {
    const resp = await this.callApi('messages.getLongPollServer', {
      need_pts: 1,
      lp_version: 3,
    });
    if (resp.response) {
      this.longPollState = resp.response;
    }
  }

  private async doLongPoll(timeout: number): Promise<ChannelMessage[]> {
    if (!this.longPollState) return [];

    try {
      const url = `https://${this.longPollState.server}?act=a_check&key=${encodeURIComponent(this.longPollState.key)}&ts=${this.longPollState.ts}&wait=${timeout}&mode=2&version=3`;

      const resp = await fetch(url, {
        signal: AbortSignal.timeout((timeout + 5) * 1000),
      });

      const data = await resp.json() as any;

      if (data.failed) {
        await this.refreshLongPollServer();
        return [];
      }

      if (data.ts) {
        this.longPollState.ts = data.ts;
      }

      const messages: ChannelMessage[] = [];

      for (const u of data.updates || []) {
        if (u[0] === 4) { // Type 4 = new message
          const fromId = u[6] ? Number(u[6].split(':')[1]) || u[3] : u[3];
          const isSelf = fromId === this.userId;

          messages.push({
            id: String(u[1]),
            timestamp: u[4] * 1000,
            text: u[5] || '',
            fromSelf: isSelf,
            attachments: [],  // Long Poll doesn't include attachment details
          });
        }
      }

      return messages;
    } catch {
      return [];
    }
  }

  // ── Private: Parse history message ─────────────────────────────────────

  private parseHistoryMessage(msg: any): ChannelMessage {
    const attachments: ChannelAttachment[] = [];

    if (msg.attachments) {
      for (const att of msg.attachments) {
        if (att.type === 'doc' && att.doc) {
          attachments.push({
            type: 'doc',
            id: `doc${att.doc.owner_id}_${att.doc.id}`,
            url: att.doc.url,
            ownerId: att.doc.owner_id,
            raw: att.doc,
          });
        } else if (att.type === 'photo' && att.photo) {
          const sizes = att.photo.sizes || [];
          const largest = sizes.reduce((best: any, s: any) =>
            (!best || s.width * s.height > best.width * best.height) ? s : best, null);

          attachments.push({
            type: 'photo',
            id: `photo${att.photo.owner_id}_${att.photo.id}`,
            url: largest?.url,
            ownerId: att.photo.owner_id,
            raw: att.photo,
          });
        }
      }
    }

    return {
      id: String(msg.id),
      timestamp: msg.date * 1000,
      text: msg.text || '',
      fromSelf: msg.from_id === this.userId,
      attachments,
    };
  }

  // ── Private: File Upload ───────────────────────────────────────────────

  private async uploadFile(uploadUrl: string, data: Buffer, filename: string): Promise<any> {
    const FormData = await import('form-data');
    const form = new FormData.default();
    form.append('file', data, { filename, contentType: 'application/octet-stream' });

    const resp = await fetch(uploadUrl, {
      method: 'POST',
      body: form as any,
      headers: (form as any).getHeaders(),
    });

    return resp.json();
  }
}
