/**
 * YTP OKProvider — Odnoklassniki API as an append-only mailbox.
 *
 * Uses OK Long Poll for receiving, messages.send for sending.
 * MD5 request signing as required by OK API.
 *
 * Rate limits: ~5 requests/second, messages up to ~4000 chars.
 */

import { createHash } from 'crypto';
import type { Provider, ProviderCursor, ProviderMessage, OutboundFrame, AppendResult, ProviderCapabilities, RateHint } from './provider';

interface OKConfig {
  accessToken: string;
  applicationKey: string;   // OK app key
  sessionSecretKey: string; // OK session secret for signing
  chatId: string;           // OK chat ID (e.g., 'chat:${WT_OK_CHAT_ID}')
  recipientId?: string;     // User ID to send messages to
  label?: string;
}

export class OKProvider implements Provider {
  readonly id: string;

  private config: OKConfig;
  private lastMessageId = 0;
  private messageBuffer: ProviderMessage[] = [];
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private isRunning = false;

  constructor(config: OKConfig) {
    this.config = config;
    this.id = config.label ? `ok-${config.label}` : 'ok';
  }

  private get apiUrl(): string {
    return 'https://api.ok.ru/fb.do';
  }

  async start(): Promise<void> {
    // Verify token
    const userInfo = await this.callApi('users.getCurrentUser', {});
    if (userInfo.uid) {
      console.log(`[OKProvider:${this.id}] Authenticated as uid=${userInfo.uid}`);
    } else {
      throw new Error(`OK auth failed: ${JSON.stringify(userInfo)}`);
    }

    // Load recent messages
    const messages = await this.callApi('messages.getHistory', {
      chat: this.config.chatId,
      count: 1,
    });

    if (messages.messages && messages.messages.length > 0) {
      this.lastMessageId = messages.messages[0].messageId;
      console.log(`[OKProvider:${this.id}] Last message ID: ${this.lastMessageId}`);
    }

    this.isRunning = true;
  }

  async stop(): Promise<void> {
    this.isRunning = false;
    if (this.pollTimer) {
      clearInterval(this.pollTimer);
      this.pollTimer = null;
    }
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

  async scan(cursor: ProviderCursor): Promise<{
    messages: ProviderMessage[];
    nextCursor: ProviderCursor;
  }> {
    const sinceId = cursor ? Number(cursor) : this.lastMessageId;

    try {
      // Try Long Poll first
      const lpResult = await this.callApi('messages.getLongPollHistory', {
        chat: this.config.chatId,
        last_msg_id: sinceId > 0 ? sinceId : undefined,
      });

      if (lpResult.messages) {
        for (const msg of lpResult.messages) {
          this.messageBuffer.push({
            id: String(msg.messageId),
            timestamp: msg.date * 1000,
            text: msg.text || '',
            fromSelf: msg.senderId === msg.authorId, // simplified
          });
          this.lastMessageId = Math.max(this.lastMessageId, msg.messageId);
        }
      }

      // Fallback: getHistory
      const history = await this.callApi('messages.getHistory', {
        chat: this.config.chatId,
        count: 20,
        from_msg_id: sinceId > 0 ? sinceId : undefined,
      });

      if (history.messages) {
        for (const msg of history.messages) {
          if (msg.messageId > sinceId && !this.messageBuffer.find(m => m.id === String(msg.messageId))) {
            this.messageBuffer.push({
              id: String(msg.messageId),
              timestamp: msg.date * 1000,
              text: msg.text || '',
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
      console.error(`[OKProvider:${this.id}] Scan error:`, err);
      return { messages: [], nextCursor: cursor };
    }
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
      recommendedPollIntervalMs: 2000,
    };
  }

  rateHint(): RateHint {
    return {
      minIntervalMs: 250,
      burst: 5,
      mode: 'moderate',
    };
  }

  // ── OK API call with MD5 signing ────────────────────────────────────────

  private async callApi(method: string, params: Record<string, any>): Promise<any> {
    const allParams: Record<string, string> = {
      application_key: this.config.applicationKey,
      method,
      ...(this.config.accessToken ? { access_token: this.config.accessToken } : {}),
      format: 'json',
    };

    // Add custom params
    for (const [key, value] of Object.entries(params)) {
      if (value !== undefined && value !== null) {
        allParams[key] = String(value);
      }
    }

    // Sort params alphabetically and build sig string
    const sortedKeys = Object.keys(allParams).sort();
    const sigString = sortedKeys.map(k => `${k}=${allParams[k]}`).join('');
    const sig = createHash('md5')
      .update(sigString + this.config.sessionSecretKey)
      .digest('hex')
      .toLowerCase();

    allParams.sig = sig;

    // Build URL
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
