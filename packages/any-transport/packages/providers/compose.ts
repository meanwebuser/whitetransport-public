/**
 * YTP Composable Providers — compose(channel, receiver, encoder) → Provider
 *
 * Instead of N×M×K monolithic providers (VKTextTimer, VKDocWebhook, ...),
 * we have N+M+K composable components:
 *
 *   Channel:  the API you talk to  (VK, TG, OK, YDisk)
 *   Receiver: how you get messages (Timer, LongPoll, Webhook)
 *   Encoder:  how you pack data    (Text, Document, Photo)
 *
 * Usage:
 *   compose(new VKChannel({...}), new TimerReceiver(), new TextEncoder())
 *   → Provider with id 'vk-text-timer'
 *
 *   compose(new TGChannel({...}), new LongPollReceiver(), new DocumentEncoder())
 *   → Provider with id 'tg-doc-longpoll'
 *
 * Architecture:
 *   ┌─────────────┐   ┌──────────────┐   ┌─────────────┐
 *   │   Channel   │ + │   Receiver   │ + │   Encoder   │
 *   ├─────────────┤   ├──────────────┤   ├─────────────┤
 *   │ VKChannel   │   │ TimerRecv    │   │ TextEnc     │
 *   │ TGChannel   │   │ LongPollRecv │   │ DocEnc      │
 *   │ OKChannel   │   │ WebhookRecv  │   │ PhotoEnc    │
 *   │ YDiskCh     │   │              │   │             │
 *   └─────────────┘   └──────────────┘   └─────────────┘
 */

import type { Provider, ProviderCursor, ProviderMessage, OutboundFrame, AppendResult, ProviderCapabilities, RateHint } from './provider';

// ── Channel: the API you talk to ──────────────────────────────────────────

export interface ChannelAttachment {
  type: 'doc' | 'photo' | 'video' | 'file' | 'sticker';
  id: string;
  url?: string;
  ownerId?: number;
  /** Provider-specific raw attachment data */
  raw?: any;
}

export interface ChannelMessage {
  id: string;
  timestamp: number;
  text: string;
  fromSelf: boolean;
  attachments: ChannelAttachment[];
}

export interface ChannelCapabilities {
  maxTextBytes: number;
  supportsDocuments: boolean;
  supportsPhotos: boolean;
  supportsLongPoll: boolean;
  supportsWebhook: boolean;
  minSendIntervalMs: number;
  maxBurst: number;
}

export interface Channel {
  readonly id: string;

  /** Send a message with optional attachment string */
  sendMessage(text: string, attachment?: string): Promise<{ messageId: string; timestamp: number }>;

  /**
   * Poll for new messages since cursor.
   * timeout=0 → instant return (for TimerReceiver)
   * timeout>0 → wait up to N seconds (for LongPollReceiver)
   */
  poll(since: string | number | null, timeout: number): Promise<{
    messages: ChannelMessage[];
    nextCursor: string | number | null;
  }>;

  /** Upload document, returns attachment string (e.g. "doc123_456") */
  uploadDocument?(data: Buffer, filename: string): Promise<string>;

  /** Upload photo, returns attachment string (e.g. "photo123_456") */
  uploadPhoto?(data: Buffer, filename: string): Promise<string>;

  /** Download attachment data */
  downloadAttachment?(attachment: ChannelAttachment): Promise<Buffer>;

  /** Describe channel capabilities */
  caps(): ChannelCapabilities;

  /** Initialize (auth, verify token) */
  init(): Promise<void>;

  /** Shutdown */
  destroy(): Promise<void>;
}

// ── Receiver: how you check for incoming messages ──────────────────────────

export interface Receiver {
  readonly id: string;

  /** Initialize with a channel reference */
  init(channel: Channel): Promise<void>;

  /** Shutdown */
  stop(): Promise<void>;

  /** Get new messages since cursor */
  scan(cursor: ProviderCursor): Promise<{
    messages: ChannelMessage[];
    nextCursor: ProviderCursor;
  }>;

  /** Recommended poll interval in ms (used by the main loop) */
  recommendedIntervalMs(): number;
}

// ── Encoder: how you pack data into messages ──────────────────────────────

export interface Encoder {
  readonly id: string;

  /** Encode outbound frame into one or more channel sends */
  encode(frame: OutboundFrame, channel: Channel): Promise<AppendResult>;

  /** Decode a raw channel message into a ProviderMessage (extract attachment data, etc.) */
  decode(raw: ChannelMessage, channel: Channel): Promise<ProviderMessage>;

  /** Maximum payload bytes per message */
  maxPayloadBytes(): number;
}

// ── compose() — build a Provider from components ──────────────────────────

export interface ComposedProviderConfig {
  /** Custom id override (default: '{channel}-{encoder}-{receiver}') */
  id?: string;
}

export function compose(
  channel: Channel,
  receiver: Receiver,
  encoder: Encoder,
  config?: ComposedProviderConfig,
): Provider {
  const composedId = config?.id ?? `${channel.id}-${encoder.id}-${receiver.id}`;

  return {
    id: composedId,

    async start(): Promise<void> {
      await channel.init();
      await receiver.init(channel);
      console.log(`[compose:${composedId}] Started (channel=${channel.id}, receiver=${receiver.id}, encoder=${encoder.id})`);
    },

    async stop(): Promise<void> {
      await receiver.stop();
      await channel.destroy();
    },

    async append(frame: OutboundFrame): Promise<AppendResult> {
      return encoder.encode(frame, channel);
    },

    async scan(cursor: ProviderCursor): Promise<{
      messages: ProviderMessage[];
      nextCursor: ProviderCursor;
    }> {
      const raw = await receiver.scan(cursor);
      const decoded: ProviderMessage[] = [];

      for (const msg of raw.messages) {
        if (msg.fromSelf) continue; // skip own messages
        const providerMsg = await encoder.decode(msg, channel);
        decoded.push(providerMsg);
      }

      return { messages: decoded, nextCursor: raw.nextCursor };
    },

    capabilities(): ProviderCapabilities {
      const chCaps = channel.caps();
      const encPayload = encoder.maxPayloadBytes();

      return {
        maxTextBytes: Math.min(encPayload, chCaps.maxTextBytes),
        supportsAttachments: chCaps.supportsDocuments || chCaps.supportsPhotos,
        supportsEdit: false,
        supportsDelete: false,
        supportsMessageIds: true,
        supportsServerTimestamp: true,
        minSafeSendIntervalMs: chCaps.minSendIntervalMs,
        recommendedPollIntervalMs: receiver.recommendedIntervalMs(),
      };
    },

    rateHint(): RateHint {
      const chCaps = channel.caps();
      return {
        minIntervalMs: chCaps.minSendIntervalMs,
        burst: chCaps.maxBurst,
        mode: chCaps.minSendIntervalMs < 200 ? 'aggressive' : chCaps.minSendIntervalMs < 1000 ? 'moderate' : 'conservative',
      };
    },
  };
}
