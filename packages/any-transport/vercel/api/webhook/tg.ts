/**
 * Telegram Webhook Handler — Vercel Serverless Function
 *
 * Endpoint: POST /api/webhook/tg
 *
 * Telegram Bot API sends POST requests with Update objects.
 * This handler:
 *   1. Verifies the secret token header (if configured)
 *   2. Extracts messages and pushes to store
 *   3. Handles document/file attachments for document transport
 *
 * Setup:
 *   POST https://api.telegram.org/bot{TOKEN}/setWebhook
 *   Body: { "url": "https://your-vercel.app/api/webhook/tg", "secret_token": "your_secret" }
 */

import type { VercelRequest, VercelResponse } from '@vercel/node';

const messageStore: Map<string, any[]> = new Map();

function getStore(providerId: string): any[] {
  if (!messageStore.has(providerId)) messageStore.set(providerId, []);
  return messageStore.get(providerId)!;
}

export default async function handler(req: VercelRequest, VercelResponse: VercelResponse) {
  if (req.method !== 'POST') {
    return VercelResponse.status(405).json({ error: 'Method not allowed' });
  }

  // Verify secret token
  const SECRET_TOKEN = process.env.TG_WEBHOOK_SECRET || '';
  if (SECRET_TOKEN) {
    const headerToken = req.headers['x-telegram-bot-api-secret-token'];
    if (headerToken !== SECRET_TOKEN) {
      console.warn('[TG Webhook] Invalid secret token');
      return VercelResponse.status(403).json({ error: 'Invalid secret' });
    }
  }

  const update = req.body;
  const providerId = 'tg-wh';

  // Handle text messages
  if (update.message?.text) {
    getStore(providerId).push({
      id: String(update.message.message_id),
      timestamp: update.message.date * 1000,
      text: update.message.text,
      fromSelf: false,  // Webhook bot doesn't receive its own messages
      providerId,
    });

    console.log(`[TG Webhook] Message: id=${update.message.message_id}, text=${update.message.text.substring(0, 50)}...`);
  }

  // Handle document attachments (for document transport)
  if (update.message?.document) {
    const doc = update.message.document;
    const fileId = doc.file_id;
    const fileName = doc.file_name || 'unknown';

    // Download the file from Telegram
    try {
      const BOT_TOKEN = process.env.TG_TOKEN_1 || '';
      const fileResp = await fetch(`https://api.telegram.org/bot${BOT_TOKEN}/getFile?file_id=${fileId}`);
      const fileData = await fileResp.json() as any;

      if (fileData.ok && fileData.result?.file_path) {
        const downloadUrl = `https://api.telegram.org/file/bot${BOT_TOKEN}/${fileData.result.file_path}`;
        const downloadResp = await fetch(downloadUrl);
        const buffer = Buffer.from(await downloadResp.arrayBuffer());

        // Try to decode as PNG (YTP document transport)
        // If it's a YTP-encoded PNG, the text will be extracted
        getStore(providerId).push({
          id: `${update.message.message_id}:doc`,
          timestamp: update.message.date * 1000,
          text: `[DOC:${fileName}:${buffer.length}B]`,  // Placeholder — actual decoding done by provider
          fromSelf: false,
          providerId,
          rawBuffer: buffer.toString('base64'),
          fileName,
        });

        console.log(`[TG Webhook] Document: ${fileName} (${buffer.length}B)`);
      }
    } catch (err: any) {
      console.warn(`[TG Webhook] Document download failed: ${err.message}`);
    }
  }

  return VercelResponse.status(200).json({ ok: true });
}
