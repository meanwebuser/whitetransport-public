import {
  WB_PLATFORM_CATALOG,
  findVideoConferenceProvider,
} from './provider-catalogs.js';
import type {
  TransportRuntimeKind,
  VideoConferenceCarrier,
  VideoConferenceProviderMode,
} from './provider-catalogs.js';

export interface BuildYtpEnvFileOptions {
  readonly serverName: string;
  readonly mode: string;
  readonly strategy: string;
  readonly providers: readonly string[];
  readonly envVars: Readonly<Record<string, string>>;
}

export interface BuildWbCreatorCommandOptions {
  readonly platform: string;
  readonly resources: string;
  readonly customResources?: {
    readonly readBuf?: number;
    readonly maxDcBuf?: number;
    readonly memLimit?: number;
  };
  readonly cookiesPath?: string;
  readonly writeFilePath?: string;
}

export type VideoConferenceRoomSourceKind = 'existing-room-url' | 'create-room' | 'control-bus';

export type VideoConferenceRuntimeRole = 'creator' | 'joiner' | 'relay';

export type VideoConferenceBrowserHookMode = 'disabled' | 'inject-vk-hook' | 'inject-telemost-hook';

export interface VideoConferenceRoomSourceConfig {
  readonly kind: VideoConferenceRoomSourceKind;
  readonly roomUrl?: string;
  readonly controlRoomId?: string;
}

export interface VideoConferencePionRelayConfig {
  readonly enabled: boolean;
  readonly signalingUrl?: string;
  readonly listenHost?: string;
  readonly listenPort?: number;
  readonly iceTransportPolicy?: 'relay' | 'all';
}

export interface VideoConferenceVp8Config {
  readonly fps: number;
  readonly batch: number;
  readonly trackCount: number;
  readonly maxPacketBytes: number;
  readonly targetBitrateKbps?: number;
}

export interface ReservedVideoConferenceAudioConfig {
  readonly enabled: false;
  readonly codec?: 'opus';
  readonly targetBitrateKbps?: number;
}

export interface VideoConferenceProviderConfig {
  readonly schemaVersion: 1;
  readonly providerId: string;
  readonly platform: 'vk' | 'telemost';
  readonly mode: VideoConferenceProviderMode;
  readonly carrier: VideoConferenceCarrier;
  readonly runtimeKind: TransportRuntimeKind;
  readonly role: VideoConferenceRuntimeRole;
  readonly roomSource: VideoConferenceRoomSourceConfig;
  readonly browserHookMode: VideoConferenceBrowserHookMode;
  readonly pionRelay: VideoConferencePionRelayConfig;
  readonly vp8?: VideoConferenceVp8Config;
  readonly audio: ReservedVideoConferenceAudioConfig;
  readonly adminMetadata?: Readonly<Record<string, string>>;
}

export interface BuildVideoConferenceProviderConfigOptions {
  readonly providerId: string;
  readonly runtimeKind: TransportRuntimeKind;
  readonly role: VideoConferenceRuntimeRole;
  readonly roomSource: VideoConferenceRoomSourceConfig;
  readonly browserHookMode?: VideoConferenceBrowserHookMode;
  readonly pionRelay?: Partial<VideoConferencePionRelayConfig>;
  readonly vp8?: Partial<VideoConferenceVp8Config>;
  readonly audio?: ReservedVideoConferenceAudioConfig;
  readonly adminMetadata?: Readonly<Record<string, string>>;
}

const VK_PROVIDER_IDS = new Set([
  'vk-text',
  'vk-text-2t',
  'vk-doc-256',
  'vk-doc-1024',
  'vk-photo',
  'vk-browser-bridge',
]);

const SECOND_VK_TOKEN_PROVIDER_IDS = new Set(['vk-text-2t', 'vk-doc-1024']);
const TG_PROVIDER_IDS = new Set(['tg-bot', 'tg-2bots']);
const OK_PROVIDER_IDS = new Set(['ok-text', 'ok-doc-256', 'ok-photo']);

/**
 * Builds the anYTransportProxy env file content for selected providers.
 *
 * @param options Selected mode, strategy, providers, and user env overrides.
 * @returns Full .env text safe to return from admin/API helpers.
 */
export function buildYtpEnvFile(options: BuildYtpEnvFileOptions): string {
  const providerIds = new Set(options.providers);
  const envLines: string[] = [
    '# YTP (anYTransportProxy) Configuration',
    `# Server: ${options.serverName}`,
    `# Mode: ${options.mode}`,
    `# Strategy: ${options.strategy}`,
    '',
  ];

  if (hasAny(providerIds, VK_PROVIDER_IDS)) {
    envLines.push('# ── VK ────────────────────────────────────────');
    envLines.push(`VK_TOKEN_1=${options.envVars.VK_TOKEN_1 || 'vk1.a.your_token...'}`);
    if (hasAny(providerIds, SECOND_VK_TOKEN_PROVIDER_IDS)) {
      envLines.push(`VK_TOKEN_2=${options.envVars.VK_TOKEN_2 || 'vk1.a.second_token...'}`);
    }
    envLines.push(`VK_PEER_ID=${options.envVars.VK_PEER_ID || 'your_peer_id'}`);
    envLines.push('');
  }

  if (hasAny(providerIds, TG_PROVIDER_IDS)) {
    envLines.push('# ── Telegram ──────────────────────────────────');
    envLines.push(`TG_TOKEN_1=${options.envVars.TG_TOKEN_1 || '123456:ABC-DEF...'}`);
    if (providerIds.has('tg-2bots')) {
      envLines.push(`TG_TOKEN_2=${options.envVars.TG_TOKEN_2 || '789012:GHI-JKL...'}`);
    }
    envLines.push(`TG_CHAT_ID=${options.envVars.TG_CHAT_ID || '123456789'}`);
    envLines.push('');
  }

  if (hasAny(providerIds, OK_PROVIDER_IDS)) {
    envLines.push('# ── OK ────────────────────────────────────────');
    envLines.push(`OK_TOKEN=${options.envVars.OK_TOKEN || 'your_token:APP_KEY'}`);
    envLines.push(`OK_CHAT_ID=${options.envVars.OK_CHAT_ID || 'chat:${WT_OK_CHAT_ID}'}`);
    envLines.push('');
  }

  if (providerIds.has('yandex-disk')) {
    envLines.push('# ── Yandex Disk ───────────────────────────────');
    envLines.push(`YDISK_TOKEN=${options.envVars.YDISK_TOKEN || 'y0__your_token...'}`);
    envLines.push(`YDISK_REFRESH_TOKEN=${options.envVars.YDISK_REFRESH_TOKEN || '2:AAA:your_refresh...'}`);
    envLines.push('');
  }

  envLines.push('# ── Mode ──────────────────────────────────────');
  envLines.push(`MODE=${options.mode}`);

  return envLines.join('\n');
}

/**
 * Builds a whitelist-bypass headless creator command for a known platform.
 *
 * @param options Platform id, resources mode, optional cookies, and resource overrides.
 * @returns Command string for systemd, shell, or deployment preview.
 * @throws Error when the platform is not listed in the shared WB catalog.
 */
export function buildWbCreatorCommand(options: BuildWbCreatorCommandOptions): string {
  const platformConfig = WB_PLATFORM_CATALOG.platforms.find((platform) => platform.id === options.platform);
  if (!platformConfig) {
    throw new Error(`Unknown WB platform: ${options.platform}`);
  }

  let command = platformConfig.headlessCommand;

  if (options.cookiesPath && platformConfig.authType === 'cookies') {
    command = `./${platformConfig.headlessCreator} --cookies ${options.cookiesPath}`;
  }

  command = ensureArgument(command, '--resources', options.resources);

  if (options.writeFilePath) {
    command += ` --write-file ${options.writeFilePath}`;
  }

  if (options.resources === 'custom' && options.customResources) {
    if (options.customResources.readBuf) {
      command += ` --read-buf ${options.customResources.readBuf}`;
    }
    if (options.customResources.maxDcBuf && options.platform === 'vk') {
      command += ` --max-dc-buf ${options.customResources.maxDcBuf}`;
    }
    if (options.customResources.memLimit) {
      command += ` --mem-limit ${options.customResources.memLimit}`;
    }
  }

  return command;
}

/**
 * Builds and validates a video-conference transport config from a catalog id.
 *
 * @param options Provider id, room source, runtime role, and mode-specific knobs.
 * @returns Validated video-conference provider config.
 * @throws Error when the provider id, mode, room source, or runtime options are invalid.
 */
export function buildVideoConferenceProviderConfig(
  options: BuildVideoConferenceProviderConfigOptions,
): VideoConferenceProviderConfig {
  const provider = findVideoConferenceProvider(options.providerId);
  if (!provider) throw new Error(`Unknown video-conference provider: ${options.providerId}`);
  if (!provider.supported) {
    throw new Error(`Unsupported video-conference provider ${provider.id}: ${provider.unsupportedReason ?? 'not supported'}`);
  }
  if (provider.platform !== 'vk' && provider.platform !== 'telemost') {
    throw new Error(`Video-conference provider platform is not supported by this config builder: ${provider.platform}`);
  }
  if (!provider.runtimeKinds.includes(options.runtimeKind)) {
    throw new Error(`Runtime ${options.runtimeKind} is not supported by provider ${provider.id}`);
  }

  const config: VideoConferenceProviderConfig = {
    schemaVersion: 1,
    providerId: provider.id,
    platform: provider.platform,
    mode: provider.mode,
    carrier: provider.carrier,
    runtimeKind: options.runtimeKind,
    role: options.role,
    roomSource: options.roomSource,
    browserHookMode: options.browserHookMode ?? defaultBrowserHookMode(provider.platform),
    pionRelay: {
      enabled: options.pionRelay?.enabled ?? provider.runtimeKinds.includes('pion-relay'),
      signalingUrl: options.pionRelay?.signalingUrl,
      listenHost: options.pionRelay?.listenHost ?? '127.0.0.1',
      listenPort: options.pionRelay?.listenPort ?? 9001,
      iceTransportPolicy: options.pionRelay?.iceTransportPolicy ?? 'relay',
    },
    vp8: provider.carrier === 'vp8' || provider.carrier === 'dualstream'
      ? buildVp8Config(options.vp8)
      : undefined,
    audio: options.audio ?? { enabled: false },
    adminMetadata: options.adminMetadata,
  };

  assertVideoConferenceProviderConfig(config);
  return config;
}

/**
 * Validates a video-conference provider config.
 *
 * @param config Config produced by admin, service, or tests.
 * @throws Error when the config does not match the shared Phase 1 contract.
 */
export function assertVideoConferenceProviderConfig(config: VideoConferenceProviderConfig): void {
  if (config.schemaVersion !== 1) throw new Error(`Unsupported video-conference config schema: ${config.schemaVersion}`);
  const provider = findVideoConferenceProvider(config.providerId);
  if (!provider) throw new Error(`Unknown video-conference provider: ${config.providerId}`);
  if (!provider.supported) {
    throw new Error(`Unsupported video-conference provider ${provider.id}: ${provider.unsupportedReason ?? 'not supported'}`);
  }
  if (config.platform !== provider.platform) throw new Error(`Config platform ${config.platform} does not match ${provider.id}`);
  if (config.mode !== provider.mode) throw new Error(`Config mode ${config.mode} does not match ${provider.id}`);
  if (config.carrier !== provider.carrier) throw new Error(`Config carrier ${config.carrier} does not match ${provider.id}`);
  if (!provider.runtimeKinds.includes(config.runtimeKind)) {
    throw new Error(`Runtime ${config.runtimeKind} is not supported by provider ${provider.id}`);
  }
  assertRoomSource(config.roomSource);
  assertPionRelay(config.pionRelay);
  assertBrowserHookMode(config.platform, config.browserHookMode);
  if ((config.carrier === 'vp8' || config.carrier === 'dualstream') && !config.vp8) {
    throw new Error(`${provider.id} requires VP8 packet sizing config`);
  }
  if (config.vp8) assertVp8(config.vp8);
  if (config.audio.enabled !== false) throw new Error('Audio carrier is reserved and must remain disabled in Phase 1');
}

function hasAny(values: ReadonlySet<string>, candidates: ReadonlySet<string>): boolean {
  for (const candidate of candidates) {
    if (values.has(candidate)) {
      return true;
    }
  }
  return false;
}

function ensureArgument(command: string, argName: string, argValue: string): string {
  const parts = command.split(/\s+/);
  const existingIndex = parts.indexOf(argName);
  if (existingIndex === -1) {
    return `${command} ${argName} ${argValue}`;
  }

  const updatedParts = [...parts];
  updatedParts[existingIndex + 1] = argValue;
  return updatedParts.join(' ');
}

function defaultBrowserHookMode(platform: 'vk' | 'telemost'): VideoConferenceBrowserHookMode {
  return platform === 'vk' ? 'inject-vk-hook' : 'inject-telemost-hook';
}

function buildVp8Config(config: Partial<VideoConferenceVp8Config> | undefined): VideoConferenceVp8Config {
  return {
    fps: config?.fps ?? 24,
    batch: config?.batch ?? 30,
    trackCount: config?.trackCount ?? 1,
    maxPacketBytes: config?.maxPacketBytes ?? 1200,
    targetBitrateKbps: config?.targetBitrateKbps,
  };
}

function assertRoomSource(roomSource: VideoConferenceRoomSourceConfig): void {
  if (roomSource.kind === 'existing-room-url' && !roomSource.roomUrl) {
    throw new Error('existing-room-url room source requires roomUrl');
  }
  if (roomSource.kind === 'control-bus' && !roomSource.controlRoomId) {
    throw new Error('control-bus room source requires controlRoomId');
  }
  if (roomSource.kind !== 'existing-room-url' && roomSource.kind !== 'create-room' && roomSource.kind !== 'control-bus') {
    throw new Error(`Unknown video-conference room source: ${String(roomSource.kind)}`);
  }
}

function assertPionRelay(config: VideoConferencePionRelayConfig): void {
  if (config.listenPort !== undefined && (!Number.isInteger(config.listenPort) || config.listenPort < 1 || config.listenPort > 65535)) {
    throw new Error(`Invalid Pion relay listen port: ${String(config.listenPort)}`);
  }
  if (config.iceTransportPolicy !== 'relay' && config.iceTransportPolicy !== 'all') {
    throw new Error(`Invalid Pion relay ICE transport policy: ${String(config.iceTransportPolicy)}`);
  }
}

function assertBrowserHookMode(platform: 'vk' | 'telemost', mode: VideoConferenceBrowserHookMode): void {
  const expected = defaultBrowserHookMode(platform);
  if (mode !== 'disabled' && mode !== expected) {
    throw new Error(`${platform} provider cannot use browser hook mode ${mode}`);
  }
}

function assertVp8(config: VideoConferenceVp8Config): void {
  if (!Number.isInteger(config.fps) || config.fps < 1 || config.fps > 60) throw new Error(`Invalid VP8 fps: ${config.fps}`);
  if (!Number.isInteger(config.batch) || config.batch < 1 || config.batch > 240) throw new Error(`Invalid VP8 batch: ${config.batch}`);
  if (!Number.isInteger(config.trackCount) || config.trackCount < 1 || config.trackCount > 4) {
    throw new Error(`Invalid VP8 track count: ${config.trackCount}`);
  }
  if (!Number.isInteger(config.maxPacketBytes) || config.maxPacketBytes < 256 || config.maxPacketBytes > 65536) {
    throw new Error(`Invalid VP8 max packet bytes: ${config.maxPacketBytes}`);
  }
  if (config.targetBitrateKbps !== undefined && (!Number.isFinite(config.targetBitrateKbps) || config.targetBitrateKbps <= 0)) {
    throw new Error(`Invalid VP8 target bitrate: ${config.targetBitrateKbps}`);
  }
}
