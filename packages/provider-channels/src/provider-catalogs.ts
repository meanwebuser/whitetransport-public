/**
 * Shared transport provider catalogs used by admin UI, clients, and
 * orchestration services when they need the same provider/platform facts.
 */

export interface YtpProviderCatalogEntry {
  readonly id: string;
  readonly name: string;
  readonly mode: string;
  readonly maxMsgSize?: string;
  readonly imageSize?: string;
  readonly dataPerMsg?: string;
  readonly throughput: string;
  readonly throughputKBps: number;
  readonly latency?: string;
  readonly rateLimit: string;
  readonly dailyCap?: string;
  readonly dailyCapGB?: number;
  readonly envVars: readonly string[];
  readonly recommended?: boolean;
  readonly description: string;
}

export interface YtpStrategyCatalogEntry {
  readonly id: string;
  readonly name: string;
  readonly providers: readonly string[];
  readonly providerNames: string;
  readonly totalThroughput: string;
  readonly totalThroughputKBps: number;
  readonly dailyCap: string;
  readonly recommended?: boolean;
}

export interface YtpModeCatalogEntry {
  readonly id: string;
  readonly name: string;
  readonly description: string;
}

export interface YtpProviderCatalog {
  readonly text: readonly YtpProviderCatalogEntry[];
  readonly document: readonly YtpProviderCatalogEntry[];
  readonly photo: readonly YtpProviderCatalogEntry[];
  readonly strategies: readonly YtpStrategyCatalogEntry[];
  readonly modes: readonly YtpModeCatalogEntry[];
}

export interface WbPlatformCatalogEntry {
  readonly id: string;
  readonly name: string;
  readonly description: string;
  readonly modes: readonly string[];
  readonly headlessCreator: string;
  readonly headlessCommand: string;
  readonly joinCommand: string;
  readonly authRequired: string;
  readonly authType: string;
  readonly envVars: readonly string[];
  readonly recommended?: boolean;
  readonly notes: string;
}

export interface WbResourceModeCatalogEntry {
  readonly id: string;
  readonly name: string;
  readonly readBuf: string;
  readonly readBufBytes: number;
  readonly maxDcBuf: string;
  readonly maxDcBufBytes: number;
  readonly memLimit: string;
  readonly memLimitBytes: number;
  readonly description: string;
}

export interface WbTunnelModeCatalogEntry {
  readonly id: string;
  readonly name: string;
  readonly description: string;
  readonly platforms: readonly string[];
}

export interface WbBinaryCatalogEntry {
  readonly name: string;
  readonly platform: string;
  readonly description: string;
}

export interface WbPlatformCatalog {
  readonly platforms: readonly WbPlatformCatalogEntry[];
  readonly resourceModes: readonly WbResourceModeCatalogEntry[];
  readonly tunnelModes: readonly WbTunnelModeCatalogEntry[];
  readonly binaries: readonly WbBinaryCatalogEntry[];
}

export type TransportProviderFamily = 'video-conference' | 'wbstream' | 'provider-log' | 'adapter';

export type TransportProviderPlatform = 'vk' | 'telemost' | 'wbstream' | 'browser' | 'server';

export type VideoConferenceProviderMode = 'datachannel' | 'vp8' | 'dualstream' | 'future-audio';

export type VideoConferenceCarrier = 'datachannel' | 'vp8' | 'dualstream' | 'future-audio';

export type TransportProviderCapability =
  | 'stream'
  | 'datagram'
  | 'rendezvous'
  | 'browser-hook'
  | 'pion-relay'
  | 'room-create'
  | 'room-join'
  | 'health';

export type TransportRuntimeKind = 'browser-hook' | 'headless-creator' | 'pion-relay' | 'adapter';

export interface TransportProviderAdminMetadata {
  readonly label: string;
  readonly description: string;
  readonly recommended?: boolean;
  readonly configSchemaId: string;
  readonly docsPath?: string;
  readonly legacyRuntimeFiles: readonly string[];
}

export interface UnsupportedTransportCombination {
  readonly platform: TransportProviderPlatform;
  readonly mode: VideoConferenceProviderMode;
  readonly reason: string;
}

export interface TransportProviderCatalogEntry {
  readonly id: string;
  readonly family: TransportProviderFamily;
  readonly platform: TransportProviderPlatform;
  readonly mode: VideoConferenceProviderMode;
  readonly carrier: VideoConferenceCarrier;
  readonly capabilities: readonly TransportProviderCapability[];
  readonly runtimeKinds: readonly TransportRuntimeKind[];
  readonly supported: boolean;
  readonly unsupportedReason?: string;
  readonly configSchemaId: string;
  readonly admin: TransportProviderAdminMetadata;
}

export interface VideoConferenceProviderCatalog {
  readonly providers: readonly TransportProviderCatalogEntry[];
  readonly unsupportedCombinations: readonly UnsupportedTransportCombination[];
}

export const YTP_PROVIDER_CATALOG: YtpProviderCatalog = {
  text: [
    {
      id: 'vk-text',
      name: 'VK Text',
      mode: 'Long Poll / Webhook',
      maxMsgSize: '4 KB',
      throughput: '12 KB/s',
      throughputKBps: 12,
      latency: '~200ms',
      rateLimit: '3 req/s/token',
      dailyCap: '~1 GB',
      dailyCapGB: 1,
      envVars: ['VK_TOKEN_1', 'VK_PEER_ID'],
      description: 'VK messaging API via Long Poll or Webhook. Basic text transport.',
    },
    {
      id: 'vk-text-2t',
      name: 'VK Text (2 tokens)',
      mode: 'Multi-token',
      maxMsgSize: '4 KB',
      throughput: '24 KB/s',
      throughputKBps: 24,
      latency: '~200ms',
      rateLimit: '6 req/s',
      dailyCap: '~2 GB',
      dailyCapGB: 2,
      envVars: ['VK_TOKEN_1', 'VK_TOKEN_2', 'VK_PEER_ID'],
      description: 'VK messaging with two tokens for doubled throughput. Independent rate limit pools.',
    },
    {
      id: 'vk-browser-bridge',
      name: 'VK Browser Bridge',
      mode: 'JSONP + WS',
      maxMsgSize: '4 KB',
      throughput: '12 KB/s',
      throughputKBps: 12,
      latency: '~350ms',
      rateLimit: '3 req/s (separate pool)',
      dailyCap: '~1 GB',
      dailyCapGB: 1,
      envVars: ['VK_TOKEN_1', 'VK_PEER_ID'],
      description: 'Kate Mobile OAuth (app_id=2685278) for separate VK rate limit pool via browser-based JSONP API.',
    },
    {
      id: 'tg-bot',
      name: 'Telegram Bot',
      mode: 'Long Poll / Webhook',
      maxMsgSize: '4 KB',
      throughput: '30 KB/s',
      throughputKBps: 30,
      latency: '~100ms',
      rateLimit: '30 msg/s/chat',
      dailyCap: '~10 GB',
      dailyCapGB: 10,
      envVars: ['TG_TOKEN_1', 'TG_CHAT_ID'],
      recommended: true,
      description: 'Telegram Bot API with low latency and generous rate limits. Best text transport.',
    },
    {
      id: 'tg-2bots',
      name: 'Telegram (2 bots)',
      mode: 'Dual bots',
      maxMsgSize: '4 KB',
      throughput: '60 KB/s',
      throughputKBps: 60,
      latency: '~100ms',
      rateLimit: '60 msg/s',
      dailyCap: '~20 GB',
      dailyCapGB: 20,
      envVars: ['TG_TOKEN_1', 'TG_TOKEN_2', 'TG_CHAT_ID'],
      description: 'Two Telegram bots for doubled throughput. Independent rate limit pools.',
    },
    {
      id: 'ok-text',
      name: 'OK Text',
      mode: 'Long Poll / Webhook',
      maxMsgSize: '4 KB',
      throughput: '10 KB/s',
      throughputKBps: 10,
      latency: '~250ms',
      rateLimit: '~2.5 req/s',
      dailyCap: '~844 MB',
      dailyCapGB: 0.844,
      envVars: ['OK_TOKEN', 'OK_APP_KEY', 'OK_CHAT_ID'],
      description: 'OK (Odnoklassniki) messaging API. Lower rate limits but additional channel.',
    },
    {
      id: 'yandex-disk',
      name: 'Yandex Disk',
      mode: 'File upload',
      maxMsgSize: '4 KB/file',
      throughput: '40 KB/s',
      throughputKBps: 40,
      latency: '~500ms',
      rateLimit: '~10-30 req/s',
      dailyCap: '~3.4 GB',
      dailyCapGB: 3.4,
      envVars: ['YDISK_TOKEN', 'YDISK_REFRESH_TOKEN', 'YDISK_CLIENT_ID', 'YDISK_CLIENT_SECRET'],
      description: 'File-based transport via Yandex Disk. Harder to detect, generous rate limits. OAuth auto-refresh built in.',
    },
  ],
  document: [
    {
      id: 'vk-doc-256',
      name: 'VK Document 256×256',
      mode: 'PNG in doc upload',
      imageSize: '256x256',
      dataPerMsg: '192 KB',
      throughput: '576 KB/s',
      throughputKBps: 576,
      rateLimit: '3 req/s/token',
      dailyCap: '~49 GB',
      dailyCapGB: 49,
      envVars: ['VK_TOKEN_1', 'VK_PEER_ID'],
      recommended: true,
      description: 'VK docs.getMessagesUploadServer uploads documents without re-encoding. Data encoded into PNG pixel RGB channels.',
    },
    {
      id: 'vk-doc-1024',
      name: 'VK Document 1024×1024',
      mode: 'PNG in doc upload',
      imageSize: '1024x1024',
      dataPerMsg: '3 MB',
      throughput: '9.2 MB/s',
      throughputKBps: 9200,
      rateLimit: '3 req/s/token',
      dailyCap: '~798 GB',
      dailyCapGB: 798,
      envVars: ['VK_TOKEN_1', 'VK_TOKEN_2', 'VK_PEER_ID'],
      description: 'Maximum bandwidth YTP provider. 1024x1024 PNG = 3MB data per message. Requires two VK tokens.',
    },
    {
      id: 'ok-doc-256',
      name: 'OK Document 256×256',
      mode: 'PNG in doc upload',
      imageSize: '256x256',
      dataPerMsg: '192 KB',
      throughput: '~480 KB/s',
      throughputKBps: 480,
      rateLimit: '~2.5 req/s',
      dailyCap: '~40 GB',
      dailyCapGB: 40,
      envVars: ['OK_TOKEN', 'OK_APP_KEY', 'OK_CHAT_ID'],
      description: 'OK document upload API preserves files without re-encoding. Similar approach to VK Document.',
    },
  ],
  photo: [
    {
      id: 'vk-photo',
      name: 'VK Photo',
      mode: 'Cover image + text',
      throughput: '~12 KB/s',
      throughputKBps: 12,
      rateLimit: '3 req/s/token',
      envVars: ['VK_TOKEN_1', 'VK_PEER_ID'],
      description: 'Steganographic cover. VK re-encodes to JPEG (pixel data lost), so data sent as text with visual cover photo.',
    },
    {
      id: 'ok-photo',
      name: 'OK Photo',
      mode: 'PNG pixel encoding',
      throughput: '~480 KB/s',
      throughputKBps: 480,
      rateLimit: '~2.5 req/s',
      envVars: ['OK_TOKEN', 'OK_APP_KEY', 'OK_CHAT_ID'],
      description: 'OK may preserve PNG data. Encodes data into PNG pixel encoding via OK photos API.',
    },
  ],
  strategies: [
    {
      id: 'minimal',
      name: 'Minimal',
      providers: ['vk-text', 'tg-bot'],
      providerNames: 'VK Text + TG Bot',
      totalThroughput: '42 KB/s',
      totalThroughputKBps: 42,
      dailyCap: '~11 GB',
    },
    {
      id: 'balanced',
      name: 'Balanced',
      providers: ['vk-doc-256', 'tg-bot', 'ok-text'],
      providerNames: 'VK Doc + TG Bot + OK Text',
      totalThroughput: '616 KB/s',
      totalThroughputKBps: 616,
      dailyCap: '~51 GB',
      recommended: true,
    },
    {
      id: 'maximum',
      name: 'Maximum',
      providers: ['vk-doc-256', 'vk-text-2t', 'tg-2bots', 'ok-doc-256', 'yandex-disk'],
      providerNames: 'VK Doc(2t) + TG(2b) + OK Doc + YDisk',
      totalThroughput: '1.7 MB/s',
      totalThroughputKBps: 1700,
      dailyCap: '~162 GB',
    },
    {
      id: 'ultra-doc',
      name: 'Ultra Doc',
      providers: ['vk-doc-1024', 'ok-doc-256'],
      providerNames: 'VK Doc(1024², 2t) + OK Doc(1024²)',
      totalThroughput: '9.7 MB/s',
      totalThroughputKBps: 9700,
      dailyCap: '~839 GB',
    },
    {
      id: 'stealth',
      name: 'Stealth',
      providers: ['yandex-disk'],
      providerNames: 'Yandex Disk only',
      totalThroughput: '40 KB/s',
      totalThroughputKBps: 40,
      dailyCap: '~3.4 GB',
    },
  ],
  modes: [
    { id: 'full', name: 'Full', description: 'SOCKS5 + exit node + long poll. For VPS, Mac, always online.' },
    { id: 'client', name: 'Client', description: 'Only SOCKS5 proxy, long poll. For your laptop behind NAT.' },
    { id: 'exit', name: 'Exit Node', description: 'Only exit node (accepts connections), long poll. For partner VPS.' },
    { id: 'webhook', name: 'Webhook', description: 'Webhook receiver instead of long poll. For Vercel / Cloudflare.' },
  ],
};

export const WB_PLATFORM_CATALOG: WbPlatformCatalog = {
  platforms: [
    {
      id: 'vk',
      name: 'VK Call',
      description: 'Tunnels traffic through VK Call platform via DataChannel or VP8 video. Headless creator uses VK HTTP API directly — no browser needed.',
      modes: ['dc', 'video'],
      headlessCreator: 'headless-vk-creator',
      headlessCommand: './headless-vk-creator --cookies cookies-vk.json --resources default',
      joinCommand: './headless-vk-creator --cookies cookies-vk.json --vk-link https://vk.com/call/join/<token> --resources default',
      authRequired: 'VK cookies (exported from desktop app as JSON)',
      authType: 'cookies',
      envVars: ['VK_COOKIES_PATH'],
      recommended: true,
      notes: 'Supports both DC and Video modes. DC preferred for lower overhead. VK cookies exported as JSON [{"name":"..","value":".."},...].',
    },
    {
      id: 'telemost',
      name: 'Yandex Telemost',
      description: 'Tunnels through Yandex Telemost video calls. Video mode recommended when SFU rate-limits DataChannels. Headless via Telemost HTTP API.',
      modes: ['dc', 'video'],
      headlessCreator: 'headless-telemost-creator',
      headlessCommand: './headless-telemost-creator --cookies cookies-yandex.json --resources default',
      joinCommand: './headless-telemost-creator --cookies cookies-yandex.json --tm-link https://telemost.yandex.ru/j/<id> --resources default',
      authRequired: 'Yandex cookies (exported from desktop app as JSON)',
      authType: 'cookies',
      envVars: ['YANDEX_COOKIES_PATH'],
      notes: 'Video mode preferred — Telemost SFU may rate-limit DataChannels. Cookies exported as JSON.',
    },
    {
      id: 'wbstream',
      name: 'WB Stream',
      description: 'LiveKit-backed platform with anonymous guest tokens. No cookies or authentication required. Video mode mandatory — publisher track is always video.',
      modes: ['video'],
      headlessCreator: 'headless-wbstream-creator',
      headlessCommand: './headless-wbstream-creator --resources default',
      joinCommand: './headless-wbstream-joiner --room <link> --socks-port 1080',
      authRequired: 'None (anonymous guest tokens)',
      authType: 'anonymous',
      envVars: [],
      notes: 'Headless-only — no browser path. Anonymous guest tokens auto-acquired. Video mode mandatory. LiveKit backend.',
    },
  ],
  resourceModes: [
    {
      id: 'moderate',
      name: 'Moderate',
      readBuf: '16 KB',
      readBufBytes: 16384,
      maxDcBuf: '1 MB',
      maxDcBufBytes: 1048576,
      memLimit: '64 MB',
      memLimitBytes: 67108864,
      description: 'Conservative resource usage. Smaller buffers, more frequent backpressure checks.',
    },
    {
      id: 'default',
      name: 'Default',
      readBuf: '32 KB',
      readBufBytes: 32768,
      maxDcBuf: '4 MB',
      maxDcBufBytes: 4194304,
      memLimit: '128 MB',
      memLimitBytes: 134217728,
      description: 'Balanced performance and memory. Recommended for most deployments.',
    },
    {
      id: 'unlimited',
      name: 'Unlimited',
      readBuf: '64 KB',
      readBufBytes: 65536,
      maxDcBuf: '8 MB',
      maxDcBufBytes: 8388608,
      memLimit: '256 MB',
      memLimitBytes: 268435456,
      description: 'Maximum throughput. Larger buffers, more aggressive memory usage.',
    },
    {
      id: 'custom',
      name: 'Custom',
      readBuf: 'Configurable',
      readBufBytes: 0,
      maxDcBuf: 'Configurable',
      maxDcBufBytes: 0,
      memLimit: 'Configurable',
      memLimitBytes: 0,
      description: 'Custom resource settings. Specify --read-buf, --max-dc-buf, --mem-limit flags manually.',
    },
  ],
  tunnelModes: [
    {
      id: 'dc',
      name: 'DataChannel (DC)',
      description: 'Pion opens SCTP DataChannel on publisher PC. Tunnels TCP/UDP through it. Lower overhead, preferred for VK.',
      platforms: ['vk', 'telemost'],
    },
    {
      id: 'video',
      name: 'Video (VP8)',
      description: 'Tunnel rides on published VP8 video track. Useful when SFU rate-limits DataChannels. Mandatory for WB Stream.',
      platforms: ['vk', 'telemost', 'wbstream'],
    },
  ],
  binaries: [
    { name: 'headless-vk-creator', platform: 'VK', description: 'Headless VK creator: creates or joins calls via VK HTTP API' },
    { name: 'headless-telemost-creator', platform: 'Telemost', description: 'Headless Telemost creator with same model' },
    { name: 'headless-wbstream-creator', platform: 'WB Stream', description: 'Headless WB Stream creator (LiveKit-backed, anonymous)' },
    { name: 'headless-wbstream-joiner', platform: 'WB Stream', description: 'Desktop WB Stream joiner for Linux clients' },
    { name: 'headless-telemost-joiner', platform: 'Telemost', description: 'Desktop Telemost joiner for Linux clients' },
    { name: 'headless-vk-bot', platform: 'VK', description: 'Standalone VK Long Poll bot that spawns creators on demand' },
  ],
};

export const VIDEO_CONFERENCE_PROVIDER_CATALOG: VideoConferenceProviderCatalog = {
  providers: [
    {
      id: 'vk-video-datachannel',
      family: 'video-conference',
      platform: 'vk',
      mode: 'datachannel',
      carrier: 'datachannel',
      capabilities: ['stream', 'datagram', 'rendezvous', 'browser-hook', 'pion-relay', 'room-create', 'room-join', 'health'],
      runtimeKinds: ['browser-hook', 'headless-creator', 'pion-relay', 'adapter'],
      supported: true,
      configSchemaId: 'video-conference.v1',
      admin: {
        label: 'VK Video DataChannel',
        description: 'VK Call transport through WebRTC DataChannels exposed by the browser hook and Pion relay.',
        recommended: true,
        configSchemaId: 'video-conference.v1',
        docsPath: 'docs/architecture/vision.md',
        legacyRuntimeFiles: ['whitelist-bypass/hooks/video-vk.js', 'whitelist-bypass/relay/pion/video_vk.go'],
      },
    },
    {
      id: 'vk-video-vp8',
      family: 'video-conference',
      platform: 'vk',
      mode: 'vp8',
      carrier: 'vp8',
      capabilities: ['stream', 'rendezvous', 'browser-hook', 'pion-relay', 'room-create', 'room-join', 'health'],
      runtimeKinds: ['browser-hook', 'headless-creator', 'pion-relay', 'adapter'],
      supported: true,
      configSchemaId: 'video-conference.v1',
      admin: {
        label: 'VK Video VP8',
        description: 'VK Call transport with payload carried over a published VP8 video track.',
        configSchemaId: 'video-conference.v1',
        docsPath: 'docs/architecture/vision.md',
        legacyRuntimeFiles: ['whitelist-bypass/hooks/video-vk.js', 'whitelist-bypass/relay/pion/video_vk.go'],
      },
    },
    {
      id: 'vk-video-dualstream',
      family: 'video-conference',
      platform: 'vk',
      mode: 'dualstream',
      carrier: 'dualstream',
      capabilities: ['stream', 'datagram', 'rendezvous', 'browser-hook', 'pion-relay', 'room-create', 'room-join', 'health'],
      runtimeKinds: ['browser-hook', 'headless-creator', 'pion-relay', 'adapter'],
      supported: true,
      configSchemaId: 'video-conference.v1',
      admin: {
        label: 'VK Video Dualstream',
        description: 'VK Call transport reserving both DataChannel and VP8 carriers for policy-driven failover.',
        configSchemaId: 'video-conference.v1',
        docsPath: 'docs/architecture/vision.md',
        legacyRuntimeFiles: ['whitelist-bypass/hooks/video-vk.js', 'whitelist-bypass/relay/pion/video_vk.go'],
      },
    },
    {
      id: 'telemost-video-vp8',
      family: 'video-conference',
      platform: 'telemost',
      mode: 'vp8',
      carrier: 'vp8',
      capabilities: ['stream', 'rendezvous', 'browser-hook', 'pion-relay', 'room-create', 'room-join', 'health'],
      runtimeKinds: ['browser-hook', 'headless-creator', 'pion-relay', 'adapter'],
      supported: true,
      configSchemaId: 'video-conference.v1',
      admin: {
        label: 'Telemost Video VP8',
        description: 'Yandex Telemost transport with payload carried over the publisher VP8 track.',
        configSchemaId: 'video-conference.v1',
        docsPath: 'docs/architecture/vision.md',
        legacyRuntimeFiles: ['whitelist-bypass/hooks/video-telemost.js', 'whitelist-bypass/relay/pion/video_telemost.go'],
      },
    },
    {
      id: 'telemost-video-dualstream',
      family: 'video-conference',
      platform: 'telemost',
      mode: 'dualstream',
      carrier: 'dualstream',
      capabilities: ['stream', 'rendezvous', 'browser-hook', 'pion-relay', 'room-create', 'room-join', 'health'],
      runtimeKinds: ['browser-hook', 'headless-creator', 'pion-relay', 'adapter'],
      supported: false,
      unsupportedReason: 'Current Telemost Pion relay starts one VP8 tunnel on the pub PeerConnection; no production DataChannel tunnel is exposed.',
      configSchemaId: 'video-conference.v1',
      admin: {
        label: 'Telemost Video Dualstream',
        description: 'Reserved Telemost dual-carrier mode. Cataloged as unsupported until DataChannel tunnel wiring exists.',
        configSchemaId: 'video-conference.v1',
        docsPath: 'docs/architecture/vision.md',
        legacyRuntimeFiles: ['whitelist-bypass/hooks/video-telemost.js', 'whitelist-bypass/relay/pion/video_telemost.go'],
      },
    },
  ],
  unsupportedCombinations: [
    {
      platform: 'telemost',
      mode: 'datachannel',
      reason: 'The current Telemost runtime exposes VP8 tunnel behavior only; DataChannel transport is not a stable adapter contract.',
    },
    {
      platform: 'vk',
      mode: 'future-audio',
      reason: 'Audio carrier fields are reserved for schema stability but audio transport is not implemented in Phase 1.',
    },
    {
      platform: 'telemost',
      mode: 'future-audio',
      reason: 'Audio carrier fields are reserved for schema stability but audio transport is not implemented in Phase 1.',
    },
  ],
};

/**
 * Finds a video-conference provider catalog entry by id.
 *
 * @param providerId Stable video-conference provider id.
 * @returns Matching catalog entry or undefined when unknown.
 */
export function findVideoConferenceProvider(providerId: string): TransportProviderCatalogEntry | undefined {
  return VIDEO_CONFERENCE_PROVIDER_CATALOG.providers.find((provider) => provider.id === providerId);
}
