/**
 * YTP VKBrowserBridgeProvider — Browser-based VK transport bridge.
 *
 * This provider connects to a companion web page (hosted on GitHub Pages or
 * locally) that runs VK messaging via JSONP in the browser. The bridge
 * communicates with the Node.js server via WebSocket.
 *
 * Architecture:
 *   Node.js YTP ←WebSocket→ Bridge Page ←JSONP→ VK API
 *
 * Why this matters:
 *   - Server-side VK: uses VK API token → 3 req/s rate limit
 *   - Browser VK: uses Kate Mobile OAuth → DIFFERENT token → another 3 req/s
 *   - Running both in parallel doubles throughput!
 *
 * The companion page is `vk-browser-bridge.html` — a single-file React app
 * that can be hosted on GitHub Pages or opened locally.
 *
 * Setup:
 *   1. Open vk-browser-bridge.html in a browser
 *   2. Login with Kate Mobile OAuth
 *   3. The page connects to ws://localhost:9123/bridge
 *   4. This provider relays messages between YTP and the bridge
 */

import type { Provider, ProviderCursor, ProviderMessage, OutboundFrame, AppendResult, ProviderCapabilities, RateHint } from './provider';

interface VKBrowserBridgeConfig {
  wsUrl?: string;        // WebSocket bridge URL, default ws://localhost:9123/bridge
  peerId?: string;       // VK peer ID to send to
  label?: string;
}

interface BridgeMessage {
  type: 'append' | 'scan' | 'connected' | 'messages' | 'error' | 'ack';
  data?: any;
}

export class VKBrowserBridgeProvider implements Provider {
  readonly id: string;

  private config: VKBrowserBridgeConfig;
  private ws: WebSocket | null = null;
  private messageBuffer: ProviderMessage[] = [];
  private lastMessageId = 0;
  private pendingAcks: Map<string, { resolve: (result: AppendResult) => void; reject: (err: Error) => void }> = new Map();
  private isConnected = false;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(config: VKBrowserBridgeConfig = {}) {
    this.config = config;
    this.id = config.label ? `vk-bridge-${config.label}` : 'vk-bridge';
  }

  async start(): Promise<void> {
    console.log(`[VKBrowserBridge:${this.id}] Connecting to bridge at ${this.config.wsUrl || 'ws://localhost:9123/bridge'}`);
    this.connect();

    // Wait for connection with timeout
    await new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => {
        reject(new Error('Bridge connection timeout'));
      }, 10000);

      const checkInterval = setInterval(() => {
        if (this.isConnected) {
          clearTimeout(timeout);
          clearInterval(checkInterval);
          resolve();
        }
      }, 100);
    }).catch(err => {
      console.warn(`[VKBrowserBridge:${this.id}] Bridge not available yet: ${err.message}`);
      // Don't throw — the bridge page might not be open yet
    });
  }

  async stop(): Promise<void> {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
    this.isConnected = false;
  }

  async append(frame: OutboundFrame): Promise<AppendResult> {
    if (!this.isConnected || !this.ws) {
      throw new Error('Bridge not connected — open the companion page first');
    }

    const msgId = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;

    return new Promise<AppendResult>((resolve, reject) => {
      this.pendingAcks.set(msgId, { resolve, reject });

      this.ws!.send(JSON.stringify({
        type: 'append',
        data: {
          msgId,
          text: frame.text,
          peerId: this.config.peerId,
          priority: frame.priority,
        },
      }));

      // Timeout after 10 seconds
      setTimeout(() => {
        if (this.pendingAcks.has(msgId)) {
          this.pendingAcks.delete(msgId);
          reject(new Error('Bridge append timeout'));
        }
      }, 10000);
    });
  }

  async scan(cursor: ProviderCursor): Promise<{
    messages: ProviderMessage[];
    nextCursor: ProviderCursor;
  }> {
    if (!this.isConnected || !this.ws) {
      return { messages: [], nextCursor: cursor };
    }

    // Request new messages from bridge
    this.ws.send(JSON.stringify({
      type: 'scan',
      data: { cursor },
    }));

    // Wait a short time for bridge to respond
    await this.sleep(500);

    const sinceId = cursor ? Number(cursor) : this.lastMessageId;
    const newMessages = this.messageBuffer.filter(m => Number(m.id) > sinceId);
    this.messageBuffer = [];

    return { messages: newMessages, nextCursor: String(this.lastMessageId) };
  }

  capabilities(): ProviderCapabilities {
    return {
      maxTextBytes: 4096,
      supportsAttachments: false,
      supportsEdit: true,
      supportsDelete: false,
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

  // ── WebSocket connection ────────────────────────────────────────────────

  private connect(): void {
    const url = this.config.wsUrl || 'ws://localhost:9123/bridge';

    try {
      this.ws = new WebSocket(url);

      this.ws.onopen = () => {
        console.log(`[VKBrowserBridge:${this.id}] Connected to bridge`);
        this.isConnected = true;
      };

      this.ws.onmessage = (event) => {
        try {
          const msg: BridgeMessage = JSON.parse(event.data);
          this.handleBridgeMessage(msg);
        } catch (err) {
          console.error(`[VKBrowserBridge:${this.id}] Parse error:`, err);
        }
      };

      this.ws.onclose = () => {
        console.log(`[VKBrowserBridge:${this.id}] Bridge disconnected`);
        this.isConnected = false;
        this.scheduleReconnect();
      };

      this.ws.onerror = (err) => {
        console.error(`[VKBrowserBridge:${this.id}] WebSocket error`);
        this.isConnected = false;
      };
    } catch (err) {
      console.error(`[VKBrowserBridge:${this.id}] Connect error:`, err);
      this.scheduleReconnect();
    }
  }

  private handleBridgeMessage(msg: BridgeMessage): void {
    switch (msg.type) {
      case 'connected':
        this.isConnected = true;
        console.log(`[VKBrowserBridge:${this.id}] Bridge page confirmed connection`);
        break;

      case 'messages':
        // Received messages from the browser VK client
        if (msg.data && Array.isArray(msg.data.messages)) {
          for (const m of msg.data.messages) {
            this.messageBuffer.push({
              id: m.id,
              timestamp: m.timestamp,
              text: m.text,
              fromSelf: m.fromSelf,
            });
            const idNum = Number(m.id);
            if (idNum > this.lastMessageId) this.lastMessageId = idNum;
          }
        }
        break;

      case 'ack':
        // Message sent acknowledgment from bridge
        if (msg.data && msg.data.msgId) {
          const pending = this.pendingAcks.get(msg.data.msgId);
          if (pending) {
            this.pendingAcks.delete(msg.data.msgId);
            pending.resolve({
              messageId: msg.data.messageId || msg.data.msgId,
              timestamp: msg.data.timestamp || Date.now(),
            });
          }
        }
        break;

      case 'error':
        console.error(`[VKBrowserBridge:${this.id}] Bridge error:`, msg.data);
        break;
    }
  }

  private scheduleReconnect(): void {
    if (this.reconnectTimer) return;

    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      console.log(`[VKBrowserBridge:${this.id}] Reconnecting...`);
      this.connect();
    }, 5000);
  }

  private sleep(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms));
  }
}
