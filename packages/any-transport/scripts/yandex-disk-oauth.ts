/**
 * YTP Yandex Disk OAuth Token Acquisition
 *
 * This script helps obtain OAuth tokens for Yandex Disk API.
 * It generates the authorization URL and exchanges the code for a token.
 *
 * Two OAuth profiles are available:
 *   App 1: Full Disk access (read + write anywhere)
 *   App 2: Custom redirect URI
 *
 * Usage:
 *   npx ts-node scripts/yandex-disk-oauth.ts
 *   npx ts-node scripts/yandex-disk-oauth.ts --app 2
 *   npx ts-node scripts/yandex-disk-oauth.ts --code AAAAAAAAAAAAAAAA
 */

import * as https from 'https';
import * as http from 'http';
import { URL } from 'url';

// ── App Credentials ──────────────────────────────────────────────────────

const APPS = {
  1: {
    clientId: process.env.YDISK_CLIENT_ID || '',
    clientSecret: process.env.YDISK_CLIENT_SECRET || '',
    redirectUri: process.env.YDISK_REDIRECT_URI || 'https://oauth.yandex.ru/verification_code',
    scope: 'cloud_api:disk.read cloud_api:disk.write cloud_api:disk.app_folder',
    description: 'Full Disk access (read + write anywhere)',
  },
  2: {
    clientId: process.env.YDISK2_CLIENT_ID || '',
    clientSecret: process.env.YDISK2_CLIENT_SECRET || '',
    redirectUri: process.env.YDISK2_REDIRECT_URI || 'https://oauth-callback.example.invalid/yandex',
    scope: 'cloud_api:disk.read cloud_api:disk.write',
    description: 'Custom redirect URI',
  },
};

// ── Parse Args ───────────────────────────────────────────────────────────

const args = process.argv.slice(2);
const appNum = parseInt((args.find(a => a.startsWith('--app'))?.split('=')[1]) || args[args.indexOf('--app') + 1] || '1', 10);
const codeArg = args.find(a => a.startsWith('--code'))?.split('=')[1] || args[args.indexOf('--code') + 1];
const tokenArg = args.find(a => a.startsWith('--refresh'))?.split('=')[1] || args[args.indexOf('--refresh') + 1];

const app = APPS[appNum as keyof typeof APPS];
if (!app) {
  console.error(`Unknown app number: ${appNum}. Use 1 or 2.`);
  process.exit(1);
}

// ── Generate Auth URL ────────────────────────────────────────────────────

function generateAuthUrl(): string {
  const params = new URLSearchParams({
    response_type: 'code',
    client_id: app.clientId,
    redirect_uri: app.redirectUri,
    scope: app.scope,
    force_confirm: 'true',
  });

  return `https://oauth.yandex.ru/authorize?${params.toString()}`;
}

// ── Exchange Code for Token ──────────────────────────────────────────────

async function exchangeCodeForToken(code: string): Promise<any> {
  return new Promise((resolve, reject) => {
    const postData = new URLSearchParams({
      grant_type: 'authorization_code',
      code,
      client_id: app.clientId,
      client_secret: app.clientSecret,
      redirect_uri: app.redirectUri,
    }).toString();

    const options = {
      hostname: 'oauth.yandex.ru',
      path: '/token',
      method: 'POST',
      headers: {
        'Content-Type': 'application/x-www-form-urlencoded',
        'Content-Length': Buffer.byteLength(postData),
      },
    };

    const req = https.request(options, (res) => {
      let data = '';
      res.on('data', (chunk) => { data += chunk; });
      res.on('end', () => {
        try {
          const result = JSON.parse(data);
          resolve(result);
        } catch (err) {
          reject(new Error(`Parse error: ${data}`));
        }
      });
    });

    req.on('error', reject);
    req.write(postData);
    req.end();
  });
}

// ── Refresh Token ────────────────────────────────────────────────────────

async function refreshToken(refreshToken: string): Promise<any> {
  return new Promise((resolve, reject) => {
    const postData = new URLSearchParams({
      grant_type: 'refresh_token',
      refresh_token: refreshToken,
      client_id: app.clientId,
      client_secret: app.clientSecret,
    }).toString();

    const options = {
      hostname: 'oauth.yandex.ru',
      path: '/token',
      method: 'POST',
      headers: {
        'Content-Type': 'application/x-www-form-urlencoded',
        'Content-Length': Buffer.byteLength(postData),
      },
    };

    const req = https.request(options, (res) => {
      let data = '';
      res.on('data', (chunk) => { data += chunk; });
      res.on('end', () => {
        try {
          const result = JSON.parse(data);
          resolve(result);
        } catch (err) {
          reject(new Error(`Parse error: ${data}`));
        }
      });
    });

    req.on('error', reject);
    req.write(postData);
    req.end();
  });
}

// ── Local Server to Catch Redirect ───────────────────────────────────────

async function startLocalServer(): Promise<string> {
  return new Promise((resolve, reject) => {
    const server = http.createServer((req, res) => {
      const url = new URL(req.url || '/', 'http://localhost');
      const code = url.searchParams.get('code');
      const error = url.searchParams.get('error');

      if (code) {
        res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
        res.end('<html><body><h1>✅ Код получен! Можете закрыть эту страницу.</h1><p>Вернитесь в терминал.</p></body></html>');
        server.close();
        resolve(code);
      } else if (error) {
        res.writeHead(400, { 'Content-Type': 'text/html; charset=utf-8' });
        res.end(`<html><body><h1>❌ Ошибка: ${error}</h1></body></html>`);
        server.close();
        reject(new Error(`OAuth error: ${error}`));
      } else {
        res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
        res.end('<html><body><h1>Ожидание авторизации...</h1></body></html>');
      }
    });

    server.listen(9876, () => {
      console.log('  Local server listening on http://localhost:9876');
    });

    // Timeout after 5 minutes
    setTimeout(() => {
      server.close();
      reject(new Error('Timeout waiting for authorization'));
    }, 300000);
  });
}

// ── Verify Token ─────────────────────────────────────────────────────────

async function verifyToken(accessToken: string): Promise<any> {
  return new Promise((resolve, reject) => {
    const options = {
      hostname: 'cloud-api.yandex.net',
      path: '/v1/disk/',
      method: 'GET',
      headers: {
        'Authorization': `OAuth ${accessToken}`,
        'Accept': 'application/json',
      },
    };

    const req = https.request(options, (res) => {
      let data = '';
      res.on('data', (chunk) => { data += chunk; });
      res.on('end', () => {
        try {
          resolve(JSON.parse(data));
        } catch (err) {
          reject(new Error(`Parse error: ${data}`));
        }
      });
    });

    req.on('error', reject);
    req.end();
  });
}

// ── Main ─────────────────────────────────────────────────────────────────

async function main(): Promise<void> {
  console.log('╔══════════════════════════════════════════════════════════╗');
  console.log('║   YANDEX DISK — OAuth Token Acquisition                 ║');
  console.log('╚══════════════════════════════════════════════════════════╝\n');

  console.log(`Using App ${appNum}: ${app.description}`);
  console.log(`  ClientID: ${app.clientId}`);
  console.log(`  Redirect: ${app.redirectUri}\n`);

  // If code provided as argument, exchange it directly
  if (codeArg) {
    console.log(`Exchanging code: ${codeArg}`);
    const result = await exchangeCodeForToken(codeArg);

    if (result.access_token) {
      console.log('\n✅ Token obtained successfully!\n');
      console.log(`  Access Token:  ${result.access_token}`);
      console.log(`  Refresh Token: ${result.refresh_token}`);
      console.log(`  Expires In:    ${result.expires_in}s (${Math.floor(result.expires_in / 3600)}h)`);
      console.log(`  Token Type:    ${result.token_type}`);

      // Verify
      console.log('\nVerifying token...');
      const diskInfo = await verifyToken(result.access_token);
      if (diskInfo.user) {
        console.log(`  ✅ Disk accessible! User: ${diskInfo.user.login}`);
        console.log(`  Total space: ${(diskInfo.total_space / 1024 / 1024 / 1024).toFixed(1)} GB`);
        console.log(`  Used space:  ${(diskInfo.used_space / 1024 / 1024 / 1024).toFixed(1)} GB`);
      } else {
        console.log(`  ⚠ Verification result: ${JSON.stringify(diskInfo)}`);
      }

      // Output for .env
      console.log('\n── Add to .env ──────────────────────────────');
      console.log(`YDISK_TOKEN=${result.access_token}`);
      console.log(`YDISK_REFRESH_TOKEN=${result.refresh_token}`);
      console.log(`YDISK_CLIENT_ID=${app.clientId}`);
      console.log(`YDISK_CLIENT_SECRET=${app.clientSecret}`);
      console.log('─────────────────────────────────────────────');
    } else {
      console.log('\n❌ Token exchange failed!');
      console.log(JSON.stringify(result, null, 2));
    }
    return;
  }

  // If refresh token provided, refresh it
  if (tokenArg) {
    console.log(`Refreshing token: ${tokenArg.slice(0, 20)}...`);
    const result = await refreshToken(tokenArg);

    if (result.access_token) {
      console.log('\n✅ Token refreshed successfully!\n');
      console.log(`  Access Token:  ${result.access_token}`);
      console.log(`  Refresh Token: ${result.refresh_token || tokenArg}`);
      console.log(`  Expires In:    ${result.expires_in}s`);
    } else {
      console.log('\n❌ Token refresh failed!');
      console.log(JSON.stringify(result, null, 2));
    }
    return;
  }

  // No code — generate auth URL and guide user
  const authUrl = generateAuthUrl();

  console.log('ШАГ 1: Откройте ссылку в браузере и авторизуйтесь:\n');
  console.log(`  ${authUrl}\n`);
  console.log('ШАГ 2: После авторизации скопируйте код из страницы');
  console.log('        (он будет на странице подтверждения)\n');
  console.log('ШАГ 3: Запустите скрипт с кодом:\n');
  console.log(`  npx ts-node scripts/yandex-disk-oauth.ts --app ${appNum} --code ВАШ_КОД\n`);

  // Also try starting a local server (won't work for non-local redirect, but just in case)
  if (app.redirectUri.includes('localhost') || app.redirectUri.includes('127.0.0.1')) {
    console.log('Или подождите — локальный сервер слушает redirect...');
    try {
      const code = await startLocalServer();
      console.log(`\n✅ Код получен: ${code}\n`);
      const result = await exchangeCodeForToken(code);

      if (result.access_token) {
        console.log('✅ Token obtained!\n');
        console.log(`  Access Token:  ${result.access_token}`);
        console.log(`  Refresh Token: ${result.refresh_token}`);

        const diskInfo = await verifyToken(result.access_token);
        if (diskInfo.user) {
          console.log(`  ✅ Disk: ${diskInfo.user.login} (${(diskInfo.total_space / 1024 / 1024 / 1024).toFixed(1)} GB)`);
        }

        console.log('\n── Add to .env ──────────────────────────────');
        console.log(`YDISK_TOKEN=${result.access_token}`);
        console.log(`YDISK_REFRESH_TOKEN=${result.refresh_token}`);
        console.log(`YDISK_CLIENT_ID=${app.clientId}`);
        console.log(`YDISK_CLIENT_SECRET=${app.clientSecret}`);
        console.log('─────────────────────────────────────────────');
      }
    } catch (err: any) {
      console.log(`\nLocal server: ${err.message}`);
      console.log('Используйте ручной метод с кодом (шаги 1-3 выше)');
    }
  }
}

main().catch(err => {
  console.error('OAuth script failed:', err);
  process.exit(1);
});
