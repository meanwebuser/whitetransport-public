/**
 * Message Store API — Vercel Serverless Function
 *
 * Endpoint: GET /api/webhook/store?provider=vk-wh&cursor=123
 *
 * Returns stored webhook messages for a given provider since the cursor.
 * Used by the YTP core to poll for incoming messages without long-polling
 * the social APIs directly.
 *
 * For production, replace the in-memory store with:
 *   - Vercel KV (Redis-compatible)
 *   - Upstash Redis
 *   - Cloudflare D1
 *   - PlanetScale / Turso
 */

import type { VercelRequest, VercelResponse } from '@vercel/node';

// Shared store reference (same as webhook handlers in single-instance)
// In production, this would be Redis/KV
declare global {
  var __ytpStore: Map<string, any[]>;
}

if (!globalThis.__ytpStore) {
  globalThis.__ytpStore = new Map();
}

function getStore(providerId: string): any[] {
  if (!globalThis.__ytpStore.has(providerId)) globalThis.__ytpStore.set(providerId, []);
  return globalThis.__ytpStore.get(providerId)!;
}

export default async function handler(req: VercelRequest, res: VercelResponse) {
  if (req.method !== 'GET') {
    return res.status(405).json({ error: 'Method not allowed' });
  }

  const providerId = req.query.provider as string || 'vk-wh';
  const cursor = req.query.cursor as string || null;

  const allMsgs = getStore(providerId);

  if (!cursor) {
    return res.status(200).json({
      messages: allMsgs.splice(0),  // Return and clear
      count: allMsgs.length,
    });
  }

  const idx = allMsgs.findIndex(m => Number(m.id) > Number(cursor));
  if (idx === -1) {
    return res.status(200).json({ messages: [], count: 0 });
  }

  const result = allMsgs.splice(idx);
  return res.status(200).json({
    messages: result,
    count: result.length,
  });
}
