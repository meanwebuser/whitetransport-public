/**
 * Y Transport — Single entry point for running a P2P proxy node.
 *
 * This is the ONE file you need. Run it, give it tokens, get a proxy.
 *
 * Architecture (composable):
 *   compose(channel, receiver, encoder) → Provider
 *
 *   ┌─────────────┐   ┌──────────────┐   ┌─────────────┐
 *   │   Channel   │ + │   Receiver   │ + │   Encoder   │
 *   ├─────────────┤   ├──────────────┤   ├─────────────┤
 *   │ VKChannel   │   │ TimerRecv    │   │ TextEnc     │
 *   │ TGChannel   │   │ LongPollRecv │   │ DocEnc      │
 *   │ OKChannel   │   │ WebhookRecv  │   │ PhotoEnc    │
 *   └─────────────┘   └──────────────┘   └─────────────┘
 *
 * BOTH NODES RUN THE SAME CODE. No "server" vs "client".
 *   - "client mode"  = runs SOCKS5 + HTTP proxy locally
 *   - "exit mode"    = receives stream requests, opens TCP, relays
 *   - "webhook mode" = uses webhooks instead of long poll (Vercel)
 *   - "full mode"    = does all of the above (default)
 *
 * Quick Start:
 *
 *   # On your Mac — just want a proxy?
 *   VK_TOKEN_1=vk1.a.xxx VK_PEER_ID=123 npx ts-node packages/core/run.ts
 *   # → SOCKS5 on :1080, HTTP on :8080
 *
 *   # Timer mode (no long poll needed):
 *   RECEIVE=timer VK_TOKEN_1=vk1.a.xxx VK_PEER_ID=123 npx ts-node packages/core/run.ts
 *
 *   # Telegram channel (bot admin reads public posts):
 *   TG_TOKEN_1=123:ABC TG_CHAT_ID=-1001234567890 npx ts-node packages/core/run.ts
 *
 *   # High-bandwidth document mode:
 *   ENCODING=document VK_TOKEN_1=vk1.a.xxx VK_PEER_ID=123 npx ts-node packages/core/run.ts
 */

import { YTransportNode, type YTransportNodeConfig } from './node';
import { generateIdentity } from '../crypto/identity';
import { MemoryFrameStore } from '../storage/store';

// ── Composable provider system ───────────────────────────────────────────
import type { Provider } from '../providers/provider';
import { compose } from '../providers/compose';
import { VKChannel, type VKChannelConfig } from '../providers/channel-vk';
import { TGChannel, type TGChannelConfig } from '../providers/channel-tg';
import { OKChannel, type OKChannelConfig } from '../providers/channel-ok';
import { TimerReceiver } from '../providers/receiver-timer';
import { LongPollReceiver } from '../providers/receiver-longpoll';
import { WebhookReceiver, MemoryWebhookStore, handleVKWebhook, handleTGWebhook, handleOKWebhook } from '../providers/receiver-webhook';
import { TextEncoder } from '../providers/encoder-text';
import { DocumentEncoder } from '../providers/encoder-doc';
import { PhotoEncoder } from '../providers/encoder-photo';

// ── Node mode ────────────────────────────────────────────────────────────

type NodeMode = 'full' | 'client' | 'exit' | 'webhook';
type ReceiveMode = 'auto' | 'timer' | 'longpoll' | 'webhook';
type EncodingMode = 'auto' | 'text' | 'document' | 'photo' | 'all';

interface RunConfig {
  mode: NodeMode;
  socks5Port?: number;
  httpPort?: number;

  // Receive strategy
  receiveMode: ReceiveMode;
  timerIntervalMs?: number;

  // Encoding
  encoding: EncodingMode;
  imageWidth?: number;
  imageHeight?: number;

  // VK
  vkToken1?: string;
  vkToken2?: string;
  vkPeerId?: string;
  // VK Webhook
  vkGroupId?: string;
  vkConfirmationToken?: string;
  vkCallbackSecret?: string;

  // Telegram
  tgToken1?: string;
  tgToken2?: string;
  tgChatId?: string;

  // OK
  okToken?: string;
  okAppKey?: string;
  okAppSecret?: string;
  okChatId?: string;
  okRecipientId?: string;

  // Yandex Disk (still uses legacy provider)
  ydiskRefreshToken?: string;
  ydiskClientId?: string;
  ydiskClientSecret?: string;
}

// ── Build providers using compose() ──────────────────────────────────────

function buildProviders(config: RunConfig): Provider[] {
  const providers: Provider[] = [];
  const webhookStore = new MemoryWebhookStore();

  // Determine receive strategy
  const getReceiver = (label: string) => {
    switch (config.receiveMode) {
      case 'webhook':
        return new WebhookReceiver({ store: webhookStore, label });
      case 'timer':
        return new TimerReceiver({ intervalMs: config.timerIntervalMs });
      case 'longpoll':
        return new LongPollReceiver();
      case 'auto':
      default:
        // Auto: use webhook in webhook mode, longpoll otherwise
        if (config.mode === 'webhook') {
          return new WebhookReceiver({ store: webhookStore, label });
        }
        return new LongPollReceiver();
    }
  };

  // Determine encoders
  const getEncoders = () => {
    switch (config.encoding) {
      case 'text': return [new TextEncoder()];
      case 'document': return [new DocumentEncoder({ imageWidth: config.imageWidth, imageHeight: config.imageHeight })];
      case 'photo': return [new PhotoEncoder()];
      case 'all': return [
        new TextEncoder(),
        new DocumentEncoder({ imageWidth: config.imageWidth, imageHeight: config.imageHeight }),
        new PhotoEncoder(),
      ];
      case 'auto':
      default:
        // Auto: text + document for VK/OK, text-only for TG
        return [new TextEncoder(), new DocumentEncoder({ imageWidth: config.imageWidth, imageHeight: config.imageHeight })];
    }
  };

  // ── VK ─────────────────────────────────────────────────────────────
  if (config.vkToken1 && config.vkPeerId) {
    const channel1 = new VKChannel({ accessToken: config.vkToken1, peerId: config.vkPeerId, label: 't1' });
    const receiver = getReceiver('vk');
    const encoders = config.receiveMode === 'webhook'
      ? [new TextEncoder()] // Webhook mode: text only (Vercel can't handle doc uploads well)
      : getEncoders();

    for (const encoder of encoders) {
      providers.push(compose(channel1, receiver, encoder));
    }

    // Second token = parallel channel for higher throughput
    if (config.vkToken2) {
      const channel2 = new VKChannel({ accessToken: config.vkToken2, peerId: config.vkPeerId, label: 't2' });
      for (const encoder of encoders) {
        providers.push(compose(channel2, receiver, encoder));
      }
    }
  }

  // ── Telegram ───────────────────────────────────────────────────────
  if (config.tgToken1 && config.tgChatId) {
    // TG supports reading channel posts (channel_post) and group messages
    const channel1 = new TGChannel({
      botToken: config.tgToken1,
      chatId: config.tgChatId,
      label: 't1',
      allowedUpdates: ['message', 'channel_post'], // support public channels!
    });
    const receiver = getReceiver('tg');
    const encoders = [new TextEncoder()]; // TG: text only for now

    for (const encoder of encoders) {
      providers.push(compose(channel1, receiver, encoder));
    }

    if (config.tgToken2) {
      const channel2 = new TGChannel({
        botToken: config.tgToken2,
        chatId: config.tgChatId,
        label: 't2',
        allowedUpdates: ['message', 'channel_post'],
      });
      for (const encoder of encoders) {
        providers.push(compose(channel2, receiver, encoder));
      }
    }
  }

  // ── OK ─────────────────────────────────────────────────────────────
  if (config.okToken && config.okAppKey && config.okAppSecret && config.okChatId) {
    const channel = new OKChannel({
      accessToken: config.okToken,
      applicationKey: config.okAppKey,
      sessionSecretKey: config.okAppSecret,
      chatId: config.okChatId,
      recipientId: config.okRecipientId,
    });
    const receiver = getReceiver('ok');
    const encoders = config.receiveMode === 'webhook'
      ? [new TextEncoder()]
      : getEncoders();

    for (const encoder of encoders) {
      providers.push(compose(channel, receiver, encoder));
    }
  }

  return providers;
}

// ── Main ─────────────────────────────────────────────────────────────────

export async function run(config?: Partial<RunConfig>): Promise<YTransportNode> {
  const mode = (config?.mode || process.env.MODE || 'full') as NodeMode;
  const receiveMode = (config?.receiveMode || process.env.RECEIVE || 'auto') as ReceiveMode;
  const encoding = (config?.encoding || process.env.ENCODING || 'auto') as EncodingMode;

  const resolvedConfig: RunConfig = {
    mode,
    receiveMode,
    encoding,
    socks5Port: config?.socks5Port || parseInt(process.env.SOCKS5_PORT || '1080'),
    httpPort: config?.httpPort || parseInt(process.env.HTTP_PORT || '8080'),
    timerIntervalMs: config?.timerIntervalMs || parseInt(process.env.TIMER_INTERVAL || '3000'),
    imageWidth: config?.imageWidth || parseInt(process.env.IMAGE_WIDTH || '256'),
    imageHeight: config?.imageHeight || parseInt(process.env.IMAGE_HEIGHT || '256'),
    // VK
    vkToken1: config?.vkToken1 || process.env.VK_TOKEN_1,
    vkToken2: config?.vkToken2 || process.env.VK_TOKEN_2,
    vkPeerId: config?.vkPeerId || process.env.VK_PEER_ID,
    vkGroupId: config?.vkGroupId || process.env.VK_GROUP_ID,
    vkConfirmationToken: config?.vkConfirmationToken || process.env.VK_CONFIRMATION_TOKEN,
    vkCallbackSecret: config?.vkCallbackSecret || process.env.VK_CALLBACK_SECRET,
    // TG
    tgToken1: config?.tgToken1 || process.env.TG_TOKEN_1,
    tgToken2: config?.tgToken2 || process.env.TG_TOKEN_2,
    tgChatId: config?.tgChatId || process.env.TG_CHAT_ID,
    // OK
    okToken: config?.okToken || process.env.OK_TOKEN,
    okAppKey: config?.okAppKey || process.env.OK_APP_KEY,
    okAppSecret: config?.okAppSecret || process.env.OK_APP_SECRET,
    okChatId: config?.okChatId || process.env.OK_CHAT_ID,
    okRecipientId: config?.okRecipientId || process.env.OK_RECIPIENT_ID,
    // YDisk
    ydiskRefreshToken: config?.ydiskRefreshToken || process.env.YDISK_REFRESH_TOKEN,
    ydiskClientId: config?.ydiskClientId || process.env.YDISK_CLIENT_ID,
    ydiskClientSecret: config?.ydiskClientSecret || process.env.YDISK_CLIENT_SECRET,
  };

  console.log('╔══════════════════════════════════════════════════════════╗');
  console.log('║              Y Transport — P2P Proxy Node               ║');
  console.log('╠══════════════════════════════════════════════════════════╣');
  console.log(`║  Mode:       ${mode.padEnd(42)}║`);
  console.log(`║  Receive:    ${receiveMode.padEnd(42)}║`);
  console.log(`║  Encoding:   ${encoding.padEnd(42)}║`);
  console.log('╚══════════════════════════════════════════════════════════╝');

  // Build providers from config
  const providers = buildProviders(resolvedConfig);

  if (providers.length === 0) {
    console.error('');
    console.error('No providers configured! Set at least one:');
    console.error('   VK:  VK_TOKEN_1 + VK_PEER_ID');
    console.error('   TG:  TG_TOKEN_1 + TG_CHAT_ID');
    console.error('   OK:  OK_TOKEN + OK_APP_KEY + OK_APP_SECRET + OK_CHAT_ID');
    console.error('');
    console.error('Quick start:');
    console.error('   VK_TOKEN_1=vk1.a.xxx VK_PEER_ID=123 npx ts-node packages/core/run.ts');
    console.error('   TG_TOKEN_1=123:ABC TG_CHAT_ID=-1001234567890 npx ts-node packages/core/run.ts');
    process.exit(1);
  }

  console.log(`\nProviders: ${providers.map(p => p.id).join(', ')}`);

  // Generate or load identity
  const identity = generateIdentity();
  console.log(`Node ID: ${identity.nodeId}`);

  // Open storage
  const store = new MemoryFrameStore();
  await store.open('ytp-data.db');

  // Build and start node
  const nodeConfig: YTransportNodeConfig = {
    identity,
    providers,
    store,
    socks5Port: mode === 'exit' || mode === 'webhook' ? undefined : resolvedConfig.socks5Port,
    httpConnectPort: mode === 'exit' || mode === 'webhook' ? undefined : resolvedConfig.httpPort,
  };

  const node = new YTransportNode(nodeConfig);

  // Graceful shutdown
  const shutdown = async () => {
    console.log('\nShutting down...');
    await node.stop();
    process.exit(0);
  };
  process.on('SIGINT', shutdown);
  process.on('SIGTERM', shutdown);

  await node.start();

  if (mode !== 'exit' && mode !== 'webhook') {
    console.log(`\nProxy ready! Configure your browser/app:`);
    console.log(`   SOCKS5: 127.0.0.1:${resolvedConfig.socks5Port || 1080}`);
    console.log(`   HTTP:   127.0.0.1:${resolvedConfig.httpPort || 8080}`);
  } else if (mode === 'exit') {
    console.log(`\nExit node running. Waiting for peer connections...`);
  } else {
    console.log(`\nWebhook node running.`);
  }

  return node;
}

// Run directly if called as main
if (require.main === module) {
  run().catch(err => {
    console.error('Fatal error:', err);
    process.exit(1);
  });
}
