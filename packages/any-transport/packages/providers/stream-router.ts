/**
 * Unified stream transport dialer.
 *
 * Stream transports such as whitelist-bypass/WBStream and future YTP stream
 * adapters are selected through the same health and endpoint contract.
 */

import type {
  ByteDuplex,
  ProviderHealth,
  StreamTransportChannel,
  TransportEndpoint,
  TransportProtocol,
} from '@whitetransport/provider-channels';
import { isProviderUsable } from '@whitetransport/provider-channels';

export interface StreamRoute {
  readonly channel: StreamTransportChannel;
  readonly endpoint: TransportEndpoint;
  /** Lower values are preferred before health/latency tie-breaks. */
  readonly priority?: number;
}

export interface StreamDialFailure {
  readonly providerId: string;
  readonly endpointId: string;
  readonly reason: string;
}

export interface StreamDialResult {
  readonly stream: ByteDuplex;
  readonly route: StreamRoute;
  readonly attempted: readonly string[];
  readonly failures: readonly StreamDialFailure[];
}

export interface StreamDialOptions {
  readonly protocol?: TransportProtocol;
  readonly providerId?: string;
}

interface ScoredStreamRoute {
  readonly route: StreamRoute;
  readonly health: ProviderHealth;
}

/**
 * Connects to the best available stream route with deterministic fallback.
 */
export class StreamTransportDialer {
  private readonly routes: readonly StreamRoute[];

  constructor(routes: readonly StreamRoute[]) {
    this.routes = [...routes];
  }

  /**
   * Opens a duplex stream through the best usable route.
   *
   * @param options Optional provider/protocol filter.
   * @returns Connected stream plus selected route and failure diagnostics.
   * @throws Error when no route can be connected.
   */
  async connect(options: StreamDialOptions = {}): Promise<StreamDialResult> {
    const failures: StreamDialFailure[] = [];
    const attempted: string[] = [];
    const routes = await this.selectRoutes(options, failures);

    for (const route of routes) {
      const providerId = route.channel.identity.id;
      attempted.push(providerId);
      try {
        const stream = await route.channel.connect(route.endpoint);
        return { stream, route, attempted, failures };
      } catch (error) {
        failures.push({
          providerId,
          endpointId: route.endpoint.id,
          reason: error instanceof Error ? error.message : String(error),
        });
      }
    }

    throw new StreamDialError('No stream transport route connected', failures, attempted);
  }

  private async selectRoutes(options: StreamDialOptions, failures: StreamDialFailure[]): Promise<StreamRoute[]> {
    const scored: ScoredStreamRoute[] = [];

    for (const route of this.routes) {
      if (options.protocol && route.endpoint.protocol !== options.protocol) continue;
      if (options.providerId && route.channel.identity.id !== options.providerId) continue;

      try {
        const health = await route.channel.getHealth();
        if (isProviderUsable(health)) {
          scored.push({ route, health });
        } else {
          failures.push({
            providerId: route.channel.identity.id,
            endpointId: route.endpoint.id,
            reason: health.failureReason ?? `provider health is ${health.state}`,
          });
        }
      } catch (error) {
        failures.push({
          providerId: route.channel.identity.id,
          endpointId: route.endpoint.id,
          reason: error instanceof Error ? error.message : String(error),
        });
      }
    }

    scored.sort((left, right) => compareScoredRoutes(left, right));
    return scored.map((item) => item.route);
  }
}

export class StreamDialError extends Error {
  readonly failures: readonly StreamDialFailure[];
  readonly attempted: readonly string[];

  constructor(message: string, failures: readonly StreamDialFailure[], attempted: readonly string[]) {
    super(message);
    this.name = 'StreamDialError';
    this.failures = failures;
    this.attempted = attempted;
  }
}

function compareScoredRoutes(left: ScoredStreamRoute, right: ScoredStreamRoute): number {
  const priorityDiff = (left.route.priority ?? 100) - (right.route.priority ?? 100);
  if (priorityDiff !== 0) return priorityDiff;

  const stateDiff = healthStateScore(right.health) - healthStateScore(left.health);
  if (stateDiff !== 0) return stateDiff;

  return (left.health.latencyMs ?? Number.POSITIVE_INFINITY) - (right.health.latencyMs ?? Number.POSITIVE_INFINITY);
}

function healthStateScore(health: ProviderHealth): number {
  if (health.state === 'healthy') return 2;
  if (health.state === 'degraded') return 1;
  return 0;
}
