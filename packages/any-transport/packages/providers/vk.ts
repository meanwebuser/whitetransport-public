/**
 * YTP VKProvider — VK API as an append-only mailbox.
 *
 * Uses VK Long Poll API for receiving, messages.send for sending.
 * Supports multiple tokens for parallel channels.
 *
 * Rate limits: ~3 requests/second per token, ~20 messages/second to same user.
 * Message size: up to 4096 chars.
 */

import type { Provider, ProviderCursor, ProviderMessage, OutboundFrame, AppendResult, ProviderCapabilities, RateHint } from './provider';

interface VKConfig {
  accessToken: string;
  peerId: string;          // user_id or chat_id to read/write
  apiVersion?: string;     // default 5.131
  label?: string;          // optional label for multi-token setups
}

interface VKLongPollServer {
  server: string;
  key: string;
  ts: string;
}

interface VKLongPollUpdate {
  type: number;
  object?: any;
}

export class VKProvider implements Provider {
  readonly id: string;

  private config: VKConfig;
  private apiVersion: string;
  private longPollServer: VKLongPollServer | null = null;
  private isPolling = false;
  private lastMessageId = 0;
  private messageBuffer: ProviderMessage[] = [];
  private userId: number | null = null;

  constructor(config: VKConfig) {
    this.config = config;
    this.apiVersion = config.apiVersion || '5.131';
    this.id = config.label ? `vk-${config.label}` : 'vk';
  }

  private get apiUrl(): string {
    return 'https://api.vk.com/method';
  }

  async start(): Promise<void> {
    // Verify token and get user info
    const userInfo = await this.callApi('users.get', {});
    if (userInfo.response && userInfo.response[0]) {
      this.userId = userInfo.response[0].id;
      console.log(`[VKProvider:${this.id}] Authenticated as user_id=${this.userId}`);
    } else {
      throw new Error(`VK auth failed: ${JSON.stringify(userInfo)}`);
    }

    // Get Long Poll server
    await this.updateLongPollServer();

    // Load recent messages to set cursor
    const history = await this.callApi('messages.getHistory', {
      peer_id: this.config.peerId,
      count: 1,
    });
    if (history.response && history.response.items && history.response.items.length > 0) {
      this.lastMessageId = history.response.items[0].id;
      console.log(`[VKProvider:${this.id}] Last message ID: ${this.lastMessageId}`);
    }
  }

  async stop(): Promise<void> {
    this.isPolling = false;
  }

  async append(frame: OutboundFrame): Promise<AppendResult> {
    // Split if needed — VK max message is 4096 chars
    const chunks = this.splitMessage(frame.text, 4090); // leave room for header

    let lastResult: AppendResult | null = null;

    for (const chunk of chunks) {
      const resp = await this.callApi('messages.send', {
        peer_id: this.config.peerId,
        message: chunk,
        random_id: Math.floor(Math.random() * 2147483647),
      });

      if (resp.error) {
        throw new Error(`VK sendMessage failed: ${resp.error.error_msg}`);
      }

      lastResult = {
        messageId: String(resp.response),
        timestamp: Date.now(),
      };

      // Rate limit: wait between messages
      if (chunks.length > 1) {
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
      // Try Long Poll first for real-time
      if (this.longPollServer) {
        const updates = await this.longPoll(2); // 2-second timeout
        for (const update of updates) {
          if (update.type === 4 && update.object) {
            // Type 4 = new message
            const msg = update.object;
            if (msg.peer_id === Number(this.config.peerId) || msg.from_id === Number(this.config.peerId)) {
              this.messageBuffer.push({
                id: String(msg.id),
                timestamp: msg.date * 1000,
                text: msg.text || '',
                fromSelf: msg.from_id === this.userId,
              });
              this.lastMessageId = Math.max(this.lastMessageId, msg.id);
            }
          }
        }
      }

      // Also poll recent messages as fallback
      const history = await this.callApi('messages.getHistory', {
        peer_id: this.config.peerId,
        count: 20,
        start_message_id: sinceId > 0 ? sinceId : undefined,
      });

      if (history.response && history.response.items) {
        for (const msg of history.response.items) {
          if (msg.id > sinceId && !this.messageBuffer.find(m => m.id === String(msg.id))) {
            this.messageBuffer.push({
              id: String(msg.id),
              timestamp: msg.date * 1000,
              text: msg.text || '',
              fromSelf: msg.from_id === this.userId,
            });
            this.lastMessageId = Math.max(this.lastMessageId, msg.id);
          }
        }
      }

      // Filter and return
      const newMessages = this.messageBuffer.filter(m => Number(m.id) > sinceId);
      this.messageBuffer = []; // Clear buffer

      // Deduplicate
      const seen = new Set<string>();
      const deduped = newMessages.filter(m => {
        if (seen.has(m.id)) return false;
        seen.add(m.id);
        return true;
      });

      return {
        messages: deduped,
        nextCursor: String(this.lastMessageId),
      };
    } catch (err) {
      console.error(`[VKProvider:${this.id}] Scan error:`, err);
      return { messages: [], nextCursor: cursor };
    }
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
      recommendedPollIntervalMs: 2000,
    };
  }

  rateHint(): RateHint {
    return {
      minIntervalMs: 350,
      burst: 3,
      mode: 'moderate',
    };
  }

  // ── VK API call ────────────────────────────────────────────────────────

  private async callApi(method: string, params: Record<string, any>): Promise<any> {
    const url = new URL(`${this.apiUrl}/${method}`);
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

  // ── Long Poll ──────────────────────────────────────────────────────────

  private async updateLongPollServer(): Promise<void> {
    const resp = await this.callApi('messages.getLongPollServer', {
      need_pts: 1,
      lp_version: 3,
    });

    if (resp.response) {
      this.longPollServer = resp.response;
      console.log(`[VKProvider:${this.id}] Long Poll server acquired, ts=${resp.response.ts}`);
    }
  }

  private async longPoll(timeout: number = 2): Promise<VKLongPollUpdate[]> {
    if (!this.longPollServer) return [];

    try {
      const url = `https://${this.longPollServer.server}?act=a_check&key=${encodeURIComponent(this.longPollServer.key)}&ts=${this.longPollServer.ts}&wait=${timeout}&mode=2&version=3`;

      const resp = await fetch(url, {
        signal: AbortSignal.timeout((timeout + 5) * 1000),
      });

      const data = await resp.json() as any;

      if (data.failed) {
        // Long Poll failure — refresh server
        console.warn(`[VKProvider:${this.id}] Long Poll failed=${data.failed}, refreshing...`);
        await this.updateLongPollServer();
        return [];
      }

      if (data.ts) {
        this.longPollServer.ts = data.ts;
      }

      return (data.updates || []).map((u: any[]) => ({
        type: u[0],
        object: {
          id: u[1],
          flags: u[2],
          from_id: u[6] ? Number(u[6].split(':')[1]) || u[3] : u[3],
          date: u[4],
          text: u[5] || '',
          peer_id: u[3],
        },
      }));
    } catch (err: any) {
      if (err.name !== 'AbortError') {
        console.error(`[VKProvider:${this.id}] Long Poll error:`, err.message);
      }
      return [];
    }
  }

  // ── Helpers ────────────────────────────────────────────────────────────

  private splitMessage(text: string, maxLen: number): string[] {
    if (text.length <= maxLen) return [text];

    const chunks: string[] = [];
    let offset = 0;
    let partIdx = 0;

    while (offset < text.length) {
      const remaining = text.length - offset;
      const chunkSize = Math.min(maxLen, remaining);
      const header = `[YTP:${partIdx}] `;
      chunks.push(header + text.slice(offset, offset + chunkSize - header.length));
      offset += chunkSize - header.length;
      partIdx++;
    }

    return chunks;
  }

  private sleep(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms));
  }
}

/**
 * VKMultiTokenProvider — combines multiple VK tokens into a single provider
 * with parallel sending capability. Each token gets its own rate budget.
 */
export class VKMultiTokenProvider implements Provider {
  readonly id = 'vk-multi';

  private providers: VKProvider[];
  private roundRobin = 0;

  constructor(configs: VKConfig[]) {
    this.providers = configs.map((c, i) => new VKProvider({ ...c, label: c.label || `t${i}` }));
  }

  async start(): Promise<void> {
    for (const p of this.providers) {
      await p.start();
    }
    console.log(`[VKMultiTokenProvider] Started ${this.providers.length} VK channels`);
  }

  async stop(): Promise<void> {
    for (const p of this.providers) {
      await p.stop();
    }
  }

  async append(frame: OutboundFrame): Promise<AppendResult> {
    // Round-robin across tokens for higher throughput
    const provider = this.providers[this.roundRobin % this.providers.length];
    this.roundRobin++;
    return provider.append(frame);
  }

  async scan(cursor: ProviderCursor): Promise<{
    messages: ProviderMessage[];
    nextCursor: ProviderCursor;
  }> {
    // Merge messages from all tokens, deduplicate
    const allMessages: ProviderMessage[] = [];
    let maxId = cursor ? Number(cursor) : 0;

    for (const p of this.providers) {
      const result = await p.scan(cursor);
      allMessages.push(...result.messages);
      const resultMax = Number(result.nextCursor);
      if (resultMax > maxId) maxId = resultMax;
    }

    // Deduplicate by message ID
    const seen = new Set<string>();
    const deduped = allMessages.filter(m => {
      if (seen.has(m.id)) return false;
      seen.add(m.id);
      return true;
    });

    // Sort by timestamp
    deduped.sort((a, b) => a.timestamp - b.timestamp);

    return { messages: deduped, nextCursor: String(maxId) };
  }

  capabilities(): ProviderCapabilities {
    return {
      maxTextBytes: 4096,
      supportsAttachments: true,
      supportsEdit: true,
      supportsDelete: true,
      supportsMessageIds: true,
      supportsServerTimestamp: true,
      minSafeSendIntervalMs: 150,  // With 2 tokens: 350/2 ~ 175
      recommendedPollIntervalMs: 1500,
    };
  }

  rateHint(): RateHint {
    return {
      minIntervalMs: 150,
      burst: 6,  // 3 per token
      mode: 'moderate',
    };
  }
}
