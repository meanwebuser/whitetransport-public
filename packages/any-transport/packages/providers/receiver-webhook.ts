/**
 * YTP WebhookReceiver — receives messages via webhook callbacks.
 *
 * Instead of polling the channel, an external service (Vercel, Cloudflare Workers)
 * receives webhook POST requests and pushes messages into a WebhookStore.
 * The scan() method reads from the store.
 *
 * Latency: near-instant (push model).
 * Requires a public URL (Vercel / Cloudflare Workers / ngrok).
 */

import type { Channel, ChannelMessage, Receiver } from './compose';
import type { ProviderCursor } from './provider';

// ── Webhook Store ────────────────────────────────────────────────────────

export interface StoredWebhookMessage {
  id: string;
  timestamp: number;
  text: string;
  fromSelf: boolean;
  attachments?: any[];
}

export interface WebhookStore {
  push(providerId: string, msg: StoredWebhookMessage): Promise<void>;
  popSince(providerId: string, cursor: string | null): Promise<StoredWebhookMessage[]>;
}

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

// ── WebhookReceiver ──────────────────────────────────────────────────────

export interface WebhookReceiverConfig {
  store: WebhookStore;
  providerId?: string;   // key in the store, default = channel.id
  label?: string;
}

export class WebhookReceiver implements Receiver {
  readonly id: string;

  private config: WebhookReceiverConfig;
  private channel: Channel | null = null;

  constructor(config: WebhookReceiverConfig) {
    this.config = config;
    this.id = config.label ? `webhook-${config.label}` : 'webhook';
  }

  async init(channel: Channel): Promise<void> {
    this.channel = channel;
    console.log(`[WebhookReceiver:${this.id}] Using store for providerId=${this.storeKey()}`);
  }

  async stop(): Promise<void> {
    this.channel = null;
  }

  async scan(cursor: ProviderCursor): Promise<{
    messages: ChannelMessage[];
    nextCursor: ProviderCursor;
  }> {
    const msgs = await this.config.store.popSince(this.storeKey(), cursor as string | null);
    let maxId = cursor ? Number(cursor) : 0;

    const messages: ChannelMessage[] = msgs.map(m => {
      maxId = Math.max(maxId, Number(m.id));
      return {
        id: m.id,
        timestamp: m.timestamp,
        text: m.text,
        fromSelf: m.fromSelf,
        attachments: m.attachments || [],
      };
    });

    return { messages, nextCursor: String(maxId) };
  }

  recommendedIntervalMs(): number {
    // Webhook messages are pushed, so we can poll the store frequently
    return 1000;
  }

  /** Get the store key for this receiver */
  storeKey(): string {
    return this.config.providerId || this.channel?.id || 'default';
  }
}

// ── Webhook callback helpers ─────────────────────────────────────────────

/**
 * Handle VK Callback API event and push to store.
 * Call this from your /api/webhook/vk route.
 */
export async function handleVKWebhook(
  event: any,
  store: WebhookStore,
  providerId: string,
  confirmationToken: string,
  secretKey?: string,
  selfUserId?: number,
): Promise<string> {
  if (event.type === 'confirmation') {
    return confirmationToken;
  }

  if (secretKey && event.secret !== secretKey) {
    return 'ok';
  }

  if (event.type === 'message_new' && event.object?.message) {
    const msg = event.object.message;
    await store.push(providerId, {
      id: String(msg.id || msg.conversation_message_id),
      timestamp: (msg.date || Math.floor(Date.now() / 1000)) * 1000,
      text: msg.text || '',
      fromSelf: msg.from_id === selfUserId || msg.out === 1,
      attachments: msg.attachments || [],
    });
  }

  return 'ok';
}

/**
 * Handle Telegram webhook update and push to store.
 * Call this from your /api/webhook/tg route.
 */
export async function handleTGWebhook(
  update: any,
  store: WebhookStore,
  providerId: string,
  botUserId?: number,
): Promise<void> {
  const msg = update.message || update.channel_post;
  if (msg?.text) {
    await store.push(providerId, {
      id: String(msg.message_id),
      timestamp: msg.date * 1000,
      text: msg.text,
      fromSelf: msg.from?.id === botUserId,
      attachments: msg.document ? [msg.document] : [],
    });
  }
}

/**
 * Handle OK Streaming API webhook event and push to store.
 * Call this from your /api/webhook/ok route.
 */
export async function handleOKWebhook(
  event: any,
  store: WebhookStore,
  providerId: string,
): Promise<string> {
  if (event.type === 'MESSAGE_OK' && event.data) {
    const msg = event.data;
    await store.push(providerId, {
      id: String(msg.messageId),
      timestamp: (msg.date || Math.floor(Date.now() / 1000)) * 1000,
      text: msg.text || '',
      fromSelf: msg.senderId === msg.authorId,
      attachments: msg.attachment ? [msg.attachment] : [],
    });
  }
  return 'ok';
}
