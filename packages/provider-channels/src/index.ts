/**
 * Shared provider-channel contracts for discovery, control, and transport
 * messages. Implementations belong in product packages or services; this
 * package owns the stable boundary between them.
 */

export * from './control-messages.js';
export * from './control-bus.js';
export * from './discovery-bus.js';
export * from './provider-catalogs.js';
export * from './provider-configs.js';

export type ProviderKind =
  | "vk"
  | "telegram"
  | "ok"
  | "wbstream"
  | "whitelist-bypass"
  | "video-conference"
  | "yandex-disk"
  | "mailru-cloud"
  | "sbercloud"
  | "memory";

export type ProviderDirection = "publish" | "subscribe" | "duplex";

export type ProviderEncoding = "text" | "document" | "photo" | "audio" | "file" | "stream" | "video";

export type ProviderHealthState = "healthy" | "degraded" | "offline";

export type ControlMessageKind =
  | "room_state"
  | "client_feedback"
  | "admin_command"
  | "provider_probe"
  | "transport_endpoint"
  | "transport_payload";

export type TransportProtocol = "wisp" | "wb-tunnel" | "socks5" | "http-connect";

export interface ProviderIdentity {
  /** Stable provider id, unique inside one deployment policy. */
  readonly id: string;
  /** Provider family, independent from token/account identity. */
  readonly kind: ProviderKind;
  /** Human readable name for admin UI and logs. */
  readonly label: string;
  /** Direction supported by this provider instance. */
  readonly direction: ProviderDirection;
  /** Encoding used for payload transport over the provider. */
  readonly encoding: ProviderEncoding;
}

export interface ProviderBudget {
  /** Maximum payload bytes per send operation after provider encoding. */
  readonly maxPayloadBytes: number;
  /** Sustained send operations per minute for one credential/account. */
  readonly sendsPerMinute: number;
  /** Optional daily byte budget when the provider has practical quotas. */
  readonly dailyByteBudget?: number;
}

export interface ProviderHealth {
  /** Current health state used by failover selection. */
  readonly state: ProviderHealthState;
  /** Unix timestamp in milliseconds for the most recent successful operation. */
  readonly lastOkAt?: number;
  /** Unix timestamp in milliseconds for the most recent failed operation. */
  readonly lastFailureAt?: number;
  /** Short failure reason safe to show in admin UI. */
  readonly failureReason?: string;
  /** Observed round-trip latency in milliseconds. */
  readonly latencyMs?: number;
}

export interface ProviderCursor {
  /** Provider-specific cursor value for incremental reads. */
  readonly value: string;
}

export interface ChannelPayload {
  /** Logical message kind before provider-specific encoding. */
  readonly kind: ControlMessageKind;
  /** Monotonic sender timestamp in milliseconds. */
  readonly createdAt: number;
  /** Optional expiry timestamp in milliseconds. */
  readonly expiresAt?: number;
  /** Binary-safe payload bytes. */
  readonly body: Uint8Array;
  /** Metadata safe for logs and admin UI. */
  readonly metadata?: Readonly<Record<string, string>>;
}

export interface PublishedMessage {
  /** Provider identity that accepted the payload. */
  readonly providerId: string;
  /** Provider-native message id, post id, file id, or equivalent. */
  readonly remoteId: string;
  /** Cursor after the published message when the provider exposes one. */
  readonly cursor?: ProviderCursor;
  /** Unix timestamp in milliseconds when the provider accepted the message. */
  readonly acceptedAt: number;
}

export interface ReceivedMessage extends PublishedMessage {
  /** Decoded logical payload. */
  readonly payload: ChannelPayload;
  /** Provider-native author/account id when available. */
  readonly authorId?: string;
}

export interface ProviderReadResult {
  /** New messages in provider order. */
  readonly messages: readonly ReceivedMessage[];
  /** Cursor to use for the next read. */
  readonly nextCursor?: ProviderCursor;
}

export interface TransportEndpoint {
  /** Stable endpoint id inside one discovery/control plane. */
  readonly id: string;
  /** Provider that owns or discovered this endpoint. */
  readonly providerId: string;
  /** Logical protocol exposed by the endpoint. */
  readonly protocol: TransportProtocol;
  /** Optional join URL, room URL, or dial URL when the protocol has one. */
  readonly url?: string;
  /** Optional host for socket-like endpoints. */
  readonly host?: string;
  /** Optional port for socket-like endpoints. */
  readonly port?: number;
  /** Metadata safe for scheduler, admin UI, and logs. */
  readonly metadata?: Readonly<Record<string, string>>;
}

export interface ByteDuplex {
  /**
   * Writes one binary chunk to the transport.
   *
   * @param chunk Bytes to send.
   */
  write(chunk: Uint8Array): Promise<void>;

  /**
   * Reads the next binary chunk or `null` after a clean close.
   *
   * @returns Received bytes or null.
   */
  read(): Promise<Uint8Array | null>;

  /**
   * Closes the transport and releases local resources.
   */
  close(): Promise<void>;
}

export interface StreamTransportChannel {
  /** Static identity for this stream transport instance. */
  readonly identity: ProviderIdentity;
  /** Static budget used by scheduler and admin UI. */
  readonly budget: ProviderBudget;

  /**
   * Reports current health without opening a stream.
   *
   * @returns Provider health snapshot suitable for failover decisions.
   */
  getHealth(): Promise<ProviderHealth>;

  /**
   * Opens a binary stream to an endpoint.
   *
   * @param endpoint Endpoint discovered through provider/control messages.
   * @returns Binary duplex stream.
   */
  connect(endpoint: TransportEndpoint): Promise<ByteDuplex>;
}

export interface ProviderChannel {
  /** Static identity for this provider instance. */
  readonly identity: ProviderIdentity;
  /** Static budget used by scheduler and admin UI. */
  readonly budget: ProviderBudget;

  /**
   * Reports current health without performing a send or read operation.
   *
   * @returns Provider health snapshot suitable for failover decisions.
   */
  getHealth(): Promise<ProviderHealth>;

  /**
   * Publishes one logical payload through the provider.
   *
   * @param payload Logical message payload before provider-specific encoding.
   * @returns Provider-native publish receipt.
   */
  publish(payload: ChannelPayload): Promise<PublishedMessage>;

  /**
   * Reads messages after an optional provider cursor.
   *
   * @param cursor Provider cursor from a previous read result.
   * @returns Decoded messages and the next cursor.
   */
  read(cursor?: ProviderCursor): Promise<ProviderReadResult>;
}

/**
 * Returns whether a provider should be considered for new outbound work.
 *
 * @param health Provider health snapshot.
 * @returns True when the provider can receive new work.
 */
export function isProviderUsable(health: ProviderHealth): boolean {
  return health.state === "healthy" || health.state === "degraded";
}
