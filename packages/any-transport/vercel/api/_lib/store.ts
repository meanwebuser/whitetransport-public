/**
 * Shared message store for Vercel serverless functions.
 *
 * IMPORTANT: In-memory stores are NOT shared across serverless instances!
 * This is suitable for development and single-instance deployments only.
 *
 * For production on Vercel, use one of these backends:
 *
 * 1. Vercel KV (Redis-compatible, built into Vercel)
 *    import { kv } from '@vercel/kv';
 *    await kv.lpush(`ytp:${providerId}`, message);
 *    await kv.lrange(`ytp:${providerId}`, 0, -1);
 *
 * 2. Upstash Redis (serverless Redis)
 *    import { Redis } from '@upstash/redis';
 *    const redis = Redis.fromEnv();
 *
 * 3. Turso / PlanetScale (SQLite/MySQL)
 *    Better for persistent storage with complex queries
 */

declare global {
  var __ytpStore: Map<string, any[]>;
}

if (!globalThis.__ytpStore) {
  globalThis.__ytpStore = new Map();
}

export function getStore(providerId: string): any[] {
  if (!globalThis.__ytpStore.has(providerId)) globalThis.__ytpStore.set(providerId, []);
  return globalThis.__ytpStore.get(providerId)!;
}

export function pushMessage(providerId: string, msg: any): void {
  getStore(providerId).push(msg);
}

export function popSince(providerId: string, cursor: string | null): any[] {
  const store = getStore(providerId);
  if (!cursor) {
    return store.splice(0);
  }
  const idx = store.findIndex(m => Number(m.id) > Number(cursor));
  if (idx === -1) return [];
  return store.splice(idx);
}
