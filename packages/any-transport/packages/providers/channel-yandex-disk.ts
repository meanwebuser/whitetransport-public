/**
 * YTP YandexDiskChannel — Yandex Disk as a composable Channel.
 *
 * Cloud storage channels work differently from messaging channels:
 *   - "mailbox" = a directory on Yandex Disk
 *   - sendMessage = upload a file to outbox/
 *   - poll = list new files in inbox/ since cursor
 *   - No long poll or webhooks (timer-only)
 *
 * Auth: OAuth with auto-refresh using client_id/secret.
 * Get tokens: npx ts-node scripts/yandex-disk-oauth.ts
 *
 * Bandwidth: ~30 requests/sec, up to 50GB/day (free tier).
 * File size: up to 50GB per file (we use ~4KB chunks for low latency).
 */

import * as https from 'https';
import type { Channel, ChannelMessage, ChannelAttachment, ChannelCapabilities } from './compose';

export interface YandexDiskChannelConfig {
  accessToken: string;
  refreshToken?: string;
  clientId?: string;
  clientSecret?: string;
  /** Base folder on Disk. Default: '/ytp/' */
  basePath?: string;
  /** Peer identifier prefix for filenames (to distinguish own files). Default: random */
  peerPrefix?: string;
  label?: string;
}

interface DiskResource {
  name: string;
  path: string;
  created: string;
  size: number;
  public_url?: string;
}

export class YandexDiskChannel implements Channel {
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

  constructor(config: YandexDiskChannelConfig) {
    this.accessToken = config.accessToken;
    this.refreshToken = config.refreshToken || null;
    this.clientId = config.clientId || null;
    this.clientSecret = config.clientSecret || null;
    this.basePath = (config.basePath || '/ytp/').replace(/\/+$/, '') + '/';
    this.peerPrefix = config.peerPrefix || `p${Math.random().toString(36).slice(2, 6)}`;
    this.id = config.label ? `ydisk-${config.label}` : 'ydisk';
  }

  private get apiUrl(): string {
    return 'https://cloud-api.yandex.net/v1/disk';
  }

  // ── Lifecycle ──────────────────────────────────────────────────────────

  async init(): Promise<void> {
    // Verify token (auto-refresh if needed)
    const resp = await this.authenticatedRequest('GET', '/');
    if (resp.user) {
      console.log(`[YandexDiskChannel:${this.id}] Authenticated as ${resp.user.login}`);
      if (resp.total_space) {
        const totalGB = (resp.total_space / 1024 / 1024 / 1024).toFixed(1);
        const usedGB = (resp.used_space / 1024 / 1024 / 1024).toFixed(1);
        console.log(`[YandexDiskChannel:${this.id}] Disk: ${usedGB}GB / ${totalGB}GB`);
      }
    } else if (resp.error) {
      if (this.refreshToken && this.clientId && this.clientSecret) {
        console.log(`[YandexDiskChannel:${this.id}] Token expired, refreshing...`);
        await this.refreshAccessToken();
        const retry = await this.authenticatedRequest('GET', '/');
        if (!retry.user) {
          throw new Error(`Yandex Disk auth failed after refresh: ${JSON.stringify(retry)}`);
        }
        console.log(`[YandexDiskChannel:${this.id}] Refreshed! User: ${retry.user.login}`);
      } else {
        throw new Error(`Yandex Disk auth failed: ${JSON.stringify(resp)}`);
      }
    }

    // Ensure directories exist
    await this.ensureDirectory(this.basePath);
    await this.ensureDirectory(this.basePath + 'inbox');
    await this.ensureDirectory(this.basePath + 'outbox');

    console.log(`[YandexDiskChannel:${this.id}] Ready, basePath=${this.basePath}, peerPrefix=${this.peerPrefix}`);
  }

  async destroy(): Promise<void> {
    // Nothing to close
  }

  // ── Send = upload file to outbox ───────────────────────────────────────

  async sendMessage(text: string, attachment?: string): Promise<{ messageId: string; timestamp: number }> {
    this.fileCounter++;
    const fileName = `${this.peerPrefix}-${Date.now()}-${this.fileCounter}.ytp`;
    const filePath = this.basePath + 'outbox/' + fileName;

    // Get upload URL
    const uploadInfo = await this.authenticatedRequest('GET', '/resources/upload', {
      path: filePath,
      overwrite: 'true',
    });

    if (!uploadInfo.href) {
      throw new Error(`Yandex Disk: failed to get upload URL for ${filePath}`);
    }

    // Upload content
    const uploadResp = await fetch(uploadInfo.href, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/octet-stream' },
      body: text,
    });

    if (!uploadResp.ok) {
      const errText = await uploadResp.text().catch(() => '');
      throw new Error(`Yandex Disk upload failed: ${uploadResp.status} ${errText}`);
    }

    // Publish the file (make accessible via public link)
    try {
      await this.authenticatedRequest('PUT', '/resources/publish', { path: filePath });
    } catch {
      // Non-critical
    }

    return { messageId: fileName, timestamp: Date.now() };
  }

  // ── Poll = list new files in inbox ─────────────────────────────────────

  async poll(since: string | number | null, timeout: number): Promise<{
    messages: ChannelMessage[];
    nextCursor: string | number;
  }> {
    // timeout is ignored — cloud storage doesn't support long poll
    const sinceName = since ? String(since) : '';

    try {
      const files = await this.listFiles(this.basePath + 'inbox');

      // Filter new files (by name > cursor, and exclude own files)
      const newFiles = files.filter(f => {
        if (sinceName && f.name <= sinceName) return false;
        // Exclude files from our own peer prefix
        if (f.name.startsWith(this.peerPrefix + '-')) return false;
        return true;
      });

      const messages: ChannelMessage[] = [];

      for (const file of newFiles) {
        try {
          // Get download URL
          const downloadInfo = await this.authenticatedRequest('GET', '/resources/download', {
            path: file.path,
          });

          if (downloadInfo.href) {
            const contentResp = await fetch(downloadInfo.href);
            const text = await contentResp.text();

            messages.push({
              id: file.name,
              timestamp: new Date(file.created).getTime(),
              text,
              fromSelf: false, // We filter own files above
              attachments: [],
            });
          }
        } catch (err) {
          console.error(`[YandexDiskChannel:${this.id}] Error reading ${file.name}:`, err);
        }
      }

      // Cursor = lexicographic max filename
      let maxName = sinceName;
      for (const f of newFiles) {
        if (f.name > maxName) maxName = f.name;
      }

      return { messages, nextCursor: maxName };
    } catch (err) {
      console.error(`[YandexDiskChannel:${this.id}] Poll error:`, err);
      return { messages: [], nextCursor: since ?? '' };
    }
  }

  // ── Upload/Download ────────────────────────────────────────────────────

  async uploadDocument(data: Buffer, filename: string): Promise<string> {
    this.fileCounter++;
    const filePath = this.basePath + 'outbox/' + filename;

    const uploadInfo = await this.authenticatedRequest('GET', '/resources/upload', {
      path: filePath,
      overwrite: 'true',
    });

    if (!uploadInfo.href) {
      throw new Error(`Yandex Disk: failed to get upload URL for ${filePath}`);
    }

    const uploadResp = await fetch(uploadInfo.href, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/octet-stream' },
      body: data as unknown as BodyInit,
    });

    if (!uploadResp.ok) {
      const errText = await uploadResp.text().catch(() => '');
      throw new Error(`Yandex Disk upload failed: ${uploadResp.status} ${errText}`);
    }

    try {
      await this.authenticatedRequest('PUT', '/resources/publish', { path: filePath });
    } catch {
      // Non-critical
    }

    return filePath;
  }

  async uploadPhoto(data: Buffer, filename: string): Promise<string> {
    // Yandex Disk doesn't distinguish photos from documents — same upload
    return this.uploadDocument(data, filename);
  }

  async downloadAttachment(attachment: ChannelAttachment): Promise<Buffer> {
    // Download by file path (stored in attachment.id for cloud channels)
    const path = attachment.id || attachment.url;
    if (!path) throw new Error('No attachment path');

    const downloadInfo = await this.authenticatedRequest('GET', '/resources/download', { path });
    if (!downloadInfo.href) {
      throw new Error(`Yandex Disk: failed to get download URL for ${path}`);
    }

    const resp = await fetch(downloadInfo.href);
    return Buffer.from(await resp.arrayBuffer());
  }

  // ── Capabilities ──────────────────────────────────────────────────────

  caps(): ChannelCapabilities {
    return {
      maxTextBytes: 4 * 1024 * 1024, // 4MB per file (generous)
      supportsDocuments: true,
      supportsPhotos: true,
      supportsLongPoll: false,   // Timer-only
      supportsWebhook: false,    // No webhooks
      minSendIntervalMs: 100,    // ~30 req/sec
      maxBurst: 10,
    };
  }

  // ── Auth with auto-refresh ─────────────────────────────────────────────

  private async authenticatedRequest(method: string, endpoint: string, params?: Record<string, any>): Promise<any> {
    if (this.tokenExpiresAt > 0 && Date.now() > this.tokenExpiresAt - 60000) {
      await this.ensureValidToken();
    }

    const url = new URL(this.apiUrl + endpoint);

    if (params) {
      for (const [key, value] of Object.entries(params)) {
        if (value !== undefined && value !== null) {
          url.searchParams.append(key, String(value));
        }
      }
    }

    const resp = await fetch(url.toString(), {
      method,
      headers: {
        'Authorization': `OAuth ${this.accessToken}`,
        'Accept': 'application/json',
      },
    });

    if (resp.status === 204) return {};

    const data = await resp.json();

    if (resp.status === 401 && this.refreshToken && this.clientId && this.clientSecret) {
      console.log(`[YandexDiskChannel:${this.id}] Got 401, refreshing token...`);
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

    console.log(`[YandexDiskChannel:${this.id}] Refreshing OAuth token...`);

    const postData = new URLSearchParams({
      grant_type: 'refresh_token',
      refresh_token: this.refreshToken,
      client_id: this.clientId,
      client_secret: this.clientSecret,
    }).toString();

    const result = await new Promise<any>((resolve, reject) => {
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
      this.tokenExpiresAt = Date.now() + (result.expires_in || 31536000) * 1000;
      console.log(`[YandexDiskChannel:${this.id}] Token refreshed, expires in ${result.expires_in}s`);
    } else {
      throw new Error(`Token refresh failed: ${JSON.stringify(result)}`);
    }
  }

  // ── Helpers ────────────────────────────────────────────────────────────

  private async ensureDirectory(path: string): Promise<void> {
    try {
      await this.authenticatedRequest('PUT', '/resources', { path });
    } catch {
      // Directory might already exist
    }
  }

  private async listFiles(path: string): Promise<DiskResource[]> {
    const resp = await this.authenticatedRequest('GET', '/resources', {
      path,
      limit: 100,
      sort: 'created',
    });

    if (resp._embedded && resp._embedded.items) {
      return resp._embedded.items.filter((item: any) => item.type === 'file');
    }
    return [];
  }
}
