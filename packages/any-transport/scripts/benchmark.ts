/**
 * Safe carrier capacity model. This command never creates a provider instance
 * and never contacts a provider.
 *
 * Usage:
 *   npm run benchmark -- --json
 *   PROVIDERS=vk,ok npm run benchmark -- --json
 */

type CarrierProfile = {
  id: string;
  payloadBytes: number;
  minSafeSendIntervalMs: number;
  recommendedPollIntervalMs: number;
  source: string;
};

type ModelResult = CarrierProfile & {
  modeledLatencyMs: number;
  modeledThroughputBytesPerSec: number;
  modeledDailyCapacityBytes: number;
};

const DAY_MS = 24 * 60 * 60 * 1000;

// Keep these limits aligned with concrete provider capabilities(). They are
// conservative scheduling inputs, not claims about a provider API or its SLA.
const PROFILES: CarrierProfile[] = [
  { id: 'vk', payloadBytes: 4096, minSafeSendIntervalMs: 350, recommendedPollIntervalMs: 2000, source: 'packages/providers/vk.ts' },
  { id: 'vk-multi-token', payloadBytes: 4096, minSafeSendIntervalMs: 150, recommendedPollIntervalMs: 1500, source: 'packages/providers/vk.ts' },
  { id: 'ok', payloadBytes: 4000, minSafeSendIntervalMs: 250, recommendedPollIntervalMs: 2000, source: 'packages/providers/ok.ts' },
  { id: 'telegram', payloadBytes: 2048, minSafeSendIntervalMs: 1500, recommendedPollIntervalMs: 3000, source: 'packages/providers/telegram.ts' },
  { id: 'yandex-disk', payloadBytes: 4096, minSafeSendIntervalMs: 100, recommendedPollIntervalMs: 3000, source: 'packages/providers/yandex-disk.ts' },
];

function model(profile: CarrierProfile): ModelResult {
  const modeledThroughputBytesPerSec = profile.payloadBytes * 1000 / profile.minSafeSendIntervalMs;
  return {
    ...profile,
    // A receiver can wait up to one recommended poll plus the sender's safe slot.
    modeledLatencyMs: profile.minSafeSendIntervalMs + profile.recommendedPollIntervalMs,
    modeledThroughputBytesPerSec,
    modeledDailyCapacityBytes: modeledThroughputBytesPerSec * DAY_MS / 1000,
  };
}

function selectProfiles(): CarrierProfile[] {
  const requested = (process.env.PROVIDERS || 'all')
    .split(',')
    .map(value => value.trim().toLowerCase())
    .filter(Boolean);
  if (requested.includes('all')) return PROFILES;

  const selected = PROFILES.filter(profile => requested.includes(profile.id));
  const unknown = requested.filter(id => !PROFILES.some(profile => profile.id === id));
  if (unknown.length > 0) throw new Error(`Unknown provider profile(s): ${unknown.join(', ')}`);
  if (selected.length === 0) throw new Error('No provider profiles selected');
  return selected;
}

function formatGiB(bytes: number): string {
  return (bytes / 1024 ** 3).toFixed(2);
}

function main(): void {
  const results = selectProfiles().map(model);
  const report = {
    schemaVersion: 1,
    mode: 'local-capability-model',
    generatedAt: new Date().toISOString(),
    methodology: {
      throughput: 'payloadBytes / minSafeSendIntervalMs; no network I/O',
      latency: 'minSafeSendIntervalMs + recommendedPollIntervalMs; scheduling/poll model only',
      dailyCapacity: 'modeledThroughputBytesPerSec * 86400; sustained 24-hour upper estimate',
    },
    proofBoundary: {
      measured: ['local script execution', 'capability constants read from local adapter source'],
      modeled: ['delivery latency', 'throughput', 'daily capacity'],
      notMeasured: ['provider API latency', 'provider quota', 'network bandwidth', 'remote delivery', 'end-to-end tunnel throughput'],
      networkAccess: false,
    },
    results,
  };

  if (process.argv.includes('--json')) {
    process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
    return;
  }

  console.log('WhiteTransport carrier capacity model (local only; no provider traffic)');
  console.log('Provider\tLatency model\tThroughput model\t24h upper estimate');
  for (const result of results) {
    console.log(`${result.id}\t${result.modeledLatencyMs} ms\t${(result.modeledThroughputBytesPerSec / 1024).toFixed(2)} KiB/s\t${formatGiB(result.modeledDailyCapacityBytes)} GiB`);
  }
  console.log('\nModeled values are not live-provider measurements. Run with --json for the structured report.');
}

try {
  main();
} catch (error) {
  console.error(`Benchmark failed: ${error instanceof Error ? error.message : String(error)}`);
  process.exitCode = 1;
}
