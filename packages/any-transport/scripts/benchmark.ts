/**
 * YTP Speed Benchmark — Real speed tests for single and parallel providers.
 *
 * Tests:
 *   1. Single provider: latency, throughput (bytes/s), message rate (msg/s)
 *   2. Parallel providers: aggregate throughput with multiple providers
 *   3. Overhead analysis: raw vs effective throughput
 *
 * Usage:
 *   npx ts-node scripts/benchmark.ts
 *   PROVIDERS=vk,tg,ok npx ts-node scripts/benchmark.ts
 *   MODE=parallel npx ts-node scripts/benchmark.ts
 */

import { TelegramProvider } from '../packages/providers/telegram';
import { VKProvider, VKMultiTokenProvider } from '../packages/providers/vk';
import { OKProvider } from '../packages/providers/ok';
import type { Provider, OutboundFrame, ProviderCapabilities } from '../packages/providers/provider';

// ── Configuration ─────────────────────────────────────────────────────────

const TG_TOKEN_1 = process.env.TG_TOKEN_1 || '';
const TG_TOKEN_2 = process.env.TG_TOKEN_2 || '';
const TG_CHAT_ID = process.env.TG_CHAT_ID || '';

const VK_TOKEN_1 = process.env.VK_TOKEN_1 || '';
const VK_TOKEN_2 = process.env.VK_TOKEN_2 || '';
const VK_PEER_ID = process.env.VK_PEER_ID || '';

const OK_TOKEN = process.env.OK_TOKEN || '';
const OK_CHAT_ID = process.env.OK_CHAT_ID || '';

const MESSAGE_COUNT = parseInt(process.env.MSG_COUNT || '10', 10);
const MODE = process.env.MODE || 'all'; // 'single', 'parallel', 'all'
const PROVIDERS_ENV = process.env.PROVIDERS || 'vk,tg,ok';

// ── Types ─────────────────────────────────────────────────────────────────

interface BenchmarkResult {
  provider: string;
  messagesSent: number;
  totalBytes: number;
  totalTimeMs: number;
  avgLatencyMs: number;
  minLatencyMs: number;
  maxLatencyMs: number;
  throughputBytesPerSec: number;
  messageRate: number;      // messages per second
  effectiveRate: number;    // bytes/s accounting for overhead
  overheadPercent: number;  // protocol overhead percentage
  errors: number;
  capabilities: ProviderCapabilities;
}

// ── Payload Generation ────────────────────────────────────────────────────

function generatePayload(size: number): string {
  // Generate YTP-like envelope with realistic overhead
  const header = `YT1.sess01.0.A.${Date.now()}.0.D.nonce01.`;
  const maxPayload = size - header.length - 32; // 32 for tag/ciphertext overhead
  const payloadBytes = Math.max(50, maxPayload);
  const body = 'A'.repeat(payloadBytes);
  const tag = '0'.repeat(16);
  return `${header}${body}.${tag}.3600000`;
}

// ── Single Provider Benchmark ─────────────────────────────────────────────

async function benchmarkSingleProvider(provider: Provider, name: string): Promise<BenchmarkResult> {
  console.log(`\n${'═'.repeat(60)}`);
  console.log(`  BENCHMARK: ${name} (single provider)`);
  console.log(`  Messages: ${MESSAGE_COUNT}`);
  console.log(`${'═'.repeat(60)}\n`);

  const caps = provider.capabilities();
  const payloadSize = Math.min(caps.maxTextBytes, 2048); // Use 2KB or provider max
  const payload = generatePayload(payloadSize);

  const latencies: number[] = [];
  let totalBytes = 0;
  let errors = 0;
  const startTime = Date.now();

  for (let i = 0; i < MESSAGE_COUNT; i++) {
    const frame: OutboundFrame = {
      text: payload,
      priority: 2,
      deadline: Date.now() + 60000,
    };

    const sendStart = Date.now();

    try {
      await provider.append(frame);
      const latency = Date.now() - sendStart;
      latencies.push(latency);
      totalBytes += payload.length;
      console.log(`  [${i + 1}/${MESSAGE_COUNT}] ${latency}ms — ${payload.length} bytes`);
    } catch (err: any) {
      errors++;
      console.error(`  [${i + 1}/${MESSAGE_COUNT}] ERROR: ${err.message}`);
    }

    // Respect rate limits
    const minInterval = caps.minSafeSendIntervalMs;
    if (i < MESSAGE_COUNT - 1) {
      await sleep(minInterval);
    }
  }

  const totalTime = Date.now() - startTime;

  const result: BenchmarkResult = {
    provider: name,
    messagesSent: MESSAGE_COUNT - errors,
    totalBytes,
    totalTimeMs: totalTime,
    avgLatencyMs: latencies.length > 0 ? latencies.reduce((a, b) => a + b, 0) / latencies.length : 0,
    minLatencyMs: latencies.length > 0 ? Math.min(...latencies) : 0,
    maxLatencyMs: latencies.length > 0 ? Math.max(...latencies) : 0,
    throughputBytesPerSec: totalTime > 0 ? (totalBytes / totalTime) * 1000 : 0,
    messageRate: totalTime > 0 ? ((MESSAGE_COUNT - errors) / totalTime) * 1000 : 0,
    effectiveRate: 0,
    overheadPercent: 0,
    errors,
    capabilities: caps,
  };

  // Calculate overhead (header/tag vs payload)
  const headerOverhead = payload.indexOf('A'.repeat(50)); // Approximate
  const totalWithOverhead = payload.length * MESSAGE_COUNT;
  const usefulData = (payload.length - headerOverhead) * MESSAGE_COUNT;
  result.overheadPercent = totalWithOverhead > 0 ? ((1 - usefulData / totalWithOverhead) * 100) : 0;
  result.effectiveRate = totalTime > 0 ? (usefulData / totalTime) * 1000 : 0;

  return result;
}

// ── Parallel Provider Benchmark ───────────────────────────────────────────

async function benchmarkParallelProviders(providers: { provider: Provider; name: string }[]): Promise<BenchmarkResult> {
  const name = providers.map(p => p.name).join(' + ');
  console.log(`\n${'═'.repeat(60)}`);
  console.log(`  BENCHMARK: ${name} (PARALLEL)`);
  console.log(`  Messages per provider: ${MESSAGE_COUNT}`);
  console.log(`  Total messages: ${MESSAGE_COUNT * providers.length}`);
  console.log(`${'═'.repeat(60)}\n`);

  const payloadSize = 2048;
  let totalBytes = 0;
  let totalMessages = 0;
  let errors = 0;
  const startTime = Date.now();

  // Run all providers in parallel
  const results = await Promise.allSettled(
    providers.map(async ({ provider, name: pName }) => {
      const caps = provider.capabilities();
      const payload = generatePayload(Math.min(caps.maxTextBytes, payloadSize));
      let pBytes = 0;
      let pMsgs = 0;
      let pErrors = 0;

      for (let i = 0; i < MESSAGE_COUNT; i++) {
        const frame: OutboundFrame = {
          text: payload,
          priority: 2,
          deadline: Date.now() + 60000,
        };

        try {
          const sendStart = Date.now();
          await provider.append(frame);
          const latency = Date.now() - sendStart;
          pBytes += payload.length;
          pMsgs++;
          console.log(`  [${pName} ${i + 1}/${MESSAGE_COUNT}] ${latency}ms — ${payload.length} bytes`);
        } catch (err: any) {
          pErrors++;
          console.error(`  [${pName} ${i + 1}/${MESSAGE_COUNT}] ERROR: ${err.message}`);
        }

        await sleep(caps.minSafeSendIntervalMs);
      }

      return { bytes: pBytes, msgs: pMsgs, errors: pErrors };
    })
  );

  for (const r of results) {
    if (r.status === 'fulfilled') {
      totalBytes += r.value.bytes;
      totalMessages += r.value.msgs;
      errors += r.value.errors;
    }
  }

  const totalTime = Date.now() - startTime;
  const caps = providers[0].provider.capabilities();

  return {
    provider: name,
    messagesSent: totalMessages,
    totalBytes,
    totalTimeMs: totalTime,
    avgLatencyMs: totalTime / Math.max(totalMessages, 1),
    minLatencyMs: 0,
    maxLatencyMs: 0,
    throughputBytesPerSec: totalTime > 0 ? (totalBytes / totalTime) * 1000 : 0,
    messageRate: totalTime > 0 ? (totalMessages / totalTime) * 1000 : 0,
    effectiveRate: totalTime > 0 ? (totalBytes * 0.85 / totalTime) * 1000 : 0, // ~15% overhead
    overheadPercent: 15,
    errors,
    capabilities: caps,
  };
}

// ── Scan Latency Test ─────────────────────────────────────────────────────

async function benchmarkScanLatency(provider: Provider, name: string): Promise<void> {
  console.log(`\n  SCAN LATENCY TEST: ${name}`);

  const scanTimes: number[] = [];

  for (let i = 0; i < 5; i++) {
    const start = Date.now();
    await provider.scan(null);
    const elapsed = Date.now() - start;
    scanTimes.push(elapsed);
    console.log(`    Scan ${i + 1}: ${elapsed}ms`);
    await sleep(1000);
  }

  const avg = scanTimes.reduce((a, b) => a + b, 0) / scanTimes.length;
  console.log(`    Average scan latency: ${avg.toFixed(0)}ms`);
}

// ── Report Printing ───────────────────────────────────────────────────────

function printReport(results: BenchmarkResult[]): void {
  console.log(`\n${'═'.repeat(80)}`);
  console.log('  SPEED BENCHMARK RESULTS');
  console.log(`${'═'.repeat(80)}\n`);

  // Table header
  const header = '| Provider'.padEnd(22) + '| Msg/s'.padEnd(10) + '| KB/s'.padEnd(10) + '| Avg Lat'.padEnd(10) + '| Min Lat'.padEnd(10) + '| Max Lat'.padEnd(10) + '| Errors'.padEnd(8) + '|';
  const separator = '|' + '-'.repeat(21) + '|' + '-'.repeat(9) + '|' + '-'.repeat(9) + '|' + '-'.repeat(9) + '|' + '-'.repeat(9) + '|' + '-'.repeat(9) + '|' + '-'.repeat(7) + '|';

  console.log(header);
  console.log(separator);

  for (const r of results) {
    const row = `| ${r.provider}`.padEnd(22) +
      `| ${(r.messageRate).toFixed(2)}`.padEnd(10) +
      `| ${(r.throughputBytesPerSec / 1024).toFixed(2)}`.padEnd(10) +
      `| ${r.avgLatencyMs.toFixed(0)}ms`.padEnd(10) +
      `| ${r.minLatencyMs}ms`.padEnd(10) +
      `| ${r.maxLatencyMs}ms`.padEnd(10) +
      `| ${r.errors}`.padEnd(8) + '|';
    console.log(row);
  }

  console.log(separator);

  // Summary
  const totalThroughput = results.reduce((sum, r) => sum + r.throughputBytesPerSec, 0);
  const totalMsgRate = results.reduce((sum, r) => sum + r.messageRate, 0);

  console.log(`\n  AGGREGATE THROUGHPUT: ${(totalThroughput / 1024).toFixed(2)} KB/s`);
  console.log(`  AGGREGATE MSG RATE: ${totalMsgRate.toFixed(2)} msg/s`);
  console.log(`  THEORETICAL MAX (parallel): ${(totalThroughput / 1024).toFixed(2)} KB/s`);

  // Throughput optimization suggestions
  console.log(`\n${'─'.repeat(80)}`);
  console.log('  OPTIMIZATION ANALYSIS');
  console.log(`${'─'.repeat(80)}\n`);

  for (const r of results) {
    const caps = r.capabilities;
    const theoreticalMax = (caps.maxTextBytes / caps.minSafeSendIntervalMs) * 1000;
    const efficiency = theoreticalMax > 0 ? (r.throughputBytesPerSec / theoreticalMax) * 100 : 0;

    console.log(`  ${r.provider}:`);
    console.log(`    Theoretical max: ${(theoreticalMax / 1024).toFixed(2)} KB/s`);
    console.log(`    Achieved:        ${(r.throughputBytesPerSec / 1024).toFixed(2)} KB/s (${efficiency.toFixed(1)}% efficiency)`);
    console.log(`    Overhead:        ${r.overheadPercent.toFixed(1)}%`);
    console.log(`    Min interval:    ${caps.minSafeSendIntervalMs}ms`);
    console.log(`    Max msg size:    ${caps.maxTextBytes} bytes`);

    if (efficiency < 50) {
      console.log(`    ⚠ Low efficiency — consider reducing minSafeSendIntervalMs or increasing maxTextBytes`);
    }
    console.log();
  }
}

// ── Main ──────────────────────────────────────────────────────────────────

async function main(): Promise<void> {
  console.log('╔══════════════════════════════════════════════════════════════╗');
  console.log('║         Y TRANSPORT — REAL SPEED BENCHMARK                 ║');
  console.log('║         Testing with real API tokens                        ║');
  console.log('╚══════════════════════════════════════════════════════════════╝\n');

  const enabledProviders = PROVIDERS_ENV.split(',').map(p => p.trim().toLowerCase());
  const results: BenchmarkResult[] = [];

  const providerInstances: { provider: Provider; name: string }[] = [];

  // ── Initialize Providers ──────────────────────────────────────────────

  if (enabledProviders.includes('vk')) {
    try {
      const vk1 = new VKProvider({
        accessToken: VK_TOKEN_1,
        peerId: VK_PEER_ID,
        label: 'token1',
      });
      await vk1.start();
      providerInstances.push({ provider: vk1, name: 'VK-1' });

      if (VK_TOKEN_2) {
        const vk2 = new VKProvider({
          accessToken: VK_TOKEN_2,
          peerId: VK_PEER_ID,
          label: 'token2',
        });
        await vk2.start();
        providerInstances.push({ provider: vk2, name: 'VK-2' });
      }
    } catch (err: any) {
      console.error(`VK init error: ${err.message}`);
    }
  }

  if (enabledProviders.includes('tg')) {
    try {
      const tg1 = new TelegramProvider({
        botToken: TG_TOKEN_1,
        chatId: TG_CHAT_ID,
      });
      await tg1.start();
      providerInstances.push({ provider: tg1, name: 'TG-1' });

      if (TG_TOKEN_2) {
        const tg2 = new TelegramProvider({
          botToken: TG_TOKEN_2,
          chatId: TG_CHAT_ID,
        });
        await tg2.start();
        providerInstances.push({ provider: tg2, name: 'TG-2' });
      }
    } catch (err: any) {
      console.error(`TG init error: ${err.message}`);
    }
  }

  if (enabledProviders.includes('ok')) {
    try {
      const ok1 = new OKProvider({
        accessToken: OK_TOKEN.split(':')[0],
        applicationKey: process.env.OK_APP_KEY || '',
        sessionSecretKey: OK_TOKEN ? OK_TOKEN.split(':')[0].slice(-16) : '',
        chatId: OK_CHAT_ID,
        recipientId: process.env.OK_RECIPIENT_ID || '',
      });
      await ok1.start();
      providerInstances.push({ provider: ok1, name: 'OK-1' });
    } catch (err: any) {
      console.error(`OK init error: ${err.message}`);
    }
  }

  if (providerInstances.length === 0) {
    console.error('No providers initialized! Check your tokens.');
    process.exit(1);
  }

  console.log(`\nInitialized ${providerInstances.length} providers: ${providerInstances.map(p => p.name).join(', ')}\n`);

  // ── Single Provider Benchmarks ────────────────────────────────────────

  if (MODE === 'single' || MODE === 'all') {
    for (const { provider, name } of providerInstances) {
      try {
        const result = await benchmarkSingleProvider(provider, name);
        results.push(result);

        // Also test scan latency
        await benchmarkScanLatency(provider, name);
      } catch (err: any) {
        console.error(`Benchmark error for ${name}: ${err.message}`);
      }
    }
  }

  // ── Parallel Provider Benchmark ───────────────────────────────────────

  if (MODE === 'parallel' || MODE === 'all') {
    if (providerInstances.length >= 2) {
      try {
        const parallelResult = await benchmarkParallelProviders(providerInstances);
        results.push(parallelResult);
      } catch (err: any) {
        console.error(`Parallel benchmark error: ${err.message}`);
      }
    }

    // Test VK multi-token specifically
    const vkProviders = providerInstances.filter(p => p.name.startsWith('VK'));
    if (vkProviders.length >= 2) {
      try {
        const vkMulti = new VKMultiTokenProvider([
          { accessToken: VK_TOKEN_1, peerId: VK_PEER_ID, label: 't1' },
          { accessToken: VK_TOKEN_2, peerId: VK_PEER_ID, label: 't2' },
        ]);
        await vkMulti.start();

        const multiResult = await benchmarkSingleProvider(vkMulti, 'VK-MultiToken');
        results.push(multiResult);

        await vkMulti.stop();
      } catch (err: any) {
        console.error(`VK MultiToken benchmark error: ${err.message}`);
      }
    }
  }

  // ── Print Final Report ────────────────────────────────────────────────

  printReport(results);

  // ── Cleanup ───────────────────────────────────────────────────────────

  for (const { provider } of providerInstances) {
    if (provider.stop) await provider.stop();
  }

  console.log('\n✅ Benchmark complete!');
}

function sleep(ms: number): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, ms));
}

main().catch(err => {
  console.error('Benchmark failed:', err);
  process.exit(1);
});
