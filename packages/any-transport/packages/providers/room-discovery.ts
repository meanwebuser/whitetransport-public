/**
 * Shared room-discovery bus over ProviderChannel instances.
 *
 * The bus publishes the same room_state envelope through multiple providers so
 * clients can discover a usable room even when one communication provider is
 * delayed or offline.
 */

import type {
  ControlActor,
  ControlEnvelope,
  ProviderChannel,
  ProviderCursor,
  RoomLifecycleState,
  RoomProviderSnapshot,
  TransportEndpoint,
} from '@whitetransport/provider-channels';
import { createControlEnvelope } from '@whitetransport/provider-channels';
import {
  ProviderControlPublishError,
  publishControlEnvelope,
  readControlEnvelopes,
} from './control-bus';
import type {
  ControlPublishResult,
  ProviderOperationFailure,
} from './control-bus';

export interface CreateRoomStateEnvelopeOptions {
  readonly id: string;
  readonly source: ControlActor;
  readonly roomId: string;
  readonly revision: number;
  readonly state: RoomLifecycleState;
  readonly endpoints: readonly TransportEndpoint[];
  readonly providers?: readonly RoomProviderSnapshot[];
  readonly createdAt?: number;
}

export interface PublishRoomStateOptions extends CreateRoomStateEnvelopeOptions {
  readonly channels: readonly ProviderChannel[];
  /** Minimum provider publishes required before this operation is considered successful. */
  readonly minSuccesses?: number;
  readonly expiresAt?: number;
  readonly metadata?: Readonly<Record<string, string>>;
}

export interface RoomStatePublishResult {
  readonly envelope: ControlEnvelope<"room_state">;
  readonly published: ControlPublishResult<"room_state">["published"];
  readonly failures: readonly ProviderOperationFailure[];
}

export interface ReadRoomStatesOptions {
  readonly channels: readonly ProviderChannel[];
  readonly cursors?: Readonly<Record<string, ProviderCursor>>;
}

export interface RoomStateAnnouncement {
  readonly providerId: string;
  readonly remoteId: string;
  readonly acceptedAt: number;
  readonly envelope: ControlEnvelope<"room_state">;
}

export interface ReadRoomStatesResult {
  readonly announcements: readonly RoomStateAnnouncement[];
  readonly cursors: Readonly<Record<string, ProviderCursor>>;
  readonly failures: readonly ProviderOperationFailure[];
}

export class RoomStatePublishError extends Error {
  readonly result: RoomStatePublishResult;

  constructor(requiredSuccesses: number, result: RoomStatePublishResult) {
    super(`Room state publish reached ${result.published.length}/${requiredSuccesses} required provider successes`);
    this.name = 'RoomStatePublishError';
    this.result = result;
  }
}

/**
 * Creates a typed room_state control envelope.
 *
 * @param options Room state fields and source actor.
 * @returns Versioned room_state envelope.
 */
export function createRoomStateEnvelope(options: CreateRoomStateEnvelopeOptions): ControlEnvelope<"room_state"> {
  return createControlEnvelope({
    id: options.id,
    kind: 'room_state',
    createdAt: options.createdAt,
    source: options.source,
    body: {
      roomId: options.roomId,
      revision: options.revision,
      state: options.state,
      endpoints: options.endpoints,
      providers: options.providers ?? [],
    },
  });
}

/**
 * Publishes room state to multiple provider channels.
 *
 * @param options Room state, provider channels, and minimum success policy.
 * @returns Per-provider publish receipts and failures.
 * @throws RoomStatePublishError when successes are below minSuccesses.
 */
export async function publishRoomState(options: PublishRoomStateOptions): Promise<RoomStatePublishResult> {
  const envelope = createRoomStateEnvelope(options);
  try {
    return await publishControlEnvelope({
      channels: options.channels,
      envelope,
      minSuccesses: options.minSuccesses,
      expiresAt: options.expiresAt,
      metadata: options.metadata,
    });
  } catch (error) {
    if (error instanceof ProviderControlPublishError) {
      throw new RoomStatePublishError(options.minSuccesses ?? 1, error.result);
    }
    throw error;
  }
}

/**
 * Reads room_state announcements from multiple provider channels.
 *
 * @param options Provider channels and optional cursors by provider id.
 * @returns Decoded room announcements, next cursors, and provider failures.
 */
export async function readRoomStates(options: ReadRoomStatesOptions): Promise<ReadRoomStatesResult> {
  return readControlEnvelopes({
    channels: options.channels,
    cursors: options.cursors,
    kind: 'room_state',
  });
}
