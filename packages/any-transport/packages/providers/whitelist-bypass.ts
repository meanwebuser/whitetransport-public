/**
 * whitelist-bypass/WBStream stream transport boundary.
 *
 * This module makes whitelist-bypass a first-class transport kind for
 * any-transport without moving custom code back into the upstream checkout.
 */

import type {
  ByteDuplex,
  ProviderBudget,
  ProviderHealth,
  ProviderIdentity,
  StreamTransportChannel,
  TransportEndpoint,
} from '@whitetransport/provider-channels';

export const WbTunnelMessageType = Object.freeze({
  connect: 0x01,
  connectOk: 0x02,
  connectErr: 0x03,
  data: 0x04,
  close: 0x05,
  udp: 0x06,
  udpReply: 0x07,
  config: 0x08,
  configAck: 0x09,
});

export type WbTunnelMessageTypeValue = typeof WbTunnelMessageType[keyof typeof WbTunnelMessageType];

export type WhitelistBypassTunnelMode = "auto" | "data-channel" | "video";

export interface WhitelistBypassEndpoint extends TransportEndpoint {
  readonly protocol: "wb-tunnel";
  /** WBStream or whitelist-bypass room URL. */
  readonly url: string;
  readonly metadata: Readonly<{
    tunnelMode: WhitelistBypassTunnelMode;
    role: "creator" | "joiner";
    carrier: "wbstream";
  } & Record<string, string>>;
}

export interface WhitelistBypassTransportConfig {
  readonly id?: string;
  readonly label?: string;
  readonly roomUrl?: string;
  readonly tunnelMode?: WhitelistBypassTunnelMode;
  readonly maxPayloadBytes?: number;
  readonly sendsPerMinute?: number;
  readonly connector?: WhitelistBypassConnector;
}

export interface WhitelistBypassConnectorContext {
  readonly identity: ProviderIdentity;
  readonly budget: ProviderBudget;
}

export type WhitelistBypassConnector = (
  endpoint: WhitelistBypassEndpoint,
  context: WhitelistBypassConnectorContext,
) => Promise<ByteDuplex>;

export interface WhitelistBypassTransportSnapshot {
  readonly identity: ProviderIdentity;
  readonly budget: ProviderBudget;
  readonly endpoint?: WhitelistBypassEndpoint;
  readonly health: ProviderHealth;
}

export class WhitelistBypassTransport implements StreamTransportChannel {
  readonly identity: ProviderIdentity;
  readonly budget: ProviderBudget;
  private endpoint?: WhitelistBypassEndpoint;
  private connector?: WhitelistBypassConnector;
  private health: ProviderHealth = { state: "offline", failureReason: "WBStream adapter is not connected" };

  constructor(config: WhitelistBypassTransportConfig = {}) {
    const id = config.id ?? "whitelist-bypass-wbstream";
    this.identity = {
      id,
      kind: "whitelist-bypass",
      label: config.label ?? "whitelist-bypass WBStream",
      direction: "duplex",
      encoding: "stream",
    };
    this.budget = {
      maxPayloadBytes: config.maxPayloadBytes ?? 64 * 1024,
      sendsPerMinute: config.sendsPerMinute ?? 120,
    };
    this.connector = config.connector;
    if (config.roomUrl) {
      this.endpoint = createWhitelistBypassEndpoint({
        providerId: id,
        roomUrl: config.roomUrl,
        tunnelMode: config.tunnelMode,
      });
    }
    this.health = this.createInitialHealth();
  }

  /**
   * Reports descriptor health for scheduler/admin surfaces.
   *
   * @returns Current health snapshot.
   */
  async getHealth(): Promise<ProviderHealth> {
    return this.health;
  }

  /**
   * Replaces the runtime connector used to open WB tunnel streams.
   *
   * @param connector Runtime connector provided by browser/server WBStream code.
   */
  setConnector(connector: WhitelistBypassConnector): void {
    this.connector = connector;
    this.health = this.endpoint
      ? { state: "healthy", lastOkAt: this.health.lastOkAt }
      : { state: "degraded", failureReason: "No default WBStream endpoint configured" };
  }

  /**
   * Opens a WB tunnel stream through the injected runtime adapter.
   *
   * @param endpoint WB tunnel endpoint to connect.
   * @returns Binary duplex stream.
   * @throws When the endpoint is not a whitelist-bypass endpoint or no adapter is wired.
   */
  async connect(endpoint: TransportEndpoint = this.requiredDefaultEndpoint()): Promise<ByteDuplex> {
    const wbEndpoint = assertWhitelistBypassEndpoint(endpoint);
    if (!this.connector) {
      const message = "whitelist-bypass WBStream adapter is not wired into any-transport yet";
      this.health = { state: "offline", lastFailureAt: Date.now(), failureReason: message };
      throw new Error(message);
    }

    try {
      const stream = await this.connector(wbEndpoint, { identity: this.identity, budget: this.budget });
      this.health = { state: "healthy", lastOkAt: Date.now() };
      return stream;
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      this.health = { state: "degraded", lastFailureAt: Date.now(), failureReason: message };
      throw error;
    }
  }

  /**
   * Returns a serializable transport snapshot for admin/control messages.
   *
   * @returns Snapshot containing identity, budget, endpoint, and health.
   */
  snapshot(): WhitelistBypassTransportSnapshot {
    return {
      identity: this.identity,
      budget: this.budget,
      endpoint: this.endpoint,
      health: this.health,
    };
  }

  private createInitialHealth(): ProviderHealth {
    if (!this.connector) return { state: "offline", failureReason: "WBStream adapter is not connected" };
    if (!this.endpoint) return { state: "degraded", failureReason: "No default WBStream endpoint configured" };
    return { state: "healthy" };
  }

  private requiredDefaultEndpoint(): WhitelistBypassEndpoint {
    if (!this.endpoint) {
      throw new Error("No default whitelist-bypass WBStream endpoint configured");
    }
    return this.endpoint;
  }
}

export interface CreateWhitelistBypassEndpointOptions {
  readonly providerId: string;
  readonly roomUrl: string;
  readonly tunnelMode?: WhitelistBypassTunnelMode;
  readonly role?: "creator" | "joiner";
  readonly id?: string;
}

/**
 * Creates the endpoint shape shared by discovery, admin, and clients.
 *
 * @param options WBStream room and endpoint metadata.
 * @returns Shared transport endpoint for whitelist-bypass/WBStream.
 */
export function createWhitelistBypassEndpoint(options: CreateWhitelistBypassEndpointOptions): WhitelistBypassEndpoint {
  return {
    id: options.id ?? `${options.providerId}:wbstream-room`,
    providerId: options.providerId,
    protocol: "wb-tunnel",
    url: options.roomUrl,
    metadata: {
      tunnelMode: options.tunnelMode ?? "auto",
      role: options.role ?? "joiner",
      carrier: "wbstream",
    },
  };
}

/**
 * Validates that a shared transport endpoint can be used by whitelist-bypass.
 *
 * @param endpoint Shared transport endpoint.
 * @returns Endpoint narrowed to whitelist-bypass/WBStream.
 * @throws If the endpoint is not a WB tunnel endpoint with a room URL.
 */
export function assertWhitelistBypassEndpoint(endpoint: TransportEndpoint): WhitelistBypassEndpoint {
  if (endpoint.protocol !== "wb-tunnel") {
    throw new Error(`Expected wb-tunnel endpoint, got ${endpoint.protocol}`);
  }
  if (!endpoint.url) {
    throw new Error("whitelist-bypass WBStream endpoint requires a room URL");
  }
  return {
    ...endpoint,
    protocol: "wb-tunnel",
    url: endpoint.url,
    metadata: {
      tunnelMode: readTunnelMode(endpoint.metadata?.tunnelMode),
      role: readRole(endpoint.metadata?.role),
      carrier: "wbstream",
      ...endpoint.metadata,
    },
  };
}

function readTunnelMode(value: string | undefined): WhitelistBypassTunnelMode {
  if (value === "data-channel" || value === "video" || value === "auto") return value;
  return "auto";
}

function readRole(value: string | undefined): "creator" | "joiner" {
  return value === "creator" ? "creator" : "joiner";
}
