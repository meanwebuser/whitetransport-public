/**
 * YTP FileProvider — append-only log file as a provider.
 *
 * Milestone 2: two nodes communicate by writing/reading a shared
 * file (e.g. on a network drive or synced folder). Debugs the
 * append-only model without any chat API.
 */

import { appendFile, readFile, stat } from 'fs/promises';
import { existsSync } from 'fs';
import type { Provider, ProviderCursor, ProviderMessage, OutboundFrame, AppendResult, ProviderCapabilities, RateHint } from './provider';

export class FileProvider implements Provider {
  readonly id = 'file';

  private logPath: string;
  private nextSeq = 0;

  constructor(logPath: string) {
    this.logPath = logPath;
  }

  async start(): Promise<void> {
    if (!existsSync(this.logPath)) {
      await appendFile(this.logPath, '');
    }
    // Count existing lines to set nextSeq
    const content = await readFile(this.logPath, 'utf-8');
    const lines = content.split('\n').filter(l => l.trim().length > 0);
    this.nextSeq = lines.length;
  }

  async stop(): Promise<void> {
    // no-op
  }

  async append(frame: OutboundFrame): Promise<AppendResult> {
    const seq = this.nextSeq++;
    const line = JSON.stringify({ seq, text: frame.text, ts: Date.now() }) + '\n';
    await appendFile(this.logPath, line, 'utf-8');
    return { messageId: String(seq), timestamp: Date.now() };
  }

  async scan(cursor: ProviderCursor): Promise<{
    messages: ProviderMessage[];
    nextCursor: ProviderCursor;
  }> {
    const offset = cursor ? Number(cursor) : 0;
    const content = await readFile(this.logPath, 'utf-8');
    const lines = content.split('\n').filter(l => l.trim().length > 0);

    const messages: ProviderMessage[] = [];
    for (let i = offset; i < lines.length; i++) {
      try {
        const parsed = JSON.parse(lines[i]);
        messages.push({
          id: String(parsed.seq),
          timestamp: parsed.ts,
          text: parsed.text,
          fromSelf: false,
        });
      } catch {
        // skip malformed lines
      }
    }

    return {
      messages,
      nextCursor: String(lines.length),
    };
  }

  capabilities(): ProviderCapabilities {
    return {
      maxTextBytes: 1_000_000,
      supportsAttachments: false,
      supportsEdit: false,
      supportsDelete: false,
      supportsMessageIds: true,
      supportsServerTimestamp: true,
      minSafeSendIntervalMs: 100,
      recommendedPollIntervalMs: 1000,
    };
  }

  rateHint(): RateHint {
    return {
      minIntervalMs: 100,
      burst: 10,
      mode: 'moderate',
    };
  }
}
