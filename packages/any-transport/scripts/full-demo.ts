/**
 * YTP Full Demo — Complete demonstration of all providers working together.
 *
 * This script:
 *   1. Initializes all configured providers (VK, TG, OK, Yandex Disk)
 *   2. Sends test messages through each provider
 *   3. Reads messages back
 *   4. Demonstrates parallel send across multiple providers
 *   5. Shows failover behavior
 *
 * Usage:
 *   source .env && npx ts-node scripts/full-demo.ts
 */

import { TelegramProvider } from '../packages/providers/telegram';
import { VKProvider, VKMultiTokenProvider } from '../packages/providers/vk';
import { VKPhotoProvider } from '../packages/providers/vk-photo';
import { OKProvider } from '../packages/providers/ok';
import { OKPhotoProvider } from '../packages/providers/ok-photo';
import { YandexDiskProvider } from '../packages/providers/yandex-disk';
import { VKBrowserBridgeProvider } from '../packages/providers/vk-browser-bridge';
import { getImageStats } from '../packages/providers/image-codec';
import type { Provider, OutboundFrame } from '../packages/providers/provider';

// ── Configuration (all from environment variables) ─────────────────────────

const CONFIG = {
  telegram: {
    tokens: (process.env.TG_TOKENS || process.env.TG_TOKEN_1 || '').split(',').filter(Boolean),
    chatId: process.env.TG_CHAT_ID || '',
  },
  vk: {
    tokens: (process.env.VK_TOKENS || process.env.VK_TOKEN_1 || '').split(',').filter(Boolean),
    peerId: process.env.VK_PEER_ID || '',
  },
  ok: {
    token: process.env.OK_TOKEN || '',
    appKey: process.env.OK_APP_KEY || '',
    chatId: process.env.OK_CHAT_ID || '',
    recipientId: process.env.OK_RECIPIENT_ID || '',
  },
  ydisk: {
    accessToken: process.env.YDISK_TOKEN || '',
    refreshToken: process.env.YDISK_REFRESH_TOKEN || '',
    clientId: process.env.YDISK_CLIENT_ID || '',
    clientSecret: process.env.YDISK_CLIENT_SECRET || '',
  },
};

// ── Helper ────────────────────────────────────────────────────────────────

function makeFrame(text: string): OutboundFrame {
  return {
    text,
    priority: 2,
    deadline: Date.now() + 60000,
  };
}

function sleep(ms: number): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, ms));
}

// ── Provider Test ─────────────────────────────────────────────────────────

async function testProvider(provider: Provider, name: string): Promise<void> {
  console.log(`\n${'─'.repeat(50)}`);
  console.log(`  Testing: ${name}`);
  console.log(`${'─'.repeat(50)}`);

  try {
    await provider.start();
    console.log(`  ✅ ${name} started successfully`);
  } catch (err: any) {
    console.log(`  ❌ ${name} start failed: ${err.message}`);
    return;
  }

  // Test send
  const testMsg = `YTP test ${new Date().toISOString()} from ${name}`;
  try {
    const result = await provider.append(makeFrame(testMsg));
    console.log(`  ✅ Send: msg_id=${result.messageId} (${testMsg.length} chars)`);
  } catch (err: any) {
    console.log(`  ❌ Send failed: ${err.message}`);
  }

  // Test scan
  try {
    const scanResult = await provider.scan(null);
    console.log(`  📨 Scan: ${scanResult.messages.length} messages`);
    for (const msg of scanResult.messages.slice(0, 3)) {
      console.log(`     - ${msg.id}: ${msg.text.slice(0, 50)}...`);
    }
  } catch (err: any) {
    console.log(`  ❌ Scan failed: ${err.message}`);
  }

  // Print capabilities
  const caps = provider.capabilities();
  console.log(`  📊 Capabilities: maxBytes=${caps.maxTextBytes}, minInterval=${caps.minSafeSendIntervalMs}ms`);

  await provider.stop?.();
  console.log(`  🔌 ${name} stopped`);
}

// ── Parallel Send Test ────────────────────────────────────────────────────

async function testParallelSend(providers: { provider: Provider; name: string }[]): Promise<void> {
  console.log(`\n${'═'.repeat(50)}`);
  console.log(`  PARALLEL SEND TEST`);
  console.log(`${'═'.repeat(50)}`);

  const payload = `YTP parallel test ${new Date().toISOString()}`;
  const start = Date.now();

  const results = await Promise.allSettled(
    providers.map(async ({ provider, name }) => {
      const sendStart = Date.now();
      try {
        const result = await provider.append(makeFrame(payload));
        const latency = Date.now() - sendStart;
        return { name, success: true, latency, messageId: result.messageId };
      } catch (err: any) {
        return { name, success: false, latency: Date.now() - sendStart, error: err.message };
      }
    })
  );

  const total = Date.now() - start;
  let succeeded = 0;

  for (const r of results) {
    if (r.status === 'fulfilled') {
      const v = r.value;
      if (v.success) {
        succeeded++;
        console.log(`  ✅ ${v.name}: ${v.latency}ms (msg_id=${v.messageId})`);
      } else {
        console.log(`  ❌ ${v.name}: ${v.error}`);
      }
    }
  }

  console.log(`\n  Parallel send: ${succeeded}/${providers.length} succeeded in ${total}ms`);
  console.log(`  Sequential would take: ~${providers.length * 500}ms`);
  console.log(`  Speedup: ${((providers.length * 500) / total).toFixed(1)}x`);
}

// ── Main ──────────────────────────────────────────────────────────────────

async function main(): Promise<void> {
  console.log('╔══════════════════════════════════════════════════════╗');
  console.log('║         Y TRANSPORT — FULL DEMO                     ║');
  console.log('║         All Providers Test                           ║');
  console.log('╚══════════════════════════════════════════════════════╝\n');

  const activeProviders: { provider: Provider; name: string }[] = [];

  // ── Telegram ────────────────────────────────────────────────────────

  for (let i = 0; i < CONFIG.telegram.tokens.length; i++) {
    const tg = new TelegramProvider({
      botToken: CONFIG.telegram.tokens[i],
      chatId: CONFIG.telegram.chatId,
    });
    activeProviders.push({ provider: tg, name: `Telegram-${i + 1}` });
  }

  // ── VK ──────────────────────────────────────────────────────────────

  for (let i = 0; i < CONFIG.vk.tokens.length; i++) {
    const vk = new VKProvider({
      accessToken: CONFIG.vk.tokens[i],
      peerId: CONFIG.vk.peerId,
      label: `token${i + 1}`,
    });
    activeProviders.push({ provider: vk, name: `VK-${i + 1}` });
  }

  // ── VK Multi-Token ─────────────────────────────────────────────────

  if (CONFIG.vk.tokens.length >= 2) {
    const vkMulti = new VKMultiTokenProvider(
      CONFIG.vk.tokens.map((t, i) => ({
        accessToken: t,
        peerId: CONFIG.vk.peerId,
        label: `multi-${i + 1}`,
      }))
    );
    activeProviders.push({ provider: vkMulti, name: 'VK-Multi' });
  }

  // ── OK ──────────────────────────────────────────────────────────────

  if (CONFIG.ok.token) {
    const okToken = CONFIG.ok.token.includes(':') ? CONFIG.ok.token.split(':')[0] : CONFIG.ok.token;
    const ok = new OKProvider({
      accessToken: okToken,
      applicationKey: CONFIG.ok.appKey,
      sessionSecretKey: okToken.slice(-16),
      chatId: CONFIG.ok.chatId,
      recipientId: CONFIG.ok.recipientId,
    });
    activeProviders.push({ provider: ok, name: 'OK' });
  }

  // ── VK Photo/Doc Transport ──────────────────────────────────────────

  if (CONFIG.vk.tokens.length > 0) {
    const vkPhoto = new VKPhotoProvider({
      accessToken: CONFIG.vk.tokens[0],
      peerId: CONFIG.vk.peerId,
      label: 'doc1',
      useDocuments: true,  // Use document upload (data preserved!)
    });
    activeProviders.push({ provider: vkPhoto, name: 'VK-Doc' });
  }

  // ── OK Photo Transport ──────────────────────────────────────────────

  if (CONFIG.ok.token) {
    const okToken = CONFIG.ok.token.includes(':') ? CONFIG.ok.token.split(':')[0] : CONFIG.ok.token;
    const okPhoto = new OKPhotoProvider({
      accessToken: okToken,
      applicationKey: CONFIG.ok.appKey,
      sessionSecretKey: okToken.slice(-16),
      chatId: CONFIG.ok.chatId,
      recipientId: CONFIG.ok.recipientId,
      label: 'photo1',
    });
    activeProviders.push({ provider: okPhoto, name: 'OK-Photo' });
  }

  // ── VK Browser Bridge ───────────────────────────────────────────────

  const vkBridge = new VKBrowserBridgeProvider({
    wsUrl: 'ws://localhost:9123/bridge',
    peerId: CONFIG.vk.peerId,
  });
  activeProviders.push({ provider: vkBridge, name: 'VK-Bridge' });

  // ── Yandex Disk ──────────────────────────────────────────────────────

  if (CONFIG.ydisk.accessToken) {
    const ydisk = new YandexDiskProvider({
      accessToken: CONFIG.ydisk.accessToken,
      refreshToken: CONFIG.ydisk.refreshToken || undefined,
      clientId: CONFIG.ydisk.clientId || undefined,
      clientSecret: CONFIG.ydisk.clientSecret || undefined,
      label: 'deadend',
    });
    activeProviders.push({ provider: ydisk, name: 'YandexDisk' });
  }

  if (activeProviders.length === 0) {
    console.error('No providers configured! Set environment variables in .env file.');
    console.error('See .env.example for required variables.');
    process.exit(1);
  }

  // ── Individual Tests ────────────────────────────────────────────────

  for (const { provider, name } of activeProviders) {
    await testProvider(provider, name);
    await sleep(500);
  }

  // ── Parallel Test (only started providers) ──────────────────────────

  const startedProviders = activeProviders.filter(p =>
    p.name.startsWith('VK-') || p.name.startsWith('TG-')
  );

  if (startedProviders.length >= 2) {
    // Re-start for parallel test
    for (const { provider } of startedProviders) {
      try { await provider.start?.(); } catch {}
    }

    await testParallelSend(startedProviders);

    for (const { provider } of startedProviders) {
      try { await provider.stop?.(); } catch {}
    }
  }

  console.log('\n✅ Full demo complete!');
}

main().catch(err => {
  console.error('Demo failed:', err);
  process.exit(1);
});
