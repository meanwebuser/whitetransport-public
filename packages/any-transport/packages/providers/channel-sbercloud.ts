/**
 * YTP SberCloudChannel — SberCloud Object Storage as a composable Channel.
 *
 * SberCloud (Cloud.ru / СберКлауд) provides S3-compatible Object Storage.
 * This channel uses the standard S3 API to upload/download objects.
 *
 * Also compatible with any S3-compatible storage:
 *   - SberCloud Object Storage (https://cloud.ru/)
 *   - VK Cloud Solutions (https://mcs.mail.ru/)
 *   - Yandex Object Storage (https://cloud.yandex.ru/services/storage)
 *   - MinIO (self-hosted)
 *   - AWS S3
 *
 * Auth: Access Key ID + Secret Access Key (HMAC-SHA256 signing)
 *   - Get keys from SberCloud console: Object Storage → Service accounts
 *
 * Protocol: S3-compatible
 *   - PUT object    — upload file
 *   - GET object    — download file
 *   - LIST objects   — list files in bucket/prefix
 *   - DELETE object  — delete file
 *
 * Bandwidth: Very high (S3 is designed for this), 5500 GET/sec, 3500 PUT/sec.
 * Object size: 5TB max (we use ~4KB chunks for low latency).
 *
 * Region endpoints:
 *   - SberCloud:   https://hb.bizmrg.com   (ru-msk)
 *   - VK Cloud:    https://hb.vkcloud-storage.com
 *   - Yandex:      https://storage.yandexcloud.net
 */

import { createHmac, createHash } from 'crypto';
import type { Channel, ChannelMessage, ChannelAttachment, ChannelCapabilities } from './compose';

export interface SberCloudChannelConfig {
  /** S3 Access Key ID */
  accessKeyId: string;
  /** S3 Secret Access Key */
  secretAccessKey: string;
  /** Bucket name */
  bucket: string;
  /** S3 endpoint URL. Default: 'https://hb.bizmrg.com' (SberCloud) */
  endpoint?: string;
  /** Region name. Default: 'ru-msk' */
  region?: string;
  /** Key prefix inside bucket (like a folder). Default: 'ytp/' */
  prefix?: string;
  /** Peer identifier prefix for filenames. Default: random */
  peerPrefix?: string;
  label?: string;
}

export class SberCloudChannel implements Channel {
  readonly id: string;

  private config: SberCloudChannelConfig;
  private endpoint: string;
  private region: string;
  private prefix: string;
  private peerPrefix: string;
  private fileCounter = 0;

  constructor(config: SberCloudChannelConfig) {
    this.config = config;
    this.endpoint = (config.endpoint || 'https://hb.bizmrg.com').replace(/\/+$/, '');
    this.region = config.region || 'ru-msk';
    this.prefix = (config.prefix || 'ytp/').replace(/^\/+/, '').replace(/\/+$/, '') + '/';
    this.peerPrefix = config.peerPrefix || `p${Math.random().toString(36).slice(2, 6)}`;
    this.id = config.label ? `s3-${config.label}` : 's3';
  }

  // ── Lifecycle ──────────────────────────────────────────────────────────

  async init(): Promise<void> {
    // Verify credentials by listing the bucket (or prefix)
    try {
      const result = await this.s3Request('GET', '/', { 'list-type': '2', prefix: this.prefix, 'max-keys': '1' });
      // If we can list, credentials are valid
      console.log(`[SberCloudChannel:${this.id}] Authenticated, endpoint=${this.endpoint}, bucket=${this.config.bucket}`);
    } catch (err: any) {
      // Try to create prefix by writing a marker file
      try {
        await this.s3PutObject(this.prefix + '.keep', Buffer.from('ytp'));
        console.log(`[SberCloudChannel:${this.id}] Created prefix ${this.prefix}`);
      } catch (putErr: any) {
        throw new Error(`SberCloud auth/setup failed: ${err.message}`);
      }
    }

    // Create inbox/outbox marker files
    try {
      await this.s3PutObject(this.prefix + 'inbox/.keep', Buffer.from('ytp'));
      await this.s3PutObject(this.prefix + 'outbox/.keep', Buffer.from('ytp'));
    } catch {
      // Non-critical
    }

    console.log(`[SberCloudChannel:${this.id}] Ready, prefix=${this.prefix}, peerPrefix=${this.peerPrefix}`);
  }

  async destroy(): Promise<void> {}

  // ── Send = upload file to outbox ───────────────────────────────────────

  async sendMessage(text: string, attachment?: string): Promise<{ messageId: string; timestamp: number }> {
    this.fileCounter++;
    const fileName = `${this.peerPrefix}-${Date.now()}-${this.fileCounter}.ytp`;
    const key = this.prefix + 'outbox/' + fileName;

    await this.s3PutObject(key, Buffer.from(text, 'utf-8'));

    return { messageId: fileName, timestamp: Date.now() };
  }

  // ── Poll = list new files in inbox ─────────────────────────────────────

  async poll(since: string | number | null, timeout: number): Promise<{
    messages: ChannelMessage[];
    nextCursor: string | number;
  }> {
    const sinceName = since ? String(since) : '';

    try {
      const objects = await this.s3ListObjects(this.prefix + 'inbox/');

      // Filter new objects (by name > cursor, exclude own and marker files)
      const newObjects = objects.filter(o => {
        if (o.key.endsWith('.keep')) return false;
        if (sinceName && o.name <= sinceName) return false;
        if (o.name.startsWith(this.peerPrefix + '-')) return false;
        return true;
      });

      const messages: ChannelMessage[] = [];

      for (const obj of newObjects) {
        try {
          const data = await this.s3GetObject(obj.key);
          messages.push({
            id: obj.name,
            timestamp: obj.lastModified.getTime(),
            text: data.toString('utf-8'),
            fromSelf: false,
            attachments: [],
          });
        } catch (err) {
          console.error(`[SberCloudChannel:${this.id}] Error reading ${obj.name}:`, err);
        }
      }

      let maxName = sinceName;
      for (const o of newObjects) {
        if (o.name > maxName) maxName = o.name;
      }

      return { messages, nextCursor: maxName };
    } catch (err) {
      console.error(`[SberCloudChannel:${this.id}] Poll error:`, err);
      return { messages: [], nextCursor: since ?? '' };
    }
  }

  // ── Upload/Download ────────────────────────────────────────────────────

  async uploadDocument(data: Buffer, filename: string): Promise<string> {
    const key = this.prefix + 'outbox/' + filename;
    await this.s3PutObject(key, data);
    return key;
  }

  async uploadPhoto(data: Buffer, filename: string): Promise<string> {
    return this.uploadDocument(data, filename);
  }

  async downloadAttachment(attachment: ChannelAttachment): Promise<Buffer> {
    const key = attachment.id || attachment.url;
    if (!key) throw new Error('No attachment key');
    return this.s3GetObject(key);
  }

  // ── Capabilities ──────────────────────────────────────────────────────

  caps(): ChannelCapabilities {
    return {
      maxTextBytes: 5 * 1024 * 1024 * 1024, // 5GB per object (S3 limit)
      supportsDocuments: true,
      supportsPhotos: true,
      supportsLongPoll: false,
      supportsWebhook: false,
      minSendIntervalMs: 10,  // Very fast
      maxBurst: 50,
    };
  }

  // ── S3 Operations ─────────────────────────────────────────────────────

  private async s3PutObject(key: string, data: Buffer): Promise<void> {
    await this.s3Request('PUT', `/${key}`, {}, data, {
      'Content-Type': 'application/octet-stream',
      'Content-Length': String(data.length),
    });
  }

  private async s3GetObject(key: string): Promise<Buffer> {
    const resp = await this.s3Request('GET', `/${key}`, {}, undefined, {}, true);
    return Buffer.from(await (resp as Response).arrayBuffer());
  }

  private async s3ListObjects(prefix: string): Promise<Array<{ key: string; name: string; size: number; lastModified: Date }>> {
    const resp = await this.s3Request('GET', '/', { 'list-type': '2', prefix, 'max-keys': '100' });
    const text = typeof resp === 'string' ? resp : await new Response(resp as any).text();

    // Parse XML response (ListObjectsV2)
    const objects: Array<{ key: string; name: string; size: number; lastModified: Date }> = [];
    const contentRegex = /<Contents>\s*<Key>([^<]+)<\/Key>\s*<LastModified>([^<]+)<\/LastModified>\s*<Size>(\d+)<\/Size>/g;
    let match;

    while ((match = contentRegex.exec(text)) !== null) {
      const key = match[1];
      const name = key.split('/').pop() || key;
      objects.push({
        key,
        name,
        size: parseInt(match[3], 10),
        lastModified: new Date(match[2]),
      });
    }

    return objects;
  }

  // ── S3 Signing (AWS Signature Version 4) ──────────────────────────────

  private async s3Request(
    method: string,
    path: string,
    queryParams: Record<string, string> = {},
    body?: Buffer | string,
    extraHeaders: Record<string, string> = {},
    returnRawResponse?: boolean,
  ): Promise<any> {
    const now = new Date();
    const dateStamp = this.formatDate(now);
    const amzDate = this.formatDateTime(now);

    // Build canonical query string
    const sortedQueryKeys = Object.keys(queryParams).sort();
    const canonicalQueryString = sortedQueryKeys.map(k =>
      `${encodeURIComponent(k)}=${encodeURIComponent(queryParams[k])}`
    ).join('&');

    // Build canonical headers
    const host = new URL(this.endpoint).hostname;
    const headers: Record<string, string> = {
      host,
      'x-amz-date': amzDate,
      'x-amz-content-sha256': createHash('sha256').update(body || '').digest('hex'),
      ...extraHeaders,
    };

    const signedHeaderKeys = Object.keys(headers).map(k => k.toLowerCase()).sort();
    const signedHeaders = signedHeaderKeys.join(';');
    const canonicalHeaders = signedHeaderKeys.map(k =>
      `${k}:${headers[k.toLowerCase()] || headers[k]}\n`
    ).join('');

    // Build canonical request
    const canonicalUri = path;
    const payloadHash = createHash('sha256').update(body || '').digest('hex');
    const canonicalRequest = [
      method,
      canonicalUri,
      canonicalQueryString,
      canonicalHeaders,
      signedHeaders,
      payloadHash,
    ].join('\n');

    // Build string to sign
    const credentialScope = `${dateStamp}/${this.region}/s3/aws4_request`;
    const stringToSign = [
      'AWS4-HMAC-SHA256',
      amzDate,
      credentialScope,
      createHash('sha256').update(canonicalRequest).digest('hex'),
    ].join('\n');

    // Calculate signature
    const signingKey = this.getSignatureKey(dateStamp);
    const signature = createHmac('sha256', signingKey)
      .update(stringToSign)
      .digest('hex');

    // Build authorization header
    const authHeader = `AWS4-HMAC-SHA256 Credential=${this.config.accessKeyId}/${credentialScope}, SignedHeaders=${signedHeaders}, Signature=${signature}`;

    // Build final URL
    const url = `${this.endpoint}/${this.config.bucket}${path}${Object.keys(queryParams).length > 0 ? '?' + canonicalQueryString : ''}`;

    // Make request
    const fetchHeaders: Record<string, string> = {
      ...headers,
      'Authorization': authHeader,
    };
    // Remove 'host' header (fetch sets it automatically)
    delete fetchHeaders.host;

    const resp = await fetch(url, {
      method,
      headers: fetchHeaders,
      body: body as any,
    });

    if (returnRawResponse) {
      return resp;
    }

    if (resp.status >= 400) {
      const errText = await resp.text().catch(() => '');
      throw new Error(`S3 ${method} ${path} failed: ${resp.status} ${errText}`);
    }

    const text = await resp.text();
    return text;
  }

  private getSignatureKey(dateStamp: string): Buffer {
    const kDate = createHmac('sha256', `AWS4${this.config.secretAccessKey}`).update(dateStamp).digest();
    const kRegion = createHmac('sha256', kDate).update(this.region).digest();
    const kService = createHmac('sha256', kRegion).update('s3').digest();
    const kSigning = createHmac('sha256', kService).update('aws4_request').digest();
    return kSigning;
  }

  private formatDate(d: Date): string {
    return d.toISOString().slice(0, 10).replace(/-/g, '');
  }

  private formatDateTime(d: Date): string {
    return d.toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, 'Z');
  }
}
