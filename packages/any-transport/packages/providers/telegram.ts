/**
 * YTP TelegramProvider — Telegram Bot API as an append-only mailbox.
 *
 * Uses long-polling (getUpdates) for receiving, sendMessage for sending.
 * Respects rate limits conservatively.
 *
 * IMPORTANT: Bot token must be stored in OS credential vault, not in config.
 */

import type { Provider, ProviderCursor, ProviderMessage, OutboundFrame, AppendResult, ProviderCapabilities, RateHint } from './provider';

interface TelegramConfig {
  botToken: string;    // NEVER hardcode — read from OS vault
  chatId: string;      // the chat this provider reads/writes
}

interface TelegramUpdate {
  update_id: number;
  message?: {
    message_id: number;
    date: number;
    text?: string;
    from?: { id: number; is_bot: boolean };
  };
}

export class TelegramProvider implements Provider {
  readonly id = 'telegram';

  private config: TelegramConfig;
  private lastUpdateId = 0;
  private botUserId: number | null = null;

  constructor(config: TelegramConfig) {
    this.config = config;
  }

  private get apiUrl(): string {
    return `https://api.telegram.org/bot${this.config.botToken}`;
  }

  async start(): Promise<void> {
    // Verify bot token and get bot info
    const resp = await fetch(`${this.apiUrl}/getMe`);
    const data = await resp.json() as { ok: boolean; result?: { id: number } };
    if (!data.ok || !data.result) {
      throw new Error(`Telegram getMe failed: ${JSON.stringify(data)}`);
    }
    this.botUserId = data.result.id;
    console.log(`[TelegramProvider] Bot connected: user_id=${this.botUserId}`);
  }

  async stop(): Promise<void> {
    // No persistent connection to close with long-polling
  }

  async append(frame: OutboundFrame): Promise<AppendResult> {
    const resp = await fetch(`${this.apiUrl}/sendMessage`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        chat_id: this.config.chatId,
        text: frame.text,
        parse_mode: undefined, // raw text, no parsing
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
    const offset = cursor ? Number(cursor) + 1 : this.lastUpdateId;

    const resp = await fetch(
      `${this.apiUrl}/getUpdates?offset=${offset}&timeout=10&limit=100`,
    );
    const data = await resp.json() as { ok: boolean; result?: TelegramUpdate[] };

    if (!data.ok || !data.result) {
      return { messages: [], nextCursor: cursor };
    }

    const messages: ProviderMessage[] = [];
    let maxUpdateId = offset - 1;

    for (const update of data.result) {
      maxUpdateId = Math.max(maxUpdateId, update.update_id);

      if (update.message && update.message.text) {
        messages.push({
          id: String(update.message.message_id),
          timestamp: update.message.date * 1000,
          text: update.message.text,
          fromSelf: update.message.from?.id === this.botUserId,
        });
      }
    }

    this.lastUpdateId = maxUpdateId;

    return {
      messages,
      nextCursor: String(maxUpdateId),
    };
  }

  capabilities(): ProviderCapabilities {
    return {
      maxTextBytes: 2048, // conservative for text-safe mode
      supportsAttachments: true,
      supportsEdit: true,
      supportsDelete: true,
      supportsMessageIds: true,
      supportsServerTimestamp: true,
      minSafeSendIntervalMs: 1500,
      recommendedPollIntervalMs: 3000,
    };
  }

  rateHint(): RateHint {
    return {
      minIntervalMs: 1500,
      burst: 1,
      mode: 'conservative',
    };
  }
}
