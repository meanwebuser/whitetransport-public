import type {
  ByteDuplex,
  ProviderBudget,
  ProviderHealth,
  ProviderIdentity,
  StreamTransportChannel,
  TransportEndpoint,
  VideoConferenceProviderConfig,
} from '@whitetransport/provider-channels';
import type {
  VideoConferenceRoomHandle,
  VideoConferenceTransportAdapter,
} from '@whitetransport/video-conference-transport';
import { createVideoConferenceEndpoint } from '@whitetransport/video-conference-transport';

export interface VideoConferenceStreamTransportOptions {
  readonly config: VideoConferenceProviderConfig;
  readonly adapter: VideoConferenceTransportAdapter;
  readonly room?: VideoConferenceRoomHandle;
  readonly budget?: Partial<ProviderBudget>;
}

export class VideoConferenceStreamTransport implements StreamTransportChannel {
  readonly identity: ProviderIdentity;
  readonly budget: ProviderBudget;
  private readonly config: VideoConferenceProviderConfig;
  private readonly adapter: VideoConferenceTransportAdapter;
  private room?: VideoConferenceRoomHandle;

  constructor(options: VideoConferenceStreamTransportOptions) {
    this.config = options.config;
    this.adapter = options.adapter;
    this.room = options.room;
    this.identity = {
      id: options.config.providerId,
      kind: 'video-conference',
      label: options.config.providerId,
      direction: 'duplex',
      encoding: 'video',
    };
    this.budget = {
      maxPayloadBytes: options.budget?.maxPayloadBytes ?? options.config.vp8?.maxPacketBytes ?? 64 * 1024,
      sendsPerMinute: options.budget?.sendsPerMinute ?? 120,
      dailyByteBudget: options.budget?.dailyByteBudget,
    };
  }

  /**
   * Reports adapter health for stream-router route selection.
   *
   * @returns Provider health from the configured video-conference adapter.
   */
  async getHealth(): Promise<ProviderHealth> {
    return this.adapter.getHealth();
  }

  /**
   * Opens a ByteDuplex through the video-conference adapter boundary.
   *
   * @param endpoint Optional room endpoint selected by stream-router.
   * @returns Binary stream exposed by the adapter.
   */
  async connect(endpoint?: TransportEndpoint): Promise<ByteDuplex> {
    const room = this.room ?? await this.resolveRoom(endpoint);
    this.room = room;
    const stream = await this.adapter.openStream({ config: this.config, room, endpoint });
    return stream.duplex;
  }

  /**
   * Creates a stream-router endpoint for the current or configured room.
   *
   * @returns Transport endpoint suitable for StreamTransportDialer routes.
   */
  createEndpoint(): TransportEndpoint {
    if (this.room) return createVideoConferenceEndpoint(this.config.providerId, this.room);
    const roomId = this.config.roomSource.controlRoomId ?? `${this.config.providerId}:pending`;
    return {
      id: `${this.config.providerId}:${roomId}`,
      providerId: this.config.providerId,
      protocol: 'wb-tunnel',
      url: this.config.roomSource.roomUrl,
      metadata: {
        roomId,
        carrier: this.config.carrier,
        mode: this.config.mode,
      },
    };
  }

  private async resolveRoom(endpoint: TransportEndpoint | undefined): Promise<VideoConferenceRoomHandle> {
    if (endpoint?.url || this.config.roomSource.kind === 'existing-room-url') {
      const room: VideoConferenceRoomHandle = {
        roomId: endpoint?.metadata?.roomId ?? this.config.roomSource.controlRoomId ?? `${this.config.providerId}:room`,
        providerId: this.config.providerId,
        url: endpoint?.url ?? this.config.roomSource.roomUrl,
        status: 'joining',
        createdAt: Date.now(),
      };
      return this.adapter.joinRoom({ config: this.config, room });
    }

    return this.adapter.createRoom({ config: this.config });
  }
}
