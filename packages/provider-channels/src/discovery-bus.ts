import type { ControlEnvelope } from './control-messages.js';
import type { ControlMessageKind, ProviderBudget, ProviderHealth, ProviderKind } from './index.js';

export type DiscoveryCarrierId = 'vk-text' | 'ok-text' | 'ok-graph-messages' | 'memory';

export type DiscoveryCarrierCapability =
  | 'room-announce'
  | 'room-claim'
  | 'room-release'
  | 'command-request'
  | 'command-result'
  | 'health-report'
  | 'client-log';

export type DiscoveryLaneRole = 'discovery' | 'control' | 'telemetry' | 'log';

export interface DiscoveryBusLane {
  readonly id: string;
  readonly carrierId: DiscoveryCarrierId | string;
  readonly role: DiscoveryLaneRole;
  readonly label: string;
  readonly peerId: string;
  readonly capabilities: readonly DiscoveryCarrierCapability[];
}

export interface CreateVkDiscoveryBusLaneOptions {
  readonly name: string;
  readonly peerId: string;
  readonly role: DiscoveryLaneRole;
  readonly capabilities?: readonly DiscoveryCarrierCapability[];
}

export type DiscoveryMessageKind =
  | 'room.announce'
  | 'room.claim'
  | 'room.release'
  | 'command.request'
  | 'command.result'
  | 'health.report'
  | 'client.log';

export interface DiscoveryCarrierIdentity {
  readonly id: DiscoveryCarrierId | string;
  readonly providerKind: ProviderKind;
  readonly label: string;
  readonly capabilities: readonly DiscoveryCarrierCapability[];
}

export interface DiscoveryCarrierMetadata {
  readonly identity: DiscoveryCarrierIdentity;
  readonly budget: ProviderBudget;
  readonly encrypted: true;
  readonly ordering: 'best-effort' | 'provider-order';
  readonly retention: 'ephemeral' | 'provider-retained';
}

export interface DiscoveryEnvelope<K extends DiscoveryMessageKind = DiscoveryMessageKind> {
  readonly version: 1;
  readonly id: string;
  readonly kind: K;
  readonly createdAt: number;
  readonly source: {
    readonly nodeId: string;
    readonly actorId: string;
    readonly role: 'client' | 'creator' | 'admin' | 'provider';
  };
  readonly control: ControlEnvelope<ControlMessageKind>;
}

export interface CreateDiscoveryEnvelopeOptions<K extends DiscoveryMessageKind> {
  readonly id: string;
  readonly kind: K;
  readonly source: DiscoveryEnvelope<K>['source'];
  readonly control: ControlEnvelope<ControlMessageKind>;
  readonly createdAt?: number;
}

export interface DiscoveryCarrierPublishResult {
  readonly carrierId: string;
  readonly remoteId: string;
  readonly acceptedAt: number;
}

export interface DiscoveryCarrierReadResult {
  readonly envelopes: readonly DiscoveryEnvelope[];
  readonly cursor?: string;
}

export interface DiscoveryCarrier {
  readonly metadata: DiscoveryCarrierMetadata;
  getHealth(): Promise<ProviderHealth>;
  publish(envelope: DiscoveryEnvelope): Promise<DiscoveryCarrierPublishResult>;
  read(cursor?: string): Promise<DiscoveryCarrierReadResult>;
}

export const DISCOVERY_MESSAGE_TO_CAPABILITY: Readonly<Record<DiscoveryMessageKind, DiscoveryCarrierCapability>> = {
  'room.announce': 'room-announce',
  'room.claim': 'room-claim',
  'room.release': 'room-release',
  'command.request': 'command-request',
  'command.result': 'command-result',
  'health.report': 'health-report',
  'client.log': 'client-log',
};

export const DISCOVERY_LANE_DEFAULT_CAPABILITIES: Readonly<Record<DiscoveryLaneRole, readonly DiscoveryCarrierCapability[]>> = {
  discovery: ['room-announce', 'room-claim', 'room-release'],
  control: ['command-request', 'command-result', 'health-report'],
  telemetry: ['health-report', 'client-log'],
  log: ['client-log'],
};

export const STANDARD_DISCOVERY_CARRIERS: readonly DiscoveryCarrierMetadata[] = [
  {
    identity: {
      id: 'vk-text',
      providerKind: 'vk',
      label: 'VK Text Discovery',
      capabilities: ['room-announce', 'room-claim', 'room-release', 'command-request', 'command-result', 'health-report', 'client-log'],
    },
    budget: { maxPayloadBytes: 4096, sendsPerMinute: 120 },
    encrypted: true,
    ordering: 'provider-order',
    retention: 'provider-retained',
  },
  {
    identity: {
      id: 'ok-graph-messages',
      providerKind: 'ok',
      label: 'OK Graph Messages Discovery',
      capabilities: ['room-announce', 'room-claim', 'room-release', 'command-request', 'command-result', 'health-report', 'client-log'],
    },
    budget: { maxPayloadBytes: 4096, sendsPerMinute: 90 },
    encrypted: true,
    ordering: 'provider-order',
    retention: 'provider-retained',
  },
];

/**
 * Creates a typed discovery-bus envelope around an encrypted control message.
 *
 * @param options Envelope fields supplied by the caller.
 * @returns Versioned discovery envelope.
 */
export function createDiscoveryEnvelope<K extends DiscoveryMessageKind>(
  options: CreateDiscoveryEnvelopeOptions<K>,
): DiscoveryEnvelope<K> {
  return {
    version: 1,
    id: options.id,
    kind: options.kind,
    createdAt: options.createdAt ?? Date.now(),
    source: options.source,
    control: options.control,
  };
}

/**
 * Checks whether a carrier can publish a given discovery message kind.
 *
 * @param carrier Carrier metadata from catalog/config.
 * @param kind Standard discovery message kind.
 * @returns True when the carrier advertises the required capability.
 */
export function discoveryCarrierSupports(carrier: DiscoveryCarrierMetadata, kind: DiscoveryMessageKind): boolean {
  return carrier.identity.capabilities.includes(DISCOVERY_MESSAGE_TO_CAPABILITY[kind]);
}

/**
 * Builds a stable VK discovery-bus lane from an existing chat peer id.
 *
 * @param options Chat name, peer id, role, and optional capability override.
 * @returns Standard lane metadata for config files and native clients.
 * @throws Error when peer id is missing or is not a VK chat/user peer id.
 */
export function createVkDiscoveryBusLane(options: CreateVkDiscoveryBusLaneOptions): DiscoveryBusLane {
  const peerId = options.peerId.trim();
  if (!/^-?\d+$/.test(peerId)) {
    throw new Error(`VK discovery lane ${options.name} requires numeric peerId`);
  }
  const capabilities = options.capabilities ?? DISCOVERY_LANE_DEFAULT_CAPABILITIES[options.role];
  if (capabilities.length === 0) {
    throw new Error(`VK discovery lane ${options.name} requires at least one capability`);
  }
  return {
    id: `vk-${options.role}-${options.name}`,
    carrierId: 'vk-text',
    role: options.role,
    label: options.name,
    peerId,
    capabilities,
  };
}
