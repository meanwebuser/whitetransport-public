/**
 * Webhook Registration API — Vercel Serverless Function
 *
 * Endpoint: POST /api/webhook/register
 *
 * Registers webhook URLs with the social platforms.
 * Call this once after deploying to Vercel to activate webhooks.
 *
 * Body: { "base_url": "https://your-vercel.app" }
 */

import type { VercelRequest, VercelResponse } from '@vercel/node';

export default async function handler(req: VercelRequest, res: VercelResponse) {
  if (req.method !== 'POST') {
    return res.status(405).json({ error: 'Method not allowed' });
  }

  const { base_url } = req.body;
  if (!base_url) {
    return res.status(400).json({ error: 'base_url is required' });
  }

  const results: Record<string, any> = {};

  // Register Telegram webhook
  const TG_TOKEN = process.env.TG_TOKEN_1;
  if (TG_TOKEN) {
    try {
      const tgResp = await fetch(`https://api.telegram.org/bot${TG_TOKEN}/setWebhook`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          url: `${base_url}/api/webhook/tg`,
          allowed_updates: ['message'],
          secret_token: process.env.TG_WEBHOOK_SECRET || undefined,
        }),
      });
      results.telegram = await tgResp.json();
      console.log(`[Register] TG webhook: ${results.telegram.ok ? 'OK' : results.telegram.description}`);
    } catch (err: any) {
      results.telegram = { error: err.message };
    }
  }

  // VK Callback API requires manual setup in group settings
  // The confirmation token must be set in VK group → API Usage → Callback
  results.vk = {
    note: 'Set Callback URL in VK Community Settings → API Usage → Callback',
    url: `${base_url}/api/webhook/vk`,
    confirmationToken: process.env.VK_CONFIRMATION_TOKEN || 'NOT_SET',
  };

  // OK requires manual setup in application settings
  results.ok = {
    note: 'Set callback URL in OK Application Settings',
    url: `${base_url}/api/webhook/ok`,
  };

  return res.status(200).json({ results });
}
