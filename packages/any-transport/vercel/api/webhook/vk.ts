/**
 * VK Webhook Handler — Vercel Serverless Function
 *
 * Endpoint: POST /api/webhook/vk
 *
 * VK Callback API sends POST requests with event data.
 * This handler:
 *   1. Responds to confirmation requests with the confirmation token
 *   2. Verifies the secret key (if configured)
 *   3. Pushes incoming messages to the webhook store
 *
 * Setup in VK Community Settings:
 *   - Callback URL: https://your-vercel.app/api/webhook/vk
 *   - API Version: 5.131+
 *   - Enable: message_new, message_edit, message_allow
 */

import type { VercelRequest, VercelResponse } from '@vercel/node';

// In-memory store for single-instance deployments
// For production, replace with Redis / Vercel KV / Upstash
const messageStore: Map<string, any[]> = new Map();

function getStore(providerId: string): any[] {
  if (!messageStore.has(providerId)) messageStore.set(providerId, []);
  return messageStore.get(providerId)!;
}

export default async function handler(req: VercelRequest, res: VercelResponse) {
  if (req.method !== 'POST') {
    return res.status(405).json({ error: 'Method not allowed' });
  }

  const body = req.body;
  const CONFIRMATION_TOKEN = process.env.VK_CONFIRMATION_TOKEN || '';
  const SECRET_KEY = process.env.VK_CALLBACK_SECRET || '';

  // VK Confirmation request
  if (body.type === 'confirmation') {
    console.log('[VK Webhook] Confirmation request received');
    return res.status(200).send(CONFIRMATION_TOKEN);
  }

  // Verify secret
  if (SECRET_KEY && body.secret !== SECRET_KEY) {
    console.warn('[VK Webhook] Invalid secret key');
    return res.status(403).json({ error: 'Invalid secret' });
  }

  // Handle message_new
  if (body.type === 'message_new' && body.object?.message) {
    const msg = body.object.message;
    const providerId = 'vk-wh';

    getStore(providerId).push({
      id: String(msg.id || msg.conversation_message_id),
      timestamp: (msg.date || Math.floor(Date.now() / 1000)) * 1000,
      text: msg.text || '',
      fromSelf: msg.out === 1,
      providerId,
      attachments: msg.attachments || [],
    });

    console.log(`[VK Webhook] Message stored: id=${msg.id}, text=${(msg.text || '').substring(0, 50)}...`);
  }

  // Handle message_allow (user allowed messages)
  if (body.type === 'message_allow') {
    console.log(`[VK Webhook] User ${body.object.user_id} allowed messages`);
  }

  // VK requires "ok" response
  return res.status(200).send('ok');
}
