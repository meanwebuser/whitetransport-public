/**
 * YTP Webhook Providers — Serverless-compatible transport using webhooks.
 *
 * Instead of long polling (which requires a persistent server), these providers
 * receive incoming messages via webhooks and send outgoing via REST API.
 * Perfect for Vercel / Cloudflare Workers / AWS Lambda deployment.
 *
 * Supported webhook sources:
 *   - VK Callback API (groups)
 *   - Telegram Bot setWebhook
 *   - OK Streaming API
 *
 * Architecture:
 *   ┌──────────────┐     POST /api/webhook/vk      ┌──────────┐
 *   │  VK Callback  │ ─────────────────────────────▶│  Vercel  │
 *   └──────────────┘                                │  Server  │
 *   ┌──────────────┐     POST /api/webhook/tg      │          │
 *   │  TG Webhook   │ ─────────────────────────────▶│  (Redis/ │
 *   └──────────────┘                                │  KV for  │
 *   ┌──────────────┐     POST /api/webhook/ok      │  state)  │
 *   │  OK Streaming │ ─────────────────────────────▶│          │
 *   └──────────────┘                                └──────────┘
 *
 * The WebhookStore interface abstracts where incoming messages are stored.
 * Implementations: MemoryWebhookStore, RedisWebhookStore, VercelKVStore.
 */

import type { Provider, ProviderCursor, ProviderMessage, OutboundFrame, AppendResult, ProviderCapabilities, RateHint } from './provider';

// ── Webhook Message Store ────────────────────────────────────────────────

export interface StoredWebhookMessage {
  id: string;
  timestamp: number;
  text: string;
  fromSelf: boolean;
  providerId: string;
  attachments?: any[];
}

export interface WebhookStore {
  push(providerId: string, msg: StoredWebhookMessage): Promise<void>;
  popSince(providerId: string, cursor: string | null): Promise<StoredWebhookMessage[]>;
}

// ── In-Memory Webhook Store (for development / single-instance) ──────────

export class MemoryWebhookStore implements WebhookStore {
  private store: Map<string, StoredWebhookMessage[]> = new Map();

  async push(providerId: string, msg: StoredWebhookMessage): Promise<void> {
    if (!this.store.has(providerId)) this.store.set(providerId, []);
    this.store.get(providerId)!.push(msg);
  }

  async popSince(providerId: string, cursor: string | null): Promise<StoredWebhookMessage[]> {
    const msgs = this.store.get(providerId) || [];
    if (!cursor) {
      this.store.set(providerId, []);
      return msgs;
    }
    const idx = msgs.findIndex(m => Number(m.id) > Number(cursor));
    if (idx === -1) return [];
    const result = msgs.slice(idx);
    this.store.set(providerId, msgs.slice(0, idx));
    return result;
  }
}

// ── VK Webhook Provider ─────────────────────────────────────────────────

/**
 * VKWebhookProvider — uses VK Callback API for receiving messages,
 * REST API for sending. No long polling needed.
 *
 * Setup:
 *   1. Create a VK Community (group)
 *   2. Enable "Messages" in the group settings
 *   3. Set Callback URL to: https://your-vercel.app/api/webhook/vk
 *   4. Configure: group_id, confirmation_token, secret_key
 *   5. VK will send confirmation request — your API route must respond
 */
export class VKWebhookProvider implements Provider {
  readonly id: string;

  private config: VKWebhookConfig;
  private store: WebhookStore;
  private apiVersion: string;
  private userId: number | null = null;

  constructor(config: VKWebhookConfig, store: WebhookStore) {
    this.config = config;
    this.store = store;
    this.apiVersion = config.apiVersion || '5.131';
    this.id = config.label ? `vk-wh-${config.label}` : 'vk-wh';
  }

  async start(): Promise<void> {
    const userInfo = await this.callApi('users.get', {});
    if (userInfo.response && userInfo.response[0]) {
      this.userId = userInfo.response[0].id;
      console.log(`[VKWebhookProvider:${this.id}] Authenticated as user_id=${this.userId}`);
    }
    console.log(`[VKWebhookProvider:${this.id}] Webhook mode — configure Callback URL in VK group settings`);
  }

  async stop(): Promise<void> {}

  /**
   * Handle incoming VK Callback event.
   * Called from your API route (e.g., /api/webhook/vk).
   */
  async handleCallback(event: any): Promise<string> {
    // Confirmation
    if (event.type === 'confirmation') {
      return this.config.confirmationToken;
    }

    // Verify secret
    if (this.config.secretKey && event.secret !== this.config.secretKey) {
      console.warn(`[VKWebhookProvider:${this.id}] Invalid secret key`);
      return 'ok';
    }

    // Message new
    if (event.type === 'message_new' && event.object?.message) {
      const msg = event.object.message;
      await this.store.push(this.id, {
        id: String(msg.id || msg.conversation_message_id),
        timestamp: (msg.date || Math.floor(Date.now() / 1000)) * 1000,
        text: msg.text || '',
        fromSelf: msg.from_id === this.userId || msg.out === 1,
        providerId: this.id,
        attachments: msg.attachments || [],
      });
    }

    // Message edit
    if (event.type === 'message_event' && event.object) {
      const msg = event.object;
      await this.store.push(this.id, {
        id: String(msg.event_id || Date.now()),
        timestamp: Date.now(),
        text: msg.payload ? JSON.stringify(msg.payload) : '',
        fromSelf: false,
        providerId: this.id,
      });
    }

    return 'ok';
  }

  async append(frame: OutboundFrame): Promise<AppendResult> {
    const chunks = this.splitMessage(frame.text, 4090);
    let lastResult: AppendResult | null = null;

    for (let i = 0; i < chunks.length; i++) {
      const label = chunks.length > 1 ? `[YTP:${i}/${chunks.length}] ` : '[YTP] ';
      const sendResult = await this.callApi('messages.send', {
        peer_id: this.config.peerId,
        message: label + chunks[i],
        random_id: Math.floor(Math.random() * 2147483647),
      });

      if (sendResult.error) {
        throw new Error(`VK webhook send failed: ${sendResult.error.error_msg}`);
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
    const msgs = await this.store.popSince(this.id, cursor === null ? null : String(cursor));
    let maxId = cursor ? Number(cursor) : 0;

    const messages: ProviderMessage[] = msgs.map(m => {
      maxId = Math.max(maxId, Number(m.id));
      return {
        id: m.id,
        timestamp: m.timestamp,
        text: m.text,
        fromSelf: m.fromSelf,
      };
    });

    return { messages, nextCursor: String(maxId) };
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
      recommendedPollIntervalMs: 1000,  // Can poll faster — no long poll needed
    };
  }

  rateHint(): RateHint {
    return {
      minIntervalMs: 350,
      burst: 3,
      mode: 'moderate',
    };
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

  private splitMessage(text: string, maxLen: number): string[] {
    if (text.length <= maxLen) return [text];
    const chunks: string[] = [];
    let offset = 0;
    while (offset < text.length) {
      chunks.push(text.slice(offset, offset + maxLen));
      offset += maxLen;
    }
    return chunks;
  }

  private sleep(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms));
  }
}

// ── Telegram Webhook Provider ────────────────────────────────────────────

/**
 * TGWebhookProvider — uses Telegram setWebhook for receiving messages,
 * Bot API for sending. No long polling needed.
 *
 * Setup:
 *   1. Create a Telegram Bot via @BotFather
 *   2. Set webhook: POST https://api.telegram.org/bot{TOKEN}/setWebhook
 *      with url=https://your-vercel.app/api/webhook/tg
 *   3. Optionally set secret_token for verification
 */
export class TGWebhookProvider implements Provider {
  readonly id: string;

  private config: TGWebhookConfig;
  private store: WebhookStore;
  private botUserId: number | null = null;

  constructor(config: TGWebhookConfig, store: WebhookStore) {
    this.config = config;
    this.store = store;
    this.id = config.label ? `tg-wh-${config.label}` : 'tg-wh';
  }

  private get apiUrl(): string {
    return `https://api.telegram.org/bot${this.config.botToken}`;
  }

  async start(): Promise<void> {
    const resp = await fetch(`${this.apiUrl}/getMe`);
    const data = await resp.json() as { ok: boolean; result?: { id: number } };
    if (!data.ok || !data.result) {
      throw new Error(`Telegram getMe failed: ${JSON.stringify(data)}`);
    }
    this.botUserId = data.result.id;

    // Set webhook
    if (this.config.webhookUrl) {
      const whResp = await fetch(`${this.apiUrl}/setWebhook`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          url: this.config.webhookUrl,
          secret_token: this.config.secretToken,
          allowed_updates: ['message'],
        }),
      });
      const whData = await whResp.json() as { ok: boolean; description?: string };
      if (whData.ok) {
        console.log(`[TGWebhookProvider:${this.id}] Webhook set to ${this.config.webhookUrl}`);
      } else {
        console.warn(`[TGWebhookProvider:${this.id}] Webhook setup failed: ${whData.description}`);
      }
    }
  }

  async stop(): Promise<void> {
    // Optionally delete webhook on shutdown
  }

  /**
   * Handle incoming Telegram webhook update.
   * Called from your API route (e.g., /api/webhook/tg).
   */
  async handleCallback(update: any): Promise<void> {
    // Verify secret token
    if (this.config.secretToken) {
      // The secret_token is verified by the HTTP header x-telegram-bot-api-secret-token
      // This should be checked in the API route handler
    }

    if (update.message && update.message.text) {
      await this.store.push(this.id, {
        id: String(update.message.message_id),
        timestamp: update.message.date * 1000,
        text: update.message.text,
        fromSelf: update.message.from?.id === this.botUserId,
        providerId: this.id,
        attachments: update.message.document ? [update.message.document] : [],
      });
    }

    // Handle document/file attachments
    if (update.message?.document) {
      const doc = update.message.document;
      if (doc.file_id) {
        await this.store.push(this.id, {
          id: `${update.message.message_id}:doc`,
          timestamp: update.message.date * 1000,
          text: `[DOC:${doc.file_name || 'file'}:${doc.file_size || 0}]`,
          fromSelf: update.message.from?.id === this.botUserId,
          providerId: this.id,
          attachments: [doc],
        });
      }
    }
  }

  async append(frame: OutboundFrame): Promise<AppendResult> {
    const resp = await fetch(`${this.apiUrl}/sendMessage`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        chat_id: this.config.chatId,
        text: frame.text,
        parse_mode: undefined,
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

  async scan(cursor: ProviderCursor): Promise<{
    messages: ProviderMessage[];
    nextCursor: ProviderCursor;
  }> {
    const msgs = await this.store.popSince(this.id, cursor === null ? null : String(cursor));
    let maxId = cursor ? Number(cursor) : 0;

    const messages: ProviderMessage[] = msgs.map(m => {
      maxId = Math.max(maxId, Number(m.id));
      return {
        id: m.id,
        timestamp: m.timestamp,
        text: m.text,
        fromSelf: m.fromSelf,
      };
    });

    return { messages, nextCursor: String(maxId) };
  }

  capabilities(): ProviderCapabilities {
    return {
      maxTextBytes: 4096,
      supportsAttachments: true,
      supportsEdit: true,
      supportsDelete: true,
      supportsMessageIds: true,
      supportsServerTimestamp: true,
      minSafeSendIntervalMs: 30,  // 30 msg/s
      recommendedPollIntervalMs: 500,
    };
  }

  rateHint(): RateHint {
    return {
      minIntervalMs: 30,
      burst: 30,
      mode: 'aggressive',
    };
  }
}

// ── OK Webhook Provider ──────────────────────────────────────────────────

/**
 * OKWebhookProvider — uses OK Streaming API for receiving messages,
 * REST API for sending. No long polling needed.
 *
 * Setup:
 *   1. Register an OK application
 *   2. Subscribe to events via OK Streaming API
 *   3. Set callback URL to: https://your-vercel.app/api/webhook/ok
 *   4. OK sends POST with event data
 *
 * OK Streaming uses long-lived SSE or webhook callbacks depending on
 * the app configuration. This provider handles the webhook variant.
 */
export class OKWebhookProvider implements Provider {
  readonly id: string;

  private config: OKWebhookConfig;
  private store: WebhookStore;

  constructor(config: OKWebhookConfig, store: WebhookStore) {
    this.config = config;
    this.store = store;
    this.id = config.label ? `ok-wh-${config.label}` : 'ok-wh';
  }

  async start(): Promise<void> {
    console.log(`[OKWebhookProvider:${this.id}] Webhook mode — configure callback URL in OK app settings`);
  }

  async stop(): Promise<void> {}

  /**
   * Handle incoming OK webhook event.
   * Called from your API route (e.g., /api/webhook/ok).
   */
  async handleCallback(event: any): Promise<string> {
    if (event.type === 'MESSAGE_OK' && event.data) {
      const msg = event.data;
      await this.store.push(this.id, {
        id: String(msg.messageId),
        timestamp: (msg.date || Math.floor(Date.now() / 1000)) * 1000,
        text: msg.text || '',
        fromSelf: msg.senderId === msg.authorId,
        providerId: this.id,
        attachments: msg.attachment ? [msg.attachment] : [],
      });
    }

    return 'ok';
  }

  async append(frame: OutboundFrame): Promise<AppendResult> {
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
        throw new Error(`OK send failed: ${resp.error_msg}`);
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

  async scan(cursor: ProviderCursor): Promise<{
    messages: ProviderMessage[];
    nextCursor: ProviderCursor;
  }> {
    const msgs = await this.store.popSince(this.id, cursor === null ? null : String(cursor));
    let maxId = cursor ? Number(cursor) : 0;

    const messages: ProviderMessage[] = msgs.map(m => {
      maxId = Math.max(maxId, Number(m.id));
      return {
        id: m.id,
        timestamp: m.timestamp,
        text: m.text,
        fromSelf: m.fromSelf,
      };
    });

    return { messages, nextCursor: String(maxId) };
  }

  capabilities(): ProviderCapabilities {
    return {
      maxTextBytes: 4000,
      supportsAttachments: true,
      supportsEdit: false,
      supportsDelete: true,
      supportsMessageIds: true,
      supportsServerTimestamp: true,
      minSafeSendIntervalMs: 250,
      recommendedPollIntervalMs: 1000,
    };
  }

  rateHint(): RateHint {
    return {
      minIntervalMs: 250,
      burst: 5,
      mode: 'moderate',
    };
  }

  // ── OK API ─────────────────────────────────────────────────────────────

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

  private splitMessage(text: string, maxLen: number): string[] {
    if (text.length <= maxLen) return [text];
    const chunks: string[] = [];
    let offset = 0;
    while (offset < text.length) {
      chunks.push(text.slice(offset, offset + maxLen));
      offset += maxLen;
    }
    return chunks;
  }

  private sleep(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms));
  }
}

// ── Config Types ─────────────────────────────────────────────────────────

export interface VKWebhookConfig {
  accessToken: string;
  peerId: string;
  groupId: string;             // VK community ID for Callback API
  confirmationToken: string;   // String to respond on confirmation request
  secretKey?: string;          // Secret key for Callback API verification
  apiVersion?: string;
  label?: string;
}

export interface TGWebhookConfig {
  botToken: string;
  chatId: string;
  webhookUrl?: string;         // Your public Vercel URL + /api/webhook/tg
  secretToken?: string;        // x-telegram-bot-api-secret-token header
  label?: string;
}

export interface OKWebhookConfig {
  accessToken: string;
  applicationKey: string;
  sessionSecretKey: string;
  chatId: string;
  recipientId?: string;
  label?: string;
}

// Re-export createHash for OK providers
import { createHash } from 'crypto';
