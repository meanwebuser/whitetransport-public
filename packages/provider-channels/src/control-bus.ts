import type {
  ControlEnvelope,
  ControlMessageKind,
  ProviderChannel,
  ProviderCursor,
  PublishedMessage,
  ReceivedMessage,
} from './index.js';
import {
  decodeControlPayload,
  encodeControlPayload,
} from './control-messages.js';

export interface ProviderOperationFailure {
  readonly providerId: string;
  readonly error: Error;
}

export interface PublishControlEnvelopeOptions<K extends ControlMessageKind> {
  readonly channels: readonly ProviderChannel[];
  readonly envelope: ControlEnvelope<K>;
  /** Minimum provider publishes required before this operation is considered successful. */
  readonly minSuccesses?: number;
  readonly expiresAt?: number;
  readonly metadata?: Readonly<Record<string, string>>;
}

export interface ControlPublishResult<K extends ControlMessageKind> {
  readonly envelope: ControlEnvelope<K>;
  readonly published: readonly PublishedMessage[];
  readonly failures: readonly ProviderOperationFailure[];
}

export interface ReadControlEnvelopesOptions<K extends ControlMessageKind = ControlMessageKind> {
  readonly channels: readonly ProviderChannel[];
  readonly kind?: K;
  readonly cursors?: Readonly<Record<string, ProviderCursor>>;
}

export interface ControlEnvelopeAnnouncement<K extends ControlMessageKind = ControlMessageKind> {
  readonly providerId: string;
  readonly remoteId: string;
  readonly acceptedAt: number;
  readonly envelope: ControlEnvelope<K>;
}

export interface ReadControlEnvelopesResult<K extends ControlMessageKind = ControlMessageKind> {
  readonly announcements: readonly ControlEnvelopeAnnouncement<K>[];
  readonly cursors: Readonly<Record<string, ProviderCursor>>;
  readonly failures: readonly ProviderOperationFailure[];
}

export class ProviderControlPublishError<K extends ControlMessageKind> extends Error {
  readonly result: ControlPublishResult<K>;

  constructor(requiredSuccesses: number, result: ControlPublishResult<K>) {
    super(`Control publish reached ${result.published.length}/${requiredSuccesses} required provider successes`);
    this.name = 'ProviderControlPublishError';
    this.result = result;
  }
}

/**
 * Publishes one control envelope to multiple provider channels.
 *
 * @param options Control envelope, channels, and minimum success policy.
 * @returns Per-provider publish receipts and failures.
 * @throws ProviderControlPublishError when successes are below minSuccesses.
 */
export async function publishControlEnvelope<K extends ControlMessageKind>(
  options: PublishControlEnvelopeOptions<K>,
): Promise<ControlPublishResult<K>> {
  if (options.channels.length === 0) throw new Error('publishControlEnvelope requires at least one provider channel');
  const payload = encodeControlPayload(options.envelope, {
    expiresAt: options.expiresAt,
    metadata: options.metadata,
  });
  const published: PublishedMessage[] = [];
  const failures: ProviderOperationFailure[] = [];

  await Promise.all(options.channels.map(async (channel) => {
    try {
      published.push(await channel.publish(payload));
    } catch (error) {
      failures.push({ providerId: channel.identity.id, error: normalizeError(error) });
    }
  }));

  const result: ControlPublishResult<K> = { envelope: options.envelope, published, failures };
  const minSuccesses = options.minSuccesses ?? 1;
  if (published.length < minSuccesses) throw new ProviderControlPublishError(minSuccesses, result);
  return result;
}

/**
 * Reads and decodes control envelopes from multiple provider channels.
 *
 * @param options Provider channels, optional kind filter, and cursors.
 * @returns Decoded announcements, next cursors, and provider failures.
 */
export async function readControlEnvelopes<K extends ControlMessageKind = ControlMessageKind>(
  options: ReadControlEnvelopesOptions<K>,
): Promise<ReadControlEnvelopesResult<K>> {
  if (options.channels.length === 0) throw new Error('readControlEnvelopes requires at least one provider channel');
  const announcements: ControlEnvelopeAnnouncement<K>[] = [];
  const cursors: Record<string, ProviderCursor> = {};
  const failures: ProviderOperationFailure[] = [];

  await Promise.all(options.channels.map(async (channel) => {
    try {
      const result = await channel.read(options.cursors?.[channel.identity.id]);
      if (result.nextCursor) cursors[channel.identity.id] = result.nextCursor;
      for (const message of result.messages) {
        const announcement = toControlEnvelopeAnnouncement(message, options.kind);
        if (announcement) announcements.push(announcement);
      }
    } catch (error) {
      failures.push({ providerId: channel.identity.id, error: normalizeError(error) });
    }
  }));

  return { announcements, cursors, failures };
}

function toControlEnvelopeAnnouncement<K extends ControlMessageKind>(
  message: ReceivedMessage,
  kind: K | undefined,
): ControlEnvelopeAnnouncement<K> | null {
  if (kind && message.payload.kind !== kind) return null;
  const envelope = decodeControlPayload({ ...message.payload, kind: message.payload.kind });
  return {
    providerId: message.providerId,
    remoteId: message.remoteId,
    acceptedAt: message.acceptedAt,
    envelope: envelope as ControlEnvelope<K>,
  };
}

function normalizeError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error));
}
