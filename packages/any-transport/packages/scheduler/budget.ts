/**
 * YTP Transport Budget — user-configurable limits on provider usage.
 *
 * This is a critical safety feature: the protocol must respect
 * platform rate limits and user-imposed budgets to avoid
 * account bans or suspicious activity patterns.
 */

export interface TransportBudget {
  maxMessagesPerHour: number;
  maxBytesPerDay: number;
  minSendIntervalMs: number;
  maxPollsPerHour: number;
  quietHours?: {
    from: string; // "23:00"
    to: string;   // "07:00"
    timezone: string;
  };
}

export const DEFAULT_BUDGET: TransportBudget = {
  maxMessagesPerHour: 30,
  maxBytesPerDay: 500_000, // ~500 KB/day
  minSendIntervalMs: 2000,
  maxPollsPerHour: 120,
};

export interface BudgetUsage {
  messagesSentThisHour: number;
  bytesSentToday: number;
  lastSendAt: number; // epoch-ms
  pollsThisHour: number;
  hourStart: number;  // epoch-ms
  dayStart: number;   // epoch-ms
}

export function createBudgetUsage(): BudgetUsage {
  const now = Date.now();
  const hourStart = now - (now % 3600_000);
  const dayStart = now - (now % 86400_000);
  return {
    messagesSentThisHour: 0,
    bytesSentToday: 0,
    lastSendAt: 0,
    pollsThisHour: 0,
    hourStart,
    dayStart,
  };
}

export function canSend(usage: BudgetUsage, budget: TransportBudget): { allowed: boolean; reason?: string } {
  const now = Date.now();

  // Reset hour counter
  const currentHour = now - (now % 3600_000);
  if (currentHour !== usage.hourStart) {
    usage.messagesSentThisHour = 0;
    usage.pollsThisHour = 0;
    usage.hourStart = currentHour;
  }

  // Reset day counter
  const currentDay = now - (now % 86400_000);
  if (currentDay !== usage.dayStart) {
    usage.bytesSentToday = 0;
    usage.dayStart = currentDay;
  }

  if (usage.messagesSentThisHour >= budget.maxMessagesPerHour) {
    return { allowed: false, reason: 'Hourly message limit reached' };
  }

  if (usage.bytesSentToday >= budget.maxBytesPerDay) {
    return { allowed: false, reason: 'Daily byte limit reached' };
  }

  if (now - usage.lastSendAt < budget.minSendIntervalMs) {
    return { allowed: false, reason: 'Minimum send interval not met' };
  }

  if (budget.quietHours) {
    // Check if current time is within quiet hours
    // Simplified: skip timezone handling for MVP
  }

  return { allowed: true };
}

export function recordSend(usage: BudgetUsage, bytes: number): void {
  usage.messagesSentThisHour++;
  usage.bytesSentToday += bytes;
  usage.lastSendAt = Date.now();
}

export function recordPoll(usage: BudgetUsage): void {
  const now = Date.now();
  const currentHour = now - (now % 3600_000);
  if (currentHour !== usage.hourStart) {
    usage.pollsThisHour = 0;
    usage.hourStart = currentHour;
  }
  usage.pollsThisHour++;
}
