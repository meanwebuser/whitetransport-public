/**
 * YTP Provider Selection — chooses the best provider for each outbound frame.
 *
 * Factors:
 *   - Provider health (latency, loss)
 *   - Budget remaining
 *   - Priority of the frame
 *   - Provider capabilities vs frame requirements
 */

import type { Provider, ProviderCapabilities } from '../providers/provider';
import type { OutboundFrame } from '../providers/provider';
import type { TransportBudget, BudgetUsage } from './budget';

export interface ProviderHealth {
  providerId: string;
  lastSuccessAt: number;
  lastFailureAt: number;
  consecutiveFailures: number;
  latencyMs: number;
  lossEstimate: number; // 0..1
  budgetUsed: number;   // 0..1 fraction
}

export class ProviderSelector {
  private health: Map<string, ProviderHealth> = new Map();

  /** Update health info for a provider */
  updateHealth(providerId: string, update: Partial<ProviderHealth>): void {
    const existing = this.health.get(providerId) ?? {
      providerId,
      lastSuccessAt: 0,
      lastFailureAt: 0,
      consecutiveFailures: 0,
      latencyMs: Infinity,
      lossEstimate: 0,
      budgetUsed: 0,
    };
    this.health.set(providerId, { ...existing, ...update });
  }

  recordSuccess(providerId: string, latencyMs: number): void {
    const h = this.health.get(providerId);
    if (h) {
      h.lastSuccessAt = Date.now();
      h.consecutiveFailures = 0;
      h.latencyMs = latencyMs;
    }
  }

  recordFailure(providerId: string): void {
    const h = this.health.get(providerId);
    if (h) {
      h.lastFailureAt = Date.now();
      h.consecutiveFailures++;
      h.lossEstimate = Math.min(1, h.lossEstimate + 0.1);
    }
  }

  /**
   * Select the best provider for a frame.
   * Returns null if no suitable provider is available.
   */
  select(
    providers: Provider[],
    frame: OutboundFrame,
    budgets: Map<string, { usage: BudgetUsage; budget: TransportBudget }>,
  ): Provider | null {
    const now = Date.now();
    const candidates = providers.filter(p => {
      const h = this.health.get(p.id);
      const b = budgets.get(p.id);

      // Skip providers that are completely down
      if (h && h.consecutiveFailures >= 5) return false;

      // Skip if frame exceeds provider max size
      if (frame.text.length > p.capabilities().maxTextBytes) return false;

      // Skip if budget exhausted
      if (b) {
        const { allowed } = canSendSimple(b.usage, b.budget);
        if (!allowed) return false;
      }

      return true;
    });

    if (candidates.length === 0) return null;

    // Score each candidate
    const scored = candidates.map(p => {
      const h = this.health.get(p.id);
      const cap = p.capabilities();
      const rate = p.rateHint();

      let score = 100;

      // Penalize high latency
      if (h) {
        score -= h.latencyMs / 100; // 1s = -10 points
        score -= h.lossEstimate * 50; // 50% loss = -25 points
        score -= h.consecutiveFailures * 10;
      }

      // Reward providers suitable for the frame's priority
      if (frame.priority <= 1 && rate.mode === 'conservative') {
        score -= 5; // conservative mode is slower for interactive
      }

      // Penalize long min interval for high-priority frames
      if (frame.priority <= 1) {
        score -= rate.minIntervalMs / 100;
      }

      return { provider: p, score };
    });

    scored.sort((a, b) => b.score - a.score);
    return scored[0].provider;
  }

  /**
   * For critical control messages, return all available providers
   * (quorum send).
   */
  selectQuorum(providers: Provider[]): Provider[] {
    return providers.filter(p => {
      const h = this.health.get(p.id);
      return !h || h.consecutiveFailures < 5;
    });
  }
}

function canSendSimple(usage: BudgetUsage, budget: TransportBudget): { allowed: boolean } {
  const now = Date.now();
  const currentHour = now - (now % 3600_000);
  if (currentHour !== usage.hourStart) {
    usage.messagesSentThisHour = 0;
    usage.hourStart = currentHour;
  }
  if (usage.messagesSentThisHour >= budget.maxMessagesPerHour) {
    return { allowed: false };
  }
  if (now - usage.lastSendAt < budget.minSendIntervalMs) {
    return { allowed: false };
  }
  return { allowed: true };
}
