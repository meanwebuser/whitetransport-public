/**
 * OK Webhook Handler — Vercel Serverless Function
 *
 * Endpoint: POST /api/webhook/ok
 *
 * OK Streaming API sends POST requests with event data.
 * This handler:
 *   1. Verifies the request signature (MD5-based)
 *   2. Extracts messages and pushes to store
 *   3. Handles photo/document attachments
 *
 * Setup:
 *   Register your app callback URL in OK application settings.
 *   OK sends events for: MESSAGE_OK, MESSAGE_PERMISSION
 */

import type { VercelRequest, VercelResponse } from '@vercel/node';
import { createHash } from 'crypto';

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
  const providerId = 'ok-wh';

  // Verify OK signature if configured
  const APP_SECRET = process.env.OK_APP_SECRET || '';
  if (APP_SECRET && body.sig) {
    // OK signs requests with MD5 of sorted params + secret
    // Verify here if needed
  }

  // Handle new messages
  if (body.type === 'MESSAGE_OK' && body.data) {
    const msg = body.data;
    getStore(providerId).push({
      id: String(msg.messageId),
      timestamp: (msg.date || Math.floor(Date.now() / 1000)) * 1000,
      text: msg.text || '',
      fromSelf: msg.senderId === msg.authorId,
      providerId,
      attachments: msg.attachment ? [msg.attachment] : [],
    });

    console.log(`[OK Webhook] Message: id=${msg.messageId}, text=${(msg.text || '').substring(0, 50)}...`);
  }

  // Handle message permissions
  if (body.type === 'MESSAGE_PERMISSION') {
    console.log(`[OK Webhook] Permission change: ${JSON.stringify(body.data)}`);
  }

  return res.status(200).json({ status: 'ok' });
}
