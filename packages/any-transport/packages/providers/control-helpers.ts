/**
 * Typed helpers for common WhiteTransport control-plane messages.
 */

import type {
  AdminCommandAction,
  ClientFeedbackCode,
  ClientFeedbackSeverity,
  ControlActor,
  ControlEnvelope,
  ProviderChannel,
  ProviderHealth,
  ProviderProbePhase,
  TransportEndpoint,
} from '@whitetransport/provider-channels';
import { createControlEnvelope } from '@whitetransport/provider-channels';
import type {
  ControlPublishResult,
  ReadControlEnvelopesResult,
} from './control-bus';
import {
  publishControlEnvelope,
  readControlEnvelopes,
} from './control-bus';

interface ControlEnvelopeBaseOptions {
  readonly id: string;
  readonly source: ControlActor;
  readonly createdAt?: number;
}

interface ControlPublishOptions {
  readonly channels: readonly ProviderChannel[];
  readonly minSuccesses?: number;
  readonly expiresAt?: number;
  readonly metadata?: Readonly<Record<string, string>>;
}

export interface CreateClientFeedbackEnvelopeOptions extends ControlEnvelopeBaseOptions {
  readonly clientId: string;
  readonly severity: ClientFeedbackSeverity;
  readonly code: ClientFeedbackCode;
  readonly message: string;
  readonly providerId?: string;
  readonly endpointId?: string;
  readonly observedAt?: number;
  readonly metadata?: Readonly<Record<string, string>>;
}

export interface CreateAdminCommandEnvelopeOptions extends ControlEnvelopeBaseOptions {
  readonly commandId: string;
  readonly action: AdminCommandAction;
  readonly targetId?: string;
  readonly arguments?: Readonly<Record<string, string>>;
}

export interface CreateProviderProbeEnvelopeOptions extends ControlEnvelopeBaseOptions {
  readonly probeId: string;
  readonly phase: ProviderProbePhase;
  readonly providerId: string;
  readonly endpoint?: TransportEndpoint;
  readonly health?: ProviderHealth;
}

export interface CreateTransportEndpointEnvelopeOptions extends ControlEnvelopeBaseOptions {
  readonly endpoint: TransportEndpoint;
  readonly sourceProviderId: string;
  readonly discoveredAt?: number;
  readonly ttlMs?: number;
  readonly priority?: number;
}

export type PublishClientFeedbackOptions = CreateClientFeedbackEnvelopeOptions & ControlPublishOptions;
export type PublishAdminCommandOptions = CreateAdminCommandEnvelopeOptions & ControlPublishOptions;
export type PublishProviderProbeOptions = CreateProviderProbeEnvelopeOptions & ControlPublishOptions;
export type PublishTransportEndpointOptions = CreateTransportEndpointEnvelopeOptions & ControlPublishOptions;

/**
 * Creates a typed client_feedback envelope.
 *
 * @param options Client feedback fields.
 * @returns Versioned client_feedback envelope.
 */
export function createClientFeedbackEnvelope(
  options: CreateClientFeedbackEnvelopeOptions,
): ControlEnvelope<"client_feedback"> {
  return createControlEnvelope({
    id: options.id,
    kind: 'client_feedback',
    createdAt: options.createdAt,
    source: options.source,
    body: {
      clientId: options.clientId,
      severity: options.severity,
      code: options.code,
      message: options.message,
      providerId: options.providerId,
      endpointId: options.endpointId,
      observedAt: options.observedAt ?? Date.now(),
      metadata: options.metadata,
    },
  });
}

/**
 * Creates a typed admin_command envelope.
 *
 * @param options Admin command fields.
 * @returns Versioned admin_command envelope.
 */
export function createAdminCommandEnvelope(
  options: CreateAdminCommandEnvelopeOptions,
): ControlEnvelope<"admin_command"> {
  return createControlEnvelope({
    id: options.id,
    kind: 'admin_command',
    createdAt: options.createdAt,
    source: options.source,
    body: {
      commandId: options.commandId,
      action: options.action,
      targetId: options.targetId,
      arguments: options.arguments,
    },
  });
}

/**
 * Creates a typed provider_probe envelope.
 *
 * @param options Provider probe fields.
 * @returns Versioned provider_probe envelope.
 */
export function createProviderProbeEnvelope(
  options: CreateProviderProbeEnvelopeOptions,
): ControlEnvelope<"provider_probe"> {
  return createControlEnvelope({
    id: options.id,
    kind: 'provider_probe',
    createdAt: options.createdAt,
    source: options.source,
    body: {
      probeId: options.probeId,
      phase: options.phase,
      providerId: options.providerId,
      endpoint: options.endpoint,
      health: options.health,
    },
  });
}

/**
 * Creates a typed transport_endpoint envelope.
 *
 * @param options Endpoint announcement fields.
 * @returns Versioned transport_endpoint envelope.
 */
export function createTransportEndpointEnvelope(
  options: CreateTransportEndpointEnvelopeOptions,
): ControlEnvelope<"transport_endpoint"> {
  return createControlEnvelope({
    id: options.id,
    kind: 'transport_endpoint',
    createdAt: options.createdAt,
    source: options.source,
    body: {
      endpoint: options.endpoint,
      sourceProviderId: options.sourceProviderId,
      discoveredAt: options.discoveredAt ?? Date.now(),
      ttlMs: options.ttlMs,
      priority: options.priority,
    },
  });
}

/**
 * Publishes client feedback across provider channels.
 *
 * @param options Feedback fields and provider channels.
 * @returns Per-provider publish result.
 */
export async function publishClientFeedback(
  options: PublishClientFeedbackOptions,
): Promise<ControlPublishResult<"client_feedback">> {
  return publishControlEnvelope({
    channels: options.channels,
    envelope: createClientFeedbackEnvelope(options),
    minSuccesses: options.minSuccesses,
    expiresAt: options.expiresAt,
    metadata: options.metadata,
  });
}

/**
 * Publishes an admin command across provider channels.
 *
 * @param options Command fields and provider channels.
 * @returns Per-provider publish result.
 */
export async function publishAdminCommand(
  options: PublishAdminCommandOptions,
): Promise<ControlPublishResult<"admin_command">> {
  return publishControlEnvelope({
    channels: options.channels,
    envelope: createAdminCommandEnvelope(options),
    minSuccesses: options.minSuccesses,
    expiresAt: options.expiresAt,
    metadata: options.metadata,
  });
}

/**
 * Publishes a provider probe request/result across provider channels.
 *
 * @param options Probe fields and provider channels.
 * @returns Per-provider publish result.
 */
export async function publishProviderProbe(
  options: PublishProviderProbeOptions,
): Promise<ControlPublishResult<"provider_probe">> {
  return publishControlEnvelope({
    channels: options.channels,
    envelope: createProviderProbeEnvelope(options),
    minSuccesses: options.minSuccesses,
    expiresAt: options.expiresAt,
    metadata: options.metadata,
  });
}

/**
 * Publishes a transport endpoint announcement across provider channels.
 *
 * @param options Endpoint fields and provider channels.
 * @returns Per-provider publish result.
 */
export async function publishTransportEndpoint(
  options: PublishTransportEndpointOptions,
): Promise<ControlPublishResult<"transport_endpoint">> {
  return publishControlEnvelope({
    channels: options.channels,
    envelope: createTransportEndpointEnvelope(options),
    minSuccesses: options.minSuccesses,
    expiresAt: options.expiresAt,
    metadata: options.metadata,
  });
}

export function readClientFeedback(
  channels: readonly ProviderChannel[],
): Promise<ReadControlEnvelopesResult<"client_feedback">> {
  return readControlEnvelopes({ channels, kind: 'client_feedback' });
}

export function readAdminCommands(
  channels: readonly ProviderChannel[],
): Promise<ReadControlEnvelopesResult<"admin_command">> {
  return readControlEnvelopes({ channels, kind: 'admin_command' });
}

export function readProviderProbes(
  channels: readonly ProviderChannel[],
): Promise<ReadControlEnvelopesResult<"provider_probe">> {
  return readControlEnvelopes({ channels, kind: 'provider_probe' });
}

export function readTransportEndpoints(
  channels: readonly ProviderChannel[],
): Promise<ReadControlEnvelopesResult<"transport_endpoint">> {
  return readControlEnvelopes({ channels, kind: 'transport_endpoint' });
}
