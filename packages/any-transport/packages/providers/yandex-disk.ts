/**
 * YTP YandexDiskProvider — Yandex Disk as file-based transport.
 *
 * Strategy: Upload chunks as files to Yandex Disk, share via public link,
 * other side downloads and reads. Each chunk is a file in a designated folder.
 *
 * This provides a high-bandwidth channel: Yandex Disk has generous rate limits
 * and supports files up to 50GB. We use small files (~4KB each) for low latency.
 *
 * Rate limits: ~30 requests/second, upload up to 50GB/day (free tier).
 * Message size: effectively unlimited (split into 4KB file chunks).
 *
 * Authentication: OAuth with auto-refresh using client_id/secret.
 * Get tokens: npx ts-node scripts/yandex-disk-oauth.ts
 *
 * Registered apps: credentials from environment variables (YDISK_CLIENT_ID, etc.)
 */

import * as https from 'https';
import type { Provider, ProviderCursor, ProviderMessage, OutboundFrame, AppendResult, ProviderCapabilities, RateHint } from './provider';

interface YandexDiskConfig {
  accessToken: string;           // Yandex Disk OAuth access token
  refreshToken?: string;         // OAuth refresh token for auto-renewal
  clientId?: string;             // App client ID for token refresh
  clientSecret?: string;         // App client secret for token refresh
  basePath?: string;             // folder path on Disk, default '/ytp/'
  label?: string;
}

interface DiskResource {
  name: string;
  path: string;
  created: string;
  size: number;
  public_url?: string;
}

export class YandexDiskProvider implements Provider {
  readonly id: string;

  private accessToken: string;
  private refreshToken: string | null;
  private clientId: string | null;
  private clientSecret: string | null;
  private basePath: string;
  private lastFileIndex = 0;
  private fileIndexCounter = 0;
  private tokenExpiresAt = 0;
  private refreshPromise: Promise<void> | null = null;

  constructor(config: YandexDiskConfig) {
    this.accessToken = config.accessToken;
    this.refreshToken = config.refreshToken || null;
    this.clientId = config.clientId || null;
    this.clientSecret = config.clientSecret || null;
    this.basePath = config.basePath || '/ytp/';
    this.id = config.label ? `ydisk-${config.label}` : 'ydisk';
  }

  private get apiUrl(): string {
    return 'https://cloud-api.yandex.net/v1/disk';
  }

  async start(): Promise<void> {
    // Verify token (will auto-refresh if needed)
    const resp = await this.authenticatedRequest('GET', '/');
    if (resp.user) {
      console.log(`[YandexDiskProvider:${this.id}] Authenticated as ${resp.user.login}`);
      if (resp.total_space) {
        const totalGB = (resp.total_space / 1024 / 1024 / 1024).toFixed(1);
        const usedGB = (resp.used_space / 1024 / 1024 / 1024).toFixed(1);
        console.log(`[YandexDiskProvider:${this.id}] Disk: ${usedGB}GB / ${totalGB}GB`);
      }
    } else if (resp.error) {
      // Try refreshing token
      if (this.refreshToken && this.clientId && this.clientSecret) {
        console.log(`[YandexDiskProvider:${this.id}] Token expired, refreshing...`);
        await this.refreshAccessToken();
        const retryResp = await this.authenticatedRequest('GET', '/');
        if (retryResp.user) {
          console.log(`[YandexDiskProvider:${this.id}] Refreshed! User: ${retryResp.user.login}`);
        } else {
          throw new Error(`Yandex Disk auth failed after refresh: ${JSON.stringify(retryResp)}`);
        }
      } else {
        throw new Error(`Yandex Disk auth failed: ${JSON.stringify(resp)}`);
      }
    }

    // Ensure base directory exists
    await this.ensureDirectory(this.basePath);

    // Create inbox/outbox subdirectories
    await this.ensureDirectory(this.basePath + 'inbox');
    await this.ensureDirectory(this.basePath + 'outbox');

    // Find last file index for cursor
    const files = await this.listFiles(this.basePath + 'inbox');
    if (files.length > 0) {
      const indices = files.map(f => this.parseFileIndex(f.name)).filter(i => i > 0);
      this.lastFileIndex = indices.length > 0 ? Math.max(...indices) : 0;
      console.log(`[YandexDiskProvider:${this.id}] Last inbox index: ${this.lastFileIndex}`);
    }

    console.log(`[YandexDiskProvider:${this.id}] Ready, basePath=${this.basePath}`);
  }

  async stop(): Promise<void> {
    // Nothing persistent to close
  }

  async append(frame: OutboundFrame): Promise<AppendResult> {
    this.fileIndexCounter++;
    const fileName = `${Date.now()}-${this.fileIndexCounter}.ytp`;
    const filePath = this.basePath + 'outbox/' + fileName;

    // Get upload URL
    const uploadInfo = await this.authenticatedRequest('GET', '/resources/upload', {
      path: filePath,
      overwrite: 'true',
    });

    if (!uploadInfo.href) {
      throw new Error(`Yandex Disk: failed to get upload URL for ${filePath}`);
    }

    // Upload content directly to the provided URL
    const uploadResp = await fetch(uploadInfo.href, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/octet-stream' },
      body: frame.text,
    });

    if (!uploadResp.ok) {
      const errText = await uploadResp.text().catch(() => '');
      throw new Error(`Yandex Disk upload failed: ${uploadResp.status} ${errText}`);
    }

    // Publish the file (make it accessible via public link)
    try {
      await this.authenticatedRequest('PUT', '/resources/publish', { path: filePath });
    } catch {
      // Non-critical — file is still accessible via API
    }

    return {
      messageId: fileName,
      timestamp: Date.now(),
    };
  }

  async scan(cursor: ProviderCursor): Promise<{
    messages: ProviderMessage[];
    nextCursor: ProviderCursor;
  }> {
    const sinceIndex = cursor ? Number(cursor) : this.lastFileIndex;

    try {
      const files = await this.listFiles(this.basePath + 'inbox');

      const newFiles = files.filter(f => {
        const idx = this.parseFileIndex(f.name);
        return idx > sinceIndex;
      });

      const messages: ProviderMessage[] = [];

      for (const file of newFiles) {
        try {
          // Get download URL for the file
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
              fromSelf: false,
            });

            this.lastFileIndex = Math.max(this.lastFileIndex, this.parseFileIndex(file.name));
          }
        } catch (err) {
          console.error(`[YandexDiskProvider:${this.id}] Error reading ${file.name}:`, err);
        }
      }

      return { messages, nextCursor: String(this.lastFileIndex) };
    } catch (err) {
      console.error(`[YandexDiskProvider:${this.id}] Scan error:`, err);
      return { messages: [], nextCursor: cursor };
    }
  }

  capabilities(): ProviderCapabilities {
    return {
      maxTextBytes: 4 * 1024,    // 4KB per file chunk (conservative for speed)
      supportsAttachments: false,
      supportsEdit: false,
      supportsDelete: true,
      supportsMessageIds: true,
      supportsServerTimestamp: true,
      minSafeSendIntervalMs: 100,
      recommendedPollIntervalMs: 3000,
    };
  }

  rateHint(): RateHint {
    return {
      minIntervalMs: 100,
      burst: 10,
      mode: 'aggressive',
    };
  }

  // ── Authenticated API request with auto-refresh ────────────────────────

  private async authenticatedRequest(method: string, endpoint: string, params?: Record<string, any>): Promise<any> {
    // Auto-refresh if token is about to expire
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

    // Some operations return 204 No Content
    if (resp.status === 204) return {};

    const data = await resp.json();

    // If unauthorized, try refreshing token once
    if (resp.status === 401 && this.refreshToken && this.clientId && this.clientSecret) {
      console.log(`[YandexDiskProvider:${this.id}] Got 401, refreshing token...`);
      await this.refreshAccessToken();
      return this.authenticatedRequest(method, endpoint, params);
    }

    return data;
  }

  // ── Token refresh ──────────────────────────────────────────────────────

  private async ensureValidToken(): Promise<void> {
    if (this.refreshPromise) {
      return this.refreshPromise;
    }

    if (Date.now() < this.tokenExpiresAt - 60000) {
      return; // Still valid
    }

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

    console.log(`[YandexDiskProvider:${this.id}] Refreshing OAuth token...`);

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
          try {
            resolve(JSON.parse(data));
          } catch (err) {
            reject(new Error(`Token refresh parse error: ${data}`));
          }
        });
      });

      req.on('error', reject);
      req.write(postData);
      req.end();
    });

    if (result.access_token) {
      this.accessToken = result.access_token;
      if (result.refresh_token) {
        this.refreshToken = result.refresh_token;
      }
      this.tokenExpiresAt = Date.now() + (result.expires_in || 31536000) * 1000;
      console.log(`[YandexDiskProvider:${this.id}] Token refreshed, expires in ${result.expires_in}s`);
    } else {
      throw new Error(`Token refresh failed: ${JSON.stringify(result)}`);
    }
  }

  // ── Helpers ────────────────────────────────────────────────────────────

  private async ensureDirectory(path: string): Promise<void> {
    try {
      await this.authenticatedRequest('PUT', '/resources', { path });
    } catch (err) {
      // Directory might already exist — that's fine
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

  private parseFileIndex(name: string): number {
    // Files are named like "1717300000000-1.ytp"
    const match = name.match(/^(\d+)-(\d+)\.ytp$/);
    if (match) {
      return parseInt(match[1], 10);
    }
    return 0;
  }
}
