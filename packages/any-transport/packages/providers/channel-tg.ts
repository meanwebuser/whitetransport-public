/**
 * YTP TGChannel — Telegram Bot API client for the composable provider system.
 *
 * Handles:
 *   - sendMessage → Bot API sendMessage
 *   - poll → getUpdates (timeout=0 for timer, timeout>0 for longpoll)
 *   - uploadDocument → sendDocument
 *   - downloadAttachment → getFile + download URL
 *
 * IMPORTANT: Bot admin in a channel/group receives BOTH:
 *   - `message` updates (group messages)
 *   - `channel_post` updates (channel posts)
 * Both are handled in poll().
 *
 * TG doesn't have getChatHistory for bots — only getUpdates/webhooks.
 */

import type { Channel, ChannelMessage, ChannelAttachment, ChannelCapabilities } from './compose';

export interface TGChannelConfig {
  botToken: string;
  chatId: string;            // group/channel ID (e.g. '-1001234567890' for channels)
  label?: string;
  /** Allowed update types. Default: ['message', 'channel_post'] */
  allowedUpdates?: string[];
}

interface TGUpdate {
  update_id: number;
  message?: {
    message_id: number;
    date: number;
    text?: string;
    from?: { id: number; is_bot: boolean };
    document?: any;
    photo?: any[];
  };
  channel_post?: {
    message_id: number;
    date: number;
    text?: string;
    author_signature?: string;
    document?: any;
    photo?: any[];
  };
}

export class TGChannel implements Channel {
  readonly id: string;

  private config: TGChannelConfig;
  private botUserId: number | null = null;
  private lastUpdateId = 0;
  private allowedUpdates: string[];

  constructor(config: TGChannelConfig) {
    this.config = config;
    this.id = config.label ? `tg-${config.label}` : 'tg';
    this.allowedUpdates = config.allowedUpdates || ['message', 'channel_post'];
  }

  private get apiUrl(): string {
    return `https://api.telegram.org/bot${this.config.botToken}`;
  }

  // ── Lifecycle ──────────────────────────────────────────────────────────

  async init(): Promise<void> {
    const resp = await fetch(`${this.apiUrl}/getMe`);
    const data = await resp.json() as { ok: boolean; result?: { id: number; username: string } };
    if (!data.ok || !data.result) {
      throw new Error(`Telegram getMe failed: ${JSON.stringify(data)}`);
    }
    this.botUserId = data.result.id;
    console.log(`[TGChannel:${this.id}] Bot connected: @${data.result.username} (id=${this.botUserId})`);
  }

  async destroy(): Promise<void> {
    // No persistent connection
  }

  // ── Send ───────────────────────────────────────────────────────────────

  async sendMessage(text: string, attachment?: string): Promise<{ messageId: string; timestamp: number }> {
    // If attachment looks like a document file_id, send via sendDocument
    if (attachment && attachment.startsWith('file:')) {
      const fileId = attachment.slice(5);
      const resp = await fetch(`${this.apiUrl}/sendDocument`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          chat_id: this.config.chatId,
          document: fileId,
          caption: text || undefined,
        }),
      });

      const data = await resp.json() as { ok: boolean; result?: { message_id: number; date: number }; description?: string };
      if (!data.ok) {
        throw new Error(`Telegram sendDocument failed: ${data.description}`);
      }

      return {
        messageId: String(data.result!.message_id),
        timestamp: data.result!.date * 1000,
      };
    }

    const resp = await fetch(`${this.apiUrl}/sendMessage`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        chat_id: this.config.chatId,
        text,
        parse_mode: undefined, // raw text
      }),
    });

    const data = await resp.json() as { ok: boolean; result?: { message_id: number; date: number }; description?: string };
    if (!data.ok) {
      throw new Error(`Telegram sendMessage failed: ${data.description}`);
    }

    return {
      messageId: String(data.result!.message_id),
      timestamp: data.result!.date * 1000,
    };
  }

  // ── Poll ───────────────────────────────────────────────────────────────

  async poll(since: string | number | null, timeout: number): Promise<{
    messages: ChannelMessage[];
    nextCursor: string | number;
  }> {
    const offset = since ? Number(since) + 1 : this.lastUpdateId + 1;

    const resp = await fetch(
      `${this.apiUrl}/getUpdates?offset=${offset}&timeout=${Math.min(timeout, 30)}&limit=100&allowed_updates=${JSON.stringify(this.allowedUpdates)}`,
    );

    const data = await resp.json() as { ok: boolean; result?: TGUpdate[] };

    if (!data.ok || !data.result) {
      return { messages: [], nextCursor: since ?? this.lastUpdateId };
    }

    const messages: ChannelMessage[] = [];
    let maxUpdateId = since ? Number(since) : this.lastUpdateId;

    for (const update of data.result) {
      maxUpdateId = Math.max(maxUpdateId, update.update_id);

      // Handle regular messages (groups)
      if (update.message) {
        const msg = update.message;
        const attachments = this.parseAttachments(msg);

        messages.push({
          id: String(msg.message_id),
          timestamp: msg.date * 1000,
          text: msg.text || '',
          fromSelf: msg.from?.id === this.botUserId,
          attachments,
        });
      }

      // Handle channel posts (public channels where bot is admin)
      if (update.channel_post) {
        const post = update.channel_post;
        const attachments = this.parseAttachments(post);

        messages.push({
          id: String(post.message_id),
          timestamp: post.date * 1000,
          text: post.text || '',
          fromSelf: false, // channel posts are never from our bot
          attachments,
        });
      }
    }

    this.lastUpdateId = maxUpdateId;

    return { messages, nextCursor: String(maxUpdateId) };
  }

  // ── Document Upload ────────────────────────────────────────────────────

  async uploadDocument(data: Buffer, filename: string): Promise<string> {
    const FormData = await import('form-data');
    const form = new FormData.default();
    form.append('document', data, { filename, contentType: 'application/octet-stream' });
    form.append('chat_id', this.config.chatId);

    const resp = await fetch(`${this.apiUrl}/sendDocument`, {
      method: 'POST',
      body: form as any,
      headers: (form as any).getHeaders(),
    });

    const result = await resp.json() as { ok: boolean; result?: { document?: { file_id: string } }; description?: string };
    if (!result.ok || !result.result?.document) {
      throw new Error(`TG sendDocument failed: ${result.description}`);
    }

    // Return as file: prefix so sendMessage can use it
    return `file:${result.result.document.file_id}`;
  }

  // ── Download Attachment ────────────────────────────────────────────────

  async downloadAttachment(attachment: ChannelAttachment): Promise<Buffer> {
    if (!attachment.raw?.file_id) throw new Error('No file_id in attachment');

    const fileResp = await fetch(`${this.apiUrl}/getFile?file_id=${attachment.raw.file_id}`);
    const fileData = await fileResp.json() as { ok: boolean; result?: { file_path: string } };

    if (!fileData.ok || !fileData.result?.file_path) {
      throw new Error(`TG getFile failed: ${JSON.stringify(fileData)}`);
    }

    const downloadUrl = `https://api.telegram.org/file/bot${this.config.botToken}/${fileData.result.file_path}`;
    const resp = await fetch(downloadUrl);
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
      minSendIntervalMs: 30,   // ~30 msg/s
      maxBurst: 30,
    };
  }

  // ── Private ────────────────────────────────────────────────────────────

  private parseAttachments(msg: any): ChannelAttachment[] {
    const attachments: ChannelAttachment[] = [];

    if (msg.document) {
      attachments.push({
        type: 'doc',
        id: `file:${msg.document.file_id}`,
        raw: msg.document,
      });
    }

    if (msg.photo && msg.photo.length > 0) {
      const largest = msg.photo[msg.photo.length - 1];
      attachments.push({
        type: 'photo',
        id: `file:${largest.file_id}`,
        url: largest.file_id, // not a direct URL, need getFile
        raw: largest,
      });
    }

    return attachments;
  }
}
