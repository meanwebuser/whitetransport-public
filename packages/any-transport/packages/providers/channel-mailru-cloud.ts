/**
 * YTP MailRuCloudChannel — Облако Mail.ru as a composable Channel.
 *
 * Mail.ru Cloud (Облако Mail.ru) provides cloud file storage.
 * This channel uses the Mail.ru Cloud API to upload/download files.
 *
 * Auth: OAuth2 via Mail.ru (https://oauth.mail.ru/)
 *   - Register app at https://oauth.mail.ru/app/
 *   - Scopes: cloudapi (read/write access to cloud)
 *   - Auth flow: https://oauth.mail.ru/login?client_id={ID}&response_type=code&scope=cloudapi
 *   - Token exchange: POST https://oauth.mail.ru/token
 *
 * API: https://cloud.mail.ru/api/v2/
 *   - folder/list   — list folder contents
 *   - file/upload   — get upload URL
 *   - file/add      — commit uploaded file
 *   - file/publish  — make public link
 *   - file/remove   — delete file
 *
 * Bandwidth: ~10 requests/sec, 100GB free storage.
 * File size limit: 2GB per file.
 *
 * NOTE: Mail.ru Cloud API requires cookies-based auth for some operations.
 * This implementation uses the OAuth token + API approach where possible.
 * For full functionality, you may need to use the web API with cookies.
 */

import * as https from 'https';
import type { Channel, ChannelMessage, ChannelAttachment, ChannelCapabilities } from './compose';

export interface MailRuCloudChannelConfig {
  /** OAuth2 access token */
  accessToken: string;
  /** OAuth2 refresh token for auto-renewal */
  refreshToken?: string;
  /** App client ID for token refresh */
  clientId?: string;
  /** App client secret for token refresh */
  clientSecret?: string;
  /** Base folder in cloud. Default: '/ytp/' */
  basePath?: string;
  /** Peer identifier prefix for filenames. Default: random */
  peerPrefix?: string;
  label?: string;
}

interface CloudFileInfo {
  name: string;
  path: string;
  size: number;
  kind: string;
  mtime: number;
  hash: string;
}

export class MailRuCloudChannel implements Channel {
  readonly id: string;

  private accessToken: string;
  private refreshToken: string | null;
  private clientId: string | null;
  private clientSecret: string | null;
  private basePath: string;
  private peerPrefix: string;
  private fileCounter = 0;
  private tokenExpiresAt = 0;
  private refreshPromise: Promise<void> | null = null;

  constructor(config: MailRuCloudChannelConfig) {
    this.accessToken = config.accessToken;
    this.refreshToken = config.refreshToken || null;
    this.clientId = config.clientId || null;
    this.clientSecret = config.clientSecret || null;
    this.basePath = (config.basePath || '/ytp/').replace(/\/+$/, '') + '/';
    this.peerPrefix = config.peerPrefix || `p${Math.random().toString(36).slice(2, 6)}`;
    this.id = config.label ? `mailru-${config.label}` : 'mailru';
  }

  private get apiBaseUrl(): string {
    return 'https://cloud.mail.ru/api/v2';
  }

  // ── Lifecycle ──────────────────────────────────────────────────────────

  async init(): Promise<void> {
    // Verify token by fetching user info
    try {
      const resp = await this.authenticatedRequest('GET', '/user', {});
      if (resp.email) {
        console.log(`[MailRuCloudChannel:${this.id}] Authenticated as ${resp.email}`);
        if (resp.cloud.used) {
          const usedGB = (resp.cloud.used / 1024 / 1024 / 1024).toFixed(1);
          const totalGB = (resp.cloud.total / 1024 / 1024 / 1024).toFixed(1);
          console.log(`[MailRuCloudChannel:${this.id}] Cloud: ${usedGB}GB / ${totalGB}GB`);
        }
      } else {
        // Try refreshing token
        if (this.refreshToken && this.clientId && this.clientSecret) {
          await this.refreshAccessToken();
          const retry = await this.authenticatedRequest('GET', '/user', {});
          if (!retry.email) {
            throw new Error(`Mail.ru Cloud auth failed: ${JSON.stringify(retry)}`);
          }
        } else {
          throw new Error(`Mail.ru Cloud auth failed: ${JSON.stringify(resp)}`);
        }
      }
    } catch (err: any) {
      // If /user doesn't work, try a simple folder list to verify auth
      try {
        await this.listFiles('/');
        console.log(`[MailRuCloudChannel:${this.id}] Auth verified via folder list`);
      } catch {
        throw new Error(`Mail.ru Cloud auth failed: ${err.message}`);
      }
    }

    // Ensure directories exist
    await this.ensureDirectory(this.basePath);
    await this.ensureDirectory(this.basePath + 'inbox');
    await this.ensureDirectory(this.basePath + 'outbox');

    console.log(`[MailRuCloudChannel:${this.id}] Ready, basePath=${this.basePath}, peerPrefix=${this.peerPrefix}`);
  }

  async destroy(): Promise<void> {}

  // ── Send = upload file to outbox ───────────────────────────────────────

  async sendMessage(text: string, attachment?: string): Promise<{ messageId: string; timestamp: number }> {
    this.fileCounter++;
    const fileName = `${this.peerPrefix}-${Date.now()}-${this.fileCounter}.ytp`;
    const cloudPath = this.basePath + 'outbox/' + fileName;

    // Step 1: Get upload URL
    const uploadInfo = await this.authenticatedRequest('GET', '/file/upload', {
      path: cloudPath,
    });

    if (!uploadInfo.url) {
      throw new Error(`Mail.ru Cloud: failed to get upload URL for ${cloudPath}`);
    }

    // Step 2: Upload content to the provided URL
    const uploadResp = await fetch(uploadInfo.url, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/octet-stream' },
      body: text,
    });

    if (!uploadResp.ok) {
      const errText = await uploadResp.text().catch(() => '');
      throw new Error(`Mail.ru Cloud upload failed: ${uploadResp.status} ${errText}`);
    }

    // Step 3: Commit the file (add it to cloud)
    await this.authenticatedRequest('POST', '/file/add', {
      path: cloudPath,
      size: Buffer.byteLength(text),
      hash: await this.simpleHash(text),
    });

    return { messageId: fileName, timestamp: Date.now() };
  }

  // ── Poll = list new files in inbox ─────────────────────────────────────

  async poll(since: string | number | null, timeout: number): Promise<{
    messages: ChannelMessage[];
    nextCursor: string | number;
  }> {
    const sinceName = since ? String(since) : '';

    try {
      const files = await this.listFiles(this.basePath + 'inbox');

      const newFiles = files.filter(f => {
        if (sinceName && f.name <= sinceName) return false;
        if (f.name.startsWith(this.peerPrefix + '-')) return false;
        return true;
      });

      const messages: ChannelMessage[] = [];

      for (const file of newFiles) {
        try {
          // Get download URL
          const downloadInfo = await this.authenticatedRequest('GET', '/file/download', {
            path: file.path,
          });

          if (downloadInfo.url) {
            const contentResp = await fetch(downloadInfo.url);
            const text = await contentResp.text();

            messages.push({
              id: file.name,
              timestamp: file.mtime * 1000,
              text,
              fromSelf: false,
              attachments: [],
            });
          }
        } catch (err) {
          console.error(`[MailRuCloudChannel:${this.id}] Error reading ${file.name}:`, err);
        }
      }

      let maxName = sinceName;
      for (const f of newFiles) {
        if (f.name > maxName) maxName = f.name;
      }

      return { messages, nextCursor: maxName };
    } catch (err) {
      console.error(`[MailRuCloudChannel:${this.id}] Poll error:`, err);
      return { messages: [], nextCursor: since ?? '' };
    }
  }

  // ── Upload/Download ────────────────────────────────────────────────────

  async uploadDocument(data: Buffer, filename: string): Promise<string> {
    this.fileCounter++;
    const cloudPath = this.basePath + 'outbox/' + filename;

    const uploadInfo = await this.authenticatedRequest('GET', '/file/upload', {
      path: cloudPath,
    });

    if (!uploadInfo.url) {
      throw new Error(`Mail.ru Cloud: failed to get upload URL for ${cloudPath}`);
    }

    const uploadResp = await fetch(uploadInfo.url, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/octet-stream' },
      body: data as unknown as BodyInit,
    });

    if (!uploadResp.ok) {
      throw new Error(`Mail.ru Cloud upload failed: ${uploadResp.status}`);
    }

    await this.authenticatedRequest('POST', '/file/add', {
      path: cloudPath,
      size: data.length,
      hash: await this.simpleHashBuffer(data),
    });

    return cloudPath;
  }

  async uploadPhoto(data: Buffer, filename: string): Promise<string> {
    return this.uploadDocument(data, filename);
  }

  async downloadAttachment(attachment: ChannelAttachment): Promise<Buffer> {
    const path = attachment.id || attachment.url;
    if (!path) throw new Error('No attachment path');

    const downloadInfo = await this.authenticatedRequest('GET', '/file/download', { path });
    if (!downloadInfo.url) {
      throw new Error(`Mail.ru Cloud: failed to get download URL for ${path}`);
    }

    const resp = await fetch(downloadInfo.url);
    return Buffer.from(await resp.arrayBuffer());
  }

  // ── Capabilities ──────────────────────────────────────────────────────

  caps(): ChannelCapabilities {
    return {
      maxTextBytes: 4 * 1024 * 1024, // 4MB per file
      supportsDocuments: true,
      supportsPhotos: true,
      supportsLongPoll: false,
      supportsWebhook: false,
      minSendIntervalMs: 100,    // ~10 req/sec
      maxBurst: 5,
    };
  }

  // ── Auth with auto-refresh ─────────────────────────────────────────────

  private async authenticatedRequest(method: string, endpoint: string, params?: Record<string, any>): Promise<any> {
    if (this.tokenExpiresAt > 0 && Date.now() > this.tokenExpiresAt - 60000) {
      await this.ensureValidToken();
    }

    const url = new URL(this.apiBaseUrl + endpoint);

    // Mail.ru Cloud API uses token as query param or header
    url.searchParams.append('access_token', this.accessToken);

    if (params) {
      for (const [key, value] of Object.entries(params)) {
        if (value !== undefined && value !== null) {
          url.searchParams.append(key, String(value));
        }
      }
    }

    const resp = await fetch(url.toString(), {
      method,
      headers: { 'Accept': 'application/json' },
    });

    if (resp.status === 204) return {};

    const data: any = await resp.json();

    // Mail.ru API returns { status: 200, body: ... } or { status: 400, body: { error: ... } }
    if (data.status === 200 || data.status === 201) {
      return data.body || data;
    }

    if (resp.status === 401 && this.refreshToken && this.clientId && this.clientSecret) {
      console.log(`[MailRuCloudChannel:${this.id}] Got 401, refreshing token...`);
      await this.refreshAccessToken();
      return this.authenticatedRequest(method, endpoint, params);
    }

    return data;
  }

  private async ensureValidToken(): Promise<void> {
    if (this.refreshPromise) return this.refreshPromise;
    if (Date.now() < this.tokenExpiresAt - 60000) return;

    this.refreshPromise = this.refreshAccessToken();
    try {
      await this.refreshPromise;
    } finally {
      this.refreshPromise = null;
    }
  }

  private async refreshAccessToken(): Promise<void> {
    if (!this.refreshToken || !this.clientId || !this.clientSecret) {
      throw new Error('Cannot refresh token: missing refreshToken, clientId, or clientSecret');
    }

    console.log(`[MailRuCloudChannel:${this.id}] Refreshing OAuth token...`);

    const postData = new URLSearchParams({
      grant_type: 'refresh_token',
      refresh_token: this.refreshToken,
      client_id: this.clientId,
      client_secret: this.clientSecret,
    }).toString();

    const result = await new Promise<any>((resolve, reject) => {
      const options = {
        hostname: 'oauth.mail.ru',
        path: '/token',
        method: 'POST',
        headers: {
          'Content-Type': 'application/x-www-form-urlencoded',
          'Content-Length': Buffer.byteLength(postData),
        },
      };

      const req = https.request(options, (res) => {
        let data = '';
        res.on('data', (chunk: Buffer) => { data += chunk; });
        res.on('end', () => {
          try { resolve(JSON.parse(data)); }
          catch (err) { reject(new Error(`Token refresh parse error: ${data}`)); }
        });
      });

      req.on('error', reject);
      req.write(postData);
      req.end();
    });

    if (result.access_token) {
      this.accessToken = result.access_token;
      if (result.refresh_token) this.refreshToken = result.refresh_token;
      this.tokenExpiresAt = Date.now() + (result.expires_in || 86400) * 1000;
      console.log(`[MailRuCloudChannel:${this.id}] Token refreshed, expires in ${result.expires_in}s`);
    } else {
      throw new Error(`Token refresh failed: ${JSON.stringify(result)}`);
    }
  }

  // ── Helpers ────────────────────────────────────────────────────────────

  private async ensureDirectory(path: string): Promise<void> {
    try {
      await this.authenticatedRequest('POST', '/folder/add', { path });
    } catch {
      // Directory might already exist
    }
  }

  private async listFiles(path: string): Promise<CloudFileInfo[]> {
    const resp = await this.authenticatedRequest('GET', '/folder/list', {
      path,
      limit: 100,
      sort: 'mtime',
    });

    if (resp.list) {
      return resp.list.filter((item: any) => item.kind === 'file');
    }
    return [];
  }

  private async simpleHash(text: string): Promise<string> {
    const { createHash } = await import('crypto');
    return createHash('sha256').update(text).digest('hex').slice(0, 16);
  }

  private async simpleHashBuffer(data: Buffer): Promise<string> {
    const { createHash } = await import('crypto');
    return createHash('sha256').update(data).digest('hex').slice(0, 16);
  }
}
