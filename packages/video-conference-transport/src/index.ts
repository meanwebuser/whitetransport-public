import type {
  ByteDuplex,
  ChannelPayload,
  ControlEnvelope,
  ProviderHealth,
  TransportEndpoint,
  VideoConferenceProviderConfig,
} from '@whitetransport/provider-channels';
import { assertVideoConferenceProviderConfig } from '@whitetransport/provider-channels';

export type VideoConferenceRoomStatus = 'new' | 'creating' | 'joining' | 'ready' | 'closing' | 'closed' | 'failed';

export interface VideoConferenceRoomHandle {
  readonly roomId: string;
  readonly providerId: string;
  readonly url?: string;
  readonly status: VideoConferenceRoomStatus;
  readonly createdAt: number;
  readonly metadata?: Readonly<Record<string, string>>;
}

export interface VideoConferenceStreamHandle {
  readonly streamId: string;
  readonly roomId: string;
  readonly endpoint: TransportEndpoint;
  readonly duplex: ByteDuplex;
}

export interface VideoConferenceRuntimeStatus {
  readonly providerId: string;
  readonly status: VideoConferenceRoomStatus;
  readonly activeRoomId?: string;
  readonly activeStreamIds: readonly string[];
  readonly health: ProviderHealth;
  readonly runtimeProcesses?: readonly RuntimeProcessSnapshot[];
  readonly updatedAt: number;
}

export interface CreateRoomOptions {
  readonly config: VideoConferenceProviderConfig;
  readonly roomId?: string;
  readonly metadata?: Readonly<Record<string, string>>;
}

export interface JoinRoomOptions {
  readonly config: VideoConferenceProviderConfig;
  readonly room: VideoConferenceRoomHandle;
}

export interface OpenStreamOptions {
  readonly config: VideoConferenceProviderConfig;
  readonly room: VideoConferenceRoomHandle;
  readonly endpoint?: TransportEndpoint;
  readonly streamId?: string;
}

export interface CloseStreamOptions {
  readonly streamId: string;
}

export interface SendControlMessageOptions {
  readonly envelope: ControlEnvelope;
  readonly payload?: ChannelPayload;
}

export interface VideoConferenceTransportAdapter {
  /**
   * Creates a provider-native room or call.
   *
   * @param options Validated provider config and optional metadata.
   * @returns Stable room handle safe for control-plane publication.
   */
  createRoom(options: CreateRoomOptions): Promise<VideoConferenceRoomHandle>;

  /**
   * Joins an existing provider-native room or call.
   *
   * @param options Validated provider config and room handle.
   * @returns Updated room handle after join.
   */
  joinRoom(options: JoinRoomOptions): Promise<VideoConferenceRoomHandle>;

  /**
   * Opens a binary stream over the configured carrier.
   *
   * @param options Validated provider config, room, and optional endpoint.
   * @returns Stream handle containing a shared ByteDuplex.
   */
  openStream(options: OpenStreamOptions): Promise<VideoConferenceStreamHandle>;

  /**
   * Closes one logical stream.
   *
   * @param options Stream id to close.
   */
  closeStream(options: CloseStreamOptions): Promise<void>;

  /**
   * Reports current adapter health.
   *
   * @returns Provider health snapshot for routing and admin surfaces.
   */
  getHealth(): Promise<ProviderHealth>;

  /**
   * Reports runtime status without leaking provider-specific internals.
   *
   * @returns Runtime status snapshot.
   */
  getRuntimeStatus(): Promise<VideoConferenceRuntimeStatus>;

  /**
   * Sends a typed control envelope through the adapter runtime.
   *
   * @param options Control envelope and optional encoded payload.
   */
  sendControlMessage(options: SendControlMessageOptions): Promise<void>;
}

export interface RuntimeCommand {
  readonly executable: string;
  readonly args: readonly string[];
  readonly cwd?: string;
  readonly env?: Readonly<Record<string, string>>;
  /** Environment keys that must be redacted by logs and admin surfaces. */
  readonly sensitiveEnvKeys?: readonly string[];
}

export interface RuntimeExit {
  readonly code: number | null;
  readonly signal?: string;
  readonly reason?: string;
}

export interface RuntimeProcess {
  readonly pid?: number;
  readonly command: RuntimeCommand;
  readonly exited: Promise<RuntimeExit>;

  /**
   * Stops the supervised runtime process.
   *
   * @param signal Optional process signal name understood by the launcher.
   */
  stop(signal?: string): Promise<void>;
}

export interface RuntimeProcessSnapshot {
  readonly label: string;
  readonly pid?: number;
  readonly command: RuntimeCommand;
  readonly state: 'running' | 'exited';
  readonly exit?: RuntimeExit;
}

export interface RuntimeLauncher {
  /**
   * Launches one legacy runtime process without exposing implementation details.
   *
   * @param command Redaction-aware command descriptor.
   * @returns Supervised runtime process.
   */
  launch(command: RuntimeCommand): Promise<RuntimeProcess>;
}

export interface RuntimeCommandSet {
  readonly createRoom?: RuntimeCommand;
  readonly joinRoom?: RuntimeCommand;
  readonly pionRelay?: RuntimeCommand;
}

export interface RuntimeStreamConnectorOptions {
  readonly config: VideoConferenceProviderConfig;
  readonly room: VideoConferenceRoomHandle;
  readonly endpoint: TransportEndpoint;
  readonly runtimeStatus: VideoConferenceRuntimeStatus;
}

export type RuntimeStreamConnector = (options: RuntimeStreamConnectorOptions) => Promise<ByteDuplex>;

export type RuntimeControlSender = (options: SendControlMessageOptions) => Promise<void>;

export interface RuntimeVideoConferenceAdapterOptions {
  readonly providerId: string;
  readonly launcher: RuntimeLauncher;
  readonly commands: RuntimeCommandSet;
  readonly streamConnector: RuntimeStreamConnector;
  readonly controlSender?: RuntimeControlSender;
  readonly now?: () => number;
  readonly roomUrlReader?: () => Promise<string | undefined>;
}

export interface BuildVkVp8RuntimeCommandsOptions {
  readonly headlessCreatorPath: string;
  readonly cookiesPath: string;
  readonly resources?: string;
  readonly workingDirectory?: string;
  readonly roomOutputPath?: string;
  readonly existingRoomUrl?: string;
  readonly pionRelayPath?: string;
  readonly pionPort?: number;
}

export class RuntimeVideoConferenceAdapter implements VideoConferenceTransportAdapter {
  private readonly providerId: string;
  private readonly launcher: RuntimeLauncher;
  private readonly commands: RuntimeCommandSet;
  private readonly streamConnector: RuntimeStreamConnector;
  private readonly controlSender?: RuntimeControlSender;
  private readonly now: () => number;
  private readonly roomUrlReader?: () => Promise<string | undefined>;
  private health: ProviderHealth = { state: 'offline', failureReason: 'Video-conference runtime has not started' };
  private activeRoom?: VideoConferenceRoomHandle;
  private readonly streams = new Map<string, VideoConferenceStreamHandle>();
  private readonly processes = new Map<string, RuntimeProcessSnapshot>();

  constructor(options: RuntimeVideoConferenceAdapterOptions) {
    this.providerId = options.providerId;
    this.launcher = options.launcher;
    this.commands = options.commands;
    this.streamConnector = options.streamConnector;
    this.controlSender = options.controlSender;
    this.now = options.now ?? (() => Date.now());
    this.roomUrlReader = options.roomUrlReader;
  }

  async createRoom(options: CreateRoomOptions): Promise<VideoConferenceRoomHandle> {
    assertVideoConferenceProviderConfig(options.config);
    this.assertProvider(options.config);
    if (!this.commands.createRoom) throw new Error(`No create-room runtime command configured for ${this.providerId}`);

    await this.launch('createRoom', this.commands.createRoom);
    const roomUrl = await this.roomUrlReader?.();
    this.activeRoom = {
      roomId: options.roomId ?? options.config.roomSource.controlRoomId ?? `${this.providerId}-${this.now()}`,
      providerId: this.providerId,
      url: roomUrl ?? options.config.roomSource.roomUrl,
      status: 'ready',
      createdAt: this.now(),
      metadata: options.metadata,
    };
    this.health = { state: 'healthy', lastOkAt: this.now() };
    return this.activeRoom;
  }

  async joinRoom(options: JoinRoomOptions): Promise<VideoConferenceRoomHandle> {
    assertVideoConferenceProviderConfig(options.config);
    this.assertProvider(options.config);
    if (this.commands.joinRoom) await this.launch('joinRoom', this.commands.joinRoom);
    this.activeRoom = { ...options.room, providerId: this.providerId, status: 'ready' };
    this.health = { state: 'healthy', lastOkAt: this.now() };
    return this.activeRoom;
  }

  async openStream(options: OpenStreamOptions): Promise<VideoConferenceStreamHandle> {
    assertVideoConferenceProviderConfig(options.config);
    this.assertProvider(options.config);
    const room = this.activeRoom ?? await this.joinRoom({ config: options.config, room: options.room });
    if (this.commands.pionRelay && !this.processes.has('pionRelay')) {
      await this.launch('pionRelay', this.commands.pionRelay);
    }

    const endpoint = options.endpoint ?? createVideoConferenceEndpoint(this.providerId, room);
    const runtimeStatus = await this.getRuntimeStatus();
    const duplex = await this.streamConnector({ config: options.config, room, endpoint, runtimeStatus });
    const streamId = options.streamId ?? `${room.roomId}:stream:${this.streams.size + 1}`;
    const stream: VideoConferenceStreamHandle = { streamId, roomId: room.roomId, endpoint, duplex };
    this.streams.set(streamId, stream);
    this.health = { state: 'healthy', lastOkAt: this.now() };
    return stream;
  }

  async closeStream(options: CloseStreamOptions): Promise<void> {
    const stream = this.streams.get(options.streamId);
    if (stream) await stream.duplex.close();
    this.streams.delete(options.streamId);
  }

  async getHealth(): Promise<ProviderHealth> {
    return this.health;
  }

  async getRuntimeStatus(): Promise<VideoConferenceRuntimeStatus> {
    return {
      providerId: this.providerId,
      status: this.activeRoom?.status ?? (this.processes.size > 0 ? 'joining' : 'new'),
      activeRoomId: this.activeRoom?.roomId,
      activeStreamIds: [...this.streams.keys()],
      health: this.health,
      runtimeProcesses: [...this.processes.values()],
      updatedAt: this.now(),
    };
  }

  async sendControlMessage(options: SendControlMessageOptions): Promise<void> {
    if (!this.controlSender) return;
    await this.controlSender(options);
  }

  private assertProvider(config: VideoConferenceProviderConfig): void {
    if (config.providerId !== this.providerId) {
      throw new Error(`Adapter provider ${this.providerId} cannot run config for ${config.providerId}`);
    }
  }

  private async launch(label: string, command: RuntimeCommand): Promise<void> {
    const process = await this.launcher.launch(command);
    const snapshot: RuntimeProcessSnapshot = {
      label,
      pid: process.pid,
      command: redactRuntimeCommand(process.command),
      state: 'running',
    };
    this.processes.set(label, snapshot);
    process.exited.then((exit) => {
      this.processes.set(label, { ...snapshot, state: 'exited', exit });
      if (exit.code !== 0) {
        this.health = {
          state: 'degraded',
          lastFailureAt: this.now(),
          failureReason: `${label} exited with ${exit.code ?? exit.signal ?? 'unknown'}`,
        };
      }
    }).catch((error: unknown) => {
      const message = error instanceof Error ? error.message : String(error);
      this.processes.set(label, { ...snapshot, state: 'exited', exit: { code: null, reason: message } });
      this.health = { state: 'degraded', lastFailureAt: this.now(), failureReason: message };
    });
  }
}

/**
 * Builds guarded VK VP8 runtime commands for the legacy headless creator path.
 *
 * @param options Paths and room settings for a VK VP8 runtime.
 * @returns Command set suitable for RuntimeVideoConferenceAdapter.
 */
export function buildVkVp8RuntimeCommands(options: BuildVkVp8RuntimeCommandsOptions): RuntimeCommandSet {
  const resources = options.resources ?? 'default';
  const baseArgs = ['--cookies', options.cookiesPath, '--resources', resources];
  const createRoomArgs = options.roomOutputPath
    ? [...baseArgs, '--write-file', options.roomOutputPath]
    : baseArgs;
  const joinRoomArgs = options.existingRoomUrl
    ? [...baseArgs, '--vk-link', options.existingRoomUrl]
    : baseArgs;

  return {
    createRoom: {
      executable: options.headlessCreatorPath,
      args: createRoomArgs,
      cwd: options.workingDirectory,
      sensitiveEnvKeys: [],
    },
    joinRoom: {
      executable: options.headlessCreatorPath,
      args: joinRoomArgs,
      cwd: options.workingDirectory,
      sensitiveEnvKeys: [],
    },
    pionRelay: options.pionRelayPath
      ? {
          executable: options.pionRelayPath,
          args: ['--platform', 'vk', '--port', String(options.pionPort ?? 9001)],
          cwd: options.workingDirectory,
          sensitiveEnvKeys: [],
        }
      : undefined,
  };
}

/**
 * Redacts runtime command environment values before admin/status exposure.
 *
 * @param command Runtime command descriptor.
 * @returns Command with sensitive env values replaced.
 */
export function redactRuntimeCommand(command: RuntimeCommand): RuntimeCommand {
  if (!command.env || !command.sensitiveEnvKeys || command.sensitiveEnvKeys.length === 0) return command;
  const env: Record<string, string> = { ...command.env };
  for (const key of command.sensitiveEnvKeys) {
    if (Object.prototype.hasOwnProperty.call(env, key)) env[key] = '[redacted]';
  }
  return { ...command, env };
}

export interface MemoryVideoConferenceAdapterOptions {
  readonly now?: () => number;
  readonly roomUrlPrefix?: string;
}

export class MemoryVideoConferenceAdapter implements VideoConferenceTransportAdapter {
  private readonly now: () => number;
  private readonly roomUrlPrefix: string;
  private health: ProviderHealth = { state: 'healthy' };
  private activeRoom?: VideoConferenceRoomHandle;
  private readonly streams = new Map<string, VideoConferenceStreamHandle>();
  private readonly controlMessages: ControlEnvelope[] = [];

  constructor(options: MemoryVideoConferenceAdapterOptions = {}) {
    this.now = options.now ?? (() => Date.now());
    this.roomUrlPrefix = options.roomUrlPrefix ?? 'memory://video-room';
  }

  async createRoom(options: CreateRoomOptions): Promise<VideoConferenceRoomHandle> {
    assertVideoConferenceProviderConfig(options.config);
    const roomId = options.roomId ?? `${options.config.providerId}-${this.now()}`;
    this.activeRoom = {
      roomId,
      providerId: options.config.providerId,
      url: `${this.roomUrlPrefix}/${roomId}`,
      status: 'ready',
      createdAt: this.now(),
      metadata: options.metadata,
    };
    this.health = { state: 'healthy', lastOkAt: this.now() };
    return this.activeRoom;
  }

  async joinRoom(options: JoinRoomOptions): Promise<VideoConferenceRoomHandle> {
    assertVideoConferenceProviderConfig(options.config);
    this.activeRoom = { ...options.room, status: 'ready' };
    this.health = { state: 'healthy', lastOkAt: this.now() };
    return this.activeRoom;
  }

  async openStream(options: OpenStreamOptions): Promise<VideoConferenceStreamHandle> {
    assertVideoConferenceProviderConfig(options.config);
    const endpoint = options.endpoint ?? createVideoConferenceEndpoint(options.config.providerId, options.room);
    const streamId = options.streamId ?? `${options.room.roomId}:stream:${this.streams.size + 1}`;
    const stream: VideoConferenceStreamHandle = {
      streamId,
      roomId: options.room.roomId,
      endpoint,
      duplex: new MemoryByteDuplex(),
    };
    this.streams.set(streamId, stream);
    this.health = { state: 'healthy', lastOkAt: this.now() };
    return stream;
  }

  async closeStream(options: CloseStreamOptions): Promise<void> {
    const stream = this.streams.get(options.streamId);
    if (stream) await stream.duplex.close();
    this.streams.delete(options.streamId);
  }

  async getHealth(): Promise<ProviderHealth> {
    return this.health;
  }

  async getRuntimeStatus(): Promise<VideoConferenceRuntimeStatus> {
    return {
      providerId: this.activeRoom?.providerId ?? 'unbound',
      status: this.activeRoom?.status ?? 'new',
      activeRoomId: this.activeRoom?.roomId,
      activeStreamIds: [...this.streams.keys()],
      health: this.health,
      updatedAt: this.now(),
    };
  }

  async sendControlMessage(options: SendControlMessageOptions): Promise<void> {
    this.controlMessages.push(options.envelope);
  }

  getControlMessages(): readonly ControlEnvelope[] {
    return this.controlMessages;
  }
}

export function createVideoConferenceEndpoint(
  providerId: string,
  room: VideoConferenceRoomHandle,
): TransportEndpoint {
  return {
    id: `${providerId}:${room.roomId}`,
    providerId,
    protocol: 'wb-tunnel',
    url: room.url,
    metadata: {
      roomId: room.roomId,
      carrier: 'video-conference',
    },
  };
}

class MemoryByteDuplex implements ByteDuplex {
  private readonly queue: Uint8Array[] = [];
  private closed = false;

  async write(chunk: Uint8Array): Promise<void> {
    if (this.closed) throw new Error('Memory video-conference stream is closed');
    this.queue.push(new Uint8Array(chunk));
  }

  async read(): Promise<Uint8Array | null> {
    return this.queue.shift() ?? null;
  }

  async close(): Promise<void> {
    this.closed = true;
    this.queue.length = 0;
  }
}
