/**
 * Bridges the imported anYTransportProxy provider model to the shared
 * WhiteTransport provider-channel contracts.
 */

import type {
  ChannelPayload,
  ControlMessageKind,
  ProviderBudget,
  ProviderChannel,
  ProviderCursor as SharedProviderCursor,
  ProviderDirection,
  ProviderEncoding,
  ProviderHealth,
  ProviderIdentity,
  ProviderKind,
  ProviderReadResult,
  PublishedMessage,
  ReceivedMessage,
} from '@whitetransport/provider-channels';
import type { TransportBudget } from '../scheduler/budget';
import type { Provider, ProviderCursor as LegacyProviderCursor, ProviderMessage } from './provider';

export const LEGACY_PROVIDER_WIRE_PREFIX = 'WTPC1.';
const LEGACY_PROVIDER_WIRE_VERSION = 1;
const DEFAULT_CHANNEL_PRIORITY = 2;
const DEFAULT_DEADLINE_MS = 60_000;
const CONTROL_MESSAGE_KINDS: readonly ControlMessageKind[] = [
  'room_state',
  'client_feedback',
  'admin_command',
  'provider_probe',
  'transport_endpoint',
  'transport_payload',
];

export interface ProviderContractOptions {
  /** Stable provider instance id. Defaults to the legacy provider id. */
  readonly id?: string;
  /** Provider family used by shared scheduling and admin UI. */
  readonly kind: ProviderKind;
  /** Human readable provider label. Defaults to the provider id. */
  readonly label?: string;
  /** Direction supported by this provider instance. */
  readonly direction?: ProviderDirection;
  /** Payload encoding used by this provider instance. */
  readonly encoding?: ProviderEncoding;
}

export interface LegacyProviderChannelAdapterOptions extends ProviderContractOptions {
  /** Existing any-transport budget to expose through the shared contract. */
  readonly budget: TransportBudget;
  /** Maximum payload bytes after provider encoding. Defaults to provider capabilities. */
  readonly maxPayloadBytes?: number;
  /** Default priority passed to legacy append(). */
  readonly defaultPriority?: number;
  /** Default deadline for payloads that do not set expiresAt. */
  readonly defaultDeadlineMs?: number;
}

interface LegacyProviderWirePayload {
  readonly version: typeof LEGACY_PROVIDER_WIRE_VERSION;
  readonly kind: ControlMessageKind;
  readonly createdAt: number;
  readonly expiresAt?: number;
  readonly metadata?: Readonly<Record<string, string>>;
  readonly bodyBase64: string;
}

/**
 * Creates the shared provider identity for a legacy any-transport provider.
 *
 * @param provider Imported any-transport provider implementation.
 * @param options Shared contract metadata that is not present in legacy providers.
 * @returns Provider identity for admin, scheduler, and failover layers.
 */
export function describeProviderIdentity(provider: Provider, options: ProviderContractOptions): ProviderIdentity {
  return {
    id: options.id ?? provider.id,
    kind: options.kind,
    label: options.label ?? provider.id,
    direction: options.direction ?? 'duplex',
    encoding: options.encoding ?? (provider.capabilities().supportsAttachments ? 'document' : 'text'),
  };
}

/**
 * Converts any-transport budget settings into the shared provider budget shape.
 *
 * @param budget Existing any-transport budget settings.
 * @param maxPayloadBytes Maximum provider payload size after provider encoding.
 * @returns Shared provider budget used by scheduling and admin surfaces.
 */
export function describeProviderBudget(budget: TransportBudget, maxPayloadBytes: number): ProviderBudget {
  return {
    maxPayloadBytes,
    sendsPerMinute: Math.max(1, Math.floor(budget.maxMessagesPerHour / 60)),
    dailyByteBudget: budget.maxBytesPerDay,
  };
}

/**
 * Adapts imported any-transport text providers to the shared ProviderChannel API.
 */
export class LegacyProviderChannelAdapter implements ProviderChannel {
  readonly identity: ProviderIdentity;
  readonly budget: ProviderBudget;
  private health: ProviderHealth = { state: 'degraded', failureReason: 'provider has not completed an operation yet' };
  private readonly defaultDeadlineMs: number;
  private readonly defaultPriority: number;

  constructor(private readonly provider: Provider, options: LegacyProviderChannelAdapterOptions) {
    this.identity = describeProviderIdentity(provider, options);
    this.budget = describeProviderBudget(options.budget, options.maxPayloadBytes ?? provider.capabilities().maxTextBytes);
    this.defaultDeadlineMs = options.defaultDeadlineMs ?? DEFAULT_DEADLINE_MS;
    this.defaultPriority = options.defaultPriority ?? DEFAULT_CHANNEL_PRIORITY;
  }

  /**
   * Reports the most recent operation health for scheduler/admin surfaces.
   *
   * @returns Provider health snapshot.
   */
  async getHealth(): Promise<ProviderHealth> {
    return this.health;
  }

  /**
   * Publishes one shared channel payload through a legacy text provider.
   *
   * @param payload Shared provider-channel payload.
   * @returns Provider-native publish receipt.
   */
  async publish(payload: ChannelPayload): Promise<PublishedMessage> {
    try {
      const wireText = encodeChannelPayloadForLegacyProvider(payload);
      const result = await this.provider.append({
        text: wireText,
        priority: this.defaultPriority,
        deadline: payload.expiresAt ?? Date.now() + this.defaultDeadlineMs,
      });
      this.health = { state: 'healthy', lastOkAt: Date.now() };
      return {
        providerId: this.identity.id,
        remoteId: result.messageId,
        acceptedAt: result.timestamp,
      };
    } catch (error) {
      this.recordFailure(error);
      throw error;
    }
  }

  /**
   * Reads shared channel payloads after a provider cursor.
   *
   * @param cursor Shared provider cursor from a previous read.
   * @returns Decoded messages and next cursor.
   */
  async read(cursor?: SharedProviderCursor): Promise<ProviderReadResult> {
    try {
      const result = await this.provider.scan(toLegacyCursor(cursor));
      const messages = result.messages
        .filter((message) => message.text.startsWith(LEGACY_PROVIDER_WIRE_PREFIX))
        .map((message) => this.toReceivedMessage(message));
      this.health = { state: 'healthy', lastOkAt: Date.now() };
      return {
        messages,
        nextCursor: toSharedCursor(result.nextCursor),
      };
    } catch (error) {
      this.recordFailure(error);
      throw error;
    }
  }

  private toReceivedMessage(message: ProviderMessage): ReceivedMessage {
    return {
      providerId: this.identity.id,
      remoteId: message.id,
      acceptedAt: message.timestamp,
      payload: decodeChannelPayloadFromLegacyProvider(message.text),
      authorId: message.fromSelf ? this.identity.id : undefined,
    };
  }

  private recordFailure(error: unknown): void {
    const message = error instanceof Error ? error.message : String(error);
    this.health = { state: 'degraded', lastFailureAt: Date.now(), failureReason: message };
  }
}

/**
 * Encodes shared payload bytes into a text envelope legacy providers can carry.
 *
 * @param payload Shared provider-channel payload.
 * @returns Text frame safe for legacy append() providers.
 */
export function encodeChannelPayloadForLegacyProvider(payload: ChannelPayload): string {
  const wirePayload: LegacyProviderWirePayload = {
    version: LEGACY_PROVIDER_WIRE_VERSION,
    kind: payload.kind,
    createdAt: payload.createdAt,
    expiresAt: payload.expiresAt,
    metadata: payload.metadata,
    bodyBase64: Buffer.from(payload.body).toString('base64'),
  };
  return `${LEGACY_PROVIDER_WIRE_PREFIX}${Buffer.from(JSON.stringify(wirePayload), 'utf8').toString('base64')}`;
}

/**
 * Decodes a text envelope produced by encodeChannelPayloadForLegacyProvider().
 *
 * @param text Legacy provider message text.
 * @returns Shared provider-channel payload.
 * @throws If the text is not a valid WhiteTransport provider-channel envelope.
 */
export function decodeChannelPayloadFromLegacyProvider(text: string): ChannelPayload {
  if (!text.startsWith(LEGACY_PROVIDER_WIRE_PREFIX)) {
    throw new Error('Legacy provider message does not use the WTPC1 envelope prefix');
  }
  const encodedJson = text.slice(LEGACY_PROVIDER_WIRE_PREFIX.length);
  const parsed: unknown = JSON.parse(Buffer.from(encodedJson, 'base64').toString('utf8'));
  const wirePayload = assertLegacyProviderWirePayload(parsed);
  return {
    kind: wirePayload.kind,
    createdAt: wirePayload.createdAt,
    expiresAt: wirePayload.expiresAt,
    metadata: wirePayload.metadata,
    body: new Uint8Array(Buffer.from(wirePayload.bodyBase64, 'base64')),
  };
}

function assertLegacyProviderWirePayload(value: unknown): LegacyProviderWirePayload {
  if (!isRecord(value)) throw new Error('Legacy provider envelope must be a JSON object');
  if (value.version !== LEGACY_PROVIDER_WIRE_VERSION) {
    throw new Error(`Unsupported legacy provider envelope version: ${String(value.version)}`);
  }
  if (!isControlMessageKind(value.kind)) throw new Error(`Unsupported channel payload kind: ${String(value.kind)}`);
  if (typeof value.createdAt !== 'number' || !Number.isFinite(value.createdAt)) {
    throw new Error('Legacy provider envelope createdAt must be a finite number');
  }
  if (value.expiresAt !== undefined && (typeof value.expiresAt !== 'number' || !Number.isFinite(value.expiresAt))) {
    throw new Error('Legacy provider envelope expiresAt must be a finite number');
  }
  if (value.metadata !== undefined && !isStringRecord(value.metadata)) {
    throw new Error('Legacy provider envelope metadata must be a string record');
  }
  if (typeof value.bodyBase64 !== 'string' || value.bodyBase64.length === 0) {
    throw new Error('Legacy provider envelope bodyBase64 is required');
  }
  return value as unknown as LegacyProviderWirePayload;
}

function toLegacyCursor(cursor: SharedProviderCursor | undefined): LegacyProviderCursor {
  return cursor?.value ?? null;
}

function toSharedCursor(cursor: LegacyProviderCursor): SharedProviderCursor | undefined {
  if (cursor === null) return undefined;
  return { value: String(cursor) };
}

function isControlMessageKind(value: unknown): value is ControlMessageKind {
  return typeof value === 'string' && CONTROL_MESSAGE_KINDS.includes(value as ControlMessageKind);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isStringRecord(value: unknown): value is Record<string, string> {
  return isRecord(value) && Object.values(value).every((entry) => typeof entry === 'string');
}
