/**
 * YTP Node — the main orchestrator for a Y Transport node.
 *
 * Ties together providers, scheduler, proxy, crypto, and storage.
 */

import type { Provider } from '../providers/provider';
import type { FrameStore } from '../storage/store';
import type { SessionKeys } from '../crypto/handshake';
import type { NodeIdentity } from '../crypto/identity';
import { Socks5Server, Socks5Request } from '../proxy/socks5';
import { HttpConnectServer } from '../proxy/http-connect';
import { DnsCache } from '../proxy/dns';
import { PriorityQueue } from '../scheduler/priority-queue';
import { RetransmitManager } from '../scheduler/retransmit';
import { ProviderSelector } from '../scheduler/provider-selection';
import { createBudgetUsage, type TransportBudget, type BudgetUsage } from '../scheduler/budget';
import { BundleBuilder } from '../protocol/bundle';
import { envelopeToWire, wireToEnvelope, type Envelope } from '../protocol/envelope';
import { encryptBundle, decryptBundle } from '../crypto/box';

export interface YTransportNodeConfig {
  identity: NodeIdentity;
  providers: Provider[];
  store: FrameStore;
  budget?: TransportBudget;
  socks5Port?: number;
  httpConnectPort?: number;
}

export class YTransportNode {
  private providers: Provider[];
  private store: FrameStore;
  private identity: NodeIdentity;
  private budget: TransportBudget;
  private budgetUsages: Map<string, { usage: BudgetUsage; budget: TransportBudget }>;

  private socks5: Socks5Server;
  private httpConnect: HttpConnectServer;
  private dnsCache: DnsCache;

  private outbox: PriorityQueue;
  private retransmit: RetransmitManager;
  private selector: ProviderSelector;

  private sessionKeys: Map<string, SessionKeys> = new Map();
  private streams: Map<number, { target: string; status: string }> = new Map();
  private nextStreamId = 1;

  private running = false;

  constructor(config: YTransportNodeConfig) {
    this.identity = config.identity;
    this.providers = config.providers;
    this.store = config.store;
    this.budget = config.budget ?? { maxMessagesPerHour: 30, maxBytesPerDay: 500_000, minSendIntervalMs: 2000, maxPollsPerHour: 120 };
    this.budgetUsages = new Map();

    this.outbox = new PriorityQueue();
    this.retransmit = new RetransmitManager();
    this.selector = new ProviderSelector();
    this.dnsCache = new DnsCache();

    for (const p of this.providers) {
      this.budgetUsages.set(p.id, { usage: createBudgetUsage(), budget: this.budget });
    }

    // SOCKS5 proxy — tunnel CONNECT requests
    this.socks5 = new Socks5Server((req: Socks5Request, socket) => {
      this.handleSocks5Connect(req, socket);
    });

    // HTTP CONNECT proxy
    this.httpConnect = new HttpConnectServer((host, port, socket) => {
      this.handleHttpConnect(host, port, socket);
    });
  }

  async start(): Promise<void> {
    console.log(`[YTransport] Starting node ${this.identity.nodeId}...`);

    // Open storage
    await this.store.open('ytp-data.db');

    // Start providers
    for (const provider of this.providers) {
      if (provider.start) {
        await provider.start();
        console.log(`[YTransport] Provider ${provider.id} started`);
      }
    }

    // Start local proxy servers
    await this.socks5.listen('127.0.0.1', 1080);
    await this.httpConnect.listen('127.0.0.1', 8080);

    this.running = true;
    console.log('[YTransport] Node started. SOCKS5 on :1080, HTTP CONNECT on :8080');

    // Start main loop
    this.mainLoop();
  }

  async stop(): Promise<void> {
    this.running = false;
    await this.socks5.close();
    await this.httpConnect.close();

    for (const provider of this.providers) {
      if (provider.stop) {
        await provider.stop();
      }
    }

    await this.store.close();
    console.log('[YTransport] Node stopped');
  }

  // ── Main loop ────────────────────────────────────────────────────────

  private async mainLoop(): Promise<void> {
    while (this.running) {
      try {
        await this.pollProviders();
        await this.flushOutbox();
        await this.handleRetransmits();
        this.outbox.purgeExpired();
        this.retransmit.purgeExpired();
      } catch (err) {
        console.error('[YTransport] Main loop error:', err);
      }

      await this.sleep(1000); // 1-second tick
    }
  }

  // ── Provider polling ─────────────────────────────────────────────────

  private async pollProviders(): Promise<void> {
    for (const provider of this.providers) {
      const budgetUsage = this.budgetUsages.get(provider.id)!.usage;
      // Check poll budget
      if (budgetUsage.pollsThisHour >= this.budget.maxPollsPerHour) continue;

      try {
        const cursor = await this.store.loadCursor(provider.id);
        const result = await provider.scan(cursor);

        for (const msg of result.messages) {
          if (msg.fromSelf) continue; // skip our own messages

          // Try to parse as YTP envelope
          const envelope = wireToEnvelope(msg.text);
          if (envelope) {
            await this.handleInboundEnvelope(envelope, provider);
          }
        }

        await this.store.saveCursor(provider.id, result.nextCursor as string);
        budgetUsage.pollsThisHour++;
      } catch (err) {
        console.error(`[YTransport] Poll error for ${provider.id}:`, err);
        this.selector.recordFailure(provider.id);
      }
    }
  }

  // ── Inbound envelope processing ──────────────────────────────────────

  private async handleInboundEnvelope(envelope: Envelope, _provider: Provider): Promise<void> {
    // Find session keys
    const keys = this.sessionKeys.get(envelope.sessionId);
    if (!keys) {
      console.warn(`[YTransport] No session keys for ${envelope.sessionId}`);
      return;
    }

    // Decrypt
    const bundle = decryptBundle(keys.decryptKey, {
      nonce: envelope.nonce,
      ciphertext: envelope.ciphertext,
      tag: envelope.tag,
    });

    if (!bundle) {
      console.warn(`[YTransport] Decryption failed for envelope seq=${envelope.seq}`);
      return;
    }

    // Store in inbox
    await this.store.enqueueInbox(
      envelope.sessionId,
      envelope.epochId,
      envelope.direction,
      envelope.seq,
      envelopeToWire(envelope),
    );

    // Process operations
    for (const op of bundle.operations) {
      await this.handleOperation(op);
    }

    // Send ACK
    // (simplified: ack the envelope seq)
  }

  // ── Operation handling ───────────────────────────────────────────────

  private async handleOperation(op: any): Promise<void> {
    switch (op.op) {
      case 'open-stream':
        console.log(`[YTransport] OPEN stream ${op.streamId} -> ${op.target}`);
        this.streams.set(op.streamId, { target: op.target, status: 'open' });
        // In real implementation: open TCP socket to target
        break;

      case 'stream-data':
        // Forward data to the associated local socket
        console.log(`[YTransport] DATA on stream ${op.streamId}, ${op.payload.length} chars`);
        break;

      case 'close-stream':
        console.log(`[YTransport] CLOSE stream ${op.streamId}`);
        const stream = this.streams.get(op.streamId);
        if (stream) stream.status = 'closed';
        break;

      case 'dns-result':
        this.dnsCache.set({
          name: op.answers?.[0] ?? '',
          qtype: 'A',
          answers: op.answers ?? [],
          ttl: op.ttl ?? 300,
          resolvedAt: Date.now(),
        });
        break;

      case 'ack-state':
        this.retransmit.ack(op.receivedUpTo, op.missing);
        break;

      case 'checkpoint':
        console.log(`[YTransport] CHECKPOINT epoch=${op.epoch} up_to=${op.receivedUpTo}`);
        break;

      default:
        console.log(`[YTransport] Unknown operation: ${op.op}`);
    }
  }

  // ── Outbox flush ─────────────────────────────────────────────────────

  private async flushOutbox(): Promise<void> {
    while (this.outbox.size > 0) {
      const entry = this.outbox.dequeue();
      if (!entry) break;

      if (entry.frame.deadline < Date.now()) continue; // expired

      const provider = this.selector.select(
        this.providers,
        entry.frame,
        this.budgetUsages,
      );

      if (!provider) {
        // No provider available, re-queue
        this.outbox.requeue(entry);
        break;
      }

      const budgetUsage = this.budgetUsages.get(provider.id)!.usage;

      try {
        const result = await provider.append(entry.frame);
        this.selector.recordSuccess(provider.id, Date.now() - entry.enqueuedAt);
        budgetUsage.messagesSentThisHour++;
        budgetUsage.bytesSentToday += entry.frame.text.length;
        budgetUsage.lastSendAt = Date.now();

        console.log(`[YTransport] Sent via ${provider.id}: msg_id=${result.messageId}`);
      } catch (err) {
        console.error(`[YTransport] Send error via ${provider.id}:`, err);
        this.selector.recordFailure(provider.id);
        this.outbox.requeue(entry);
      }
    }
  }

  // ── Retransmits ──────────────────────────────────────────────────────

  private async handleRetransmits(): Promise<void> {
    const retransmits = this.retransmit.getRetransmits();
    for (const entry of retransmits) {
      this.outbox.enqueue(entry.frame);
    }
  }

  // ── SOCKS5 / HTTP CONNECT handlers ───────────────────────────────────

  private handleSocks5Connect(req: Socks5Request, socket: any): void {
    console.log(`[YTransport] SOCKS5 CONNECT: ${req.targetHost}:${req.targetPort}`);

    // Send success reply
    Socks5Server.sendSuccessReply(socket);

    // Create a YTP OPEN stream operation
    const streamId = this.nextStreamId++;
    this.streams.set(streamId, { target: `${req.targetHost}:${req.targetPort}`, status: 'open' });

    // In real implementation:
    // 1. Build OpenStreamOp
    // 2. Add to BundleBuilder
    // 3. Encrypt and enqueue
    // 4. Forward socket data as StreamDataOp
    // 5. Forward remote data back to socket

    console.log(`[YTransport] Opened stream ${streamId} for ${req.targetHost}:${req.targetPort}`);
  }

  private handleHttpConnect(host: string, port: number, socket: any): void {
    console.log(`[YTransport] HTTP CONNECT: ${host}:${port}`);

    const streamId = this.nextStreamId++;
    this.streams.set(streamId, { target: `${host}:${port}`, status: 'open' });

    // Same flow as SOCKS5
  }

  // ── Helpers ──────────────────────────────────────────────────────────

  private sleep(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms));
  }
}
