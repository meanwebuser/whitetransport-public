/**
 * YTP FrameStore — persistent storage for sessions, frames, and state.
 *
 * Uses better-sqlite3 in production; this skeleton provides the
 * interface and a stub implementation.
 */

import type { Envelope } from '../protocol/envelope';

export interface StoredOutboxEntry {
  seq: number;
  sessionId: string;
  epochId: number;
  direction: string;
  envelopeSeq: number;
  wireText: string;
  priority: number;
  sentAt: number | null;
  ackedAt: number | null;
  retries: number;
  deadline: number;
}

export interface FrameStore {
  open(dbPath: string): Promise<void>;
  close(): Promise<void>;

  // Sessions
  createSession(sessionId: string, peerNodeId: string, myNodeId: string): Promise<void>;
  getActiveSessions(): Promise<string[]>;

  // Outbox
  enqueueOutbox(entry: Omit<StoredOutboxEntry, 'seq'>): Promise<number>;
  getUnsentOutbox(limit: number): Promise<StoredOutboxEntry[]>;
  markSent(seq: number): Promise<void>;
  markAcked(seq: number): Promise<void>;

  // Inbox
  enqueueInbox(sessionId: string, epochId: number, direction: string, envelopeSeq: number, wireText: string): Promise<void>;
  getUnprocessedInbox(limit: number): Promise<Array<{ seq: number; wireText: string }>>;
  markProcessed(seq: number): Promise<void>;

  // Provider cursors
  saveCursor(providerId: string, cursor: string): Promise<void>;
  loadCursor(providerId: string): Promise<string | null>;

  // ACKs
  saveAckState(sessionId: string, direction: string, receivedUpTo: number, missing: number[]): Promise<void>;
  getAckState(sessionId: string, direction: string): Promise<{ receivedUpTo: number; missing: number[] } | null>;

  // Peers
  savePeer(nodeId: string, publicKeyEd: string, publicKeyX: string, inviteCode?: string): Promise<void>;
  getPeer(nodeId: string): Promise<{ publicKeyEd: string; publicKeyX: string } | null>;
}

/**
 * In-memory stub implementation for development/testing.
 * Replace with SQLiteStore for production.
 */
export class MemoryFrameStore implements FrameStore {
  private outbox: StoredOutboxEntry[] = [];
  private inbox: Array<{ seq: number; sessionId: string; epochId: number; direction: string; envelopeSeq: number; wireText: string; processed: boolean }> = [];
  private sessions: Map<string, { peerNodeId: string; myNodeId: string }> = new Map();
  private cursors: Map<string, string> = new Map();
  private acks: Map<string, { receivedUpTo: number; missing: number[] }> = new Map();
  private peers: Map<string, { publicKeyEd: string; publicKeyX: string; inviteCode?: string }> = new Map();
  private nextOutboxSeq = 1;
  private nextInboxSeq = 1;

  async open(_dbPath: string): Promise<void> { /* no-op */ }
  async close(): Promise<void> { /* no-op */ }

  async createSession(sessionId: string, peerNodeId: string, myNodeId: string): Promise<void> {
    this.sessions.set(sessionId, { peerNodeId, myNodeId });
  }

  async getActiveSessions(): Promise<string[]> {
    return [...this.sessions.keys()];
  }

  async enqueueOutbox(entry: Omit<StoredOutboxEntry, 'seq'>): Promise<number> {
    const seq = this.nextOutboxSeq++;
    this.outbox.push({ ...entry, seq });
    return seq;
  }

  async getUnsentOutbox(limit: number): Promise<StoredOutboxEntry[]> {
    return this.outbox.filter(e => e.sentAt === null).slice(0, limit);
  }

  async markSent(seq: number): Promise<void> {
    const entry = this.outbox.find(e => e.seq === seq);
    if (entry) entry.sentAt = Date.now();
  }

  async markAcked(seq: number): Promise<void> {
    const entry = this.outbox.find(e => e.seq === seq);
    if (entry) entry.ackedAt = Date.now();
  }

  async enqueueInbox(sessionId: string, epochId: number, direction: string, envelopeSeq: number, wireText: string): Promise<void> {
    this.inbox.push({ seq: this.nextInboxSeq++, sessionId, epochId, direction, envelopeSeq, wireText, processed: false });
  }

  async getUnprocessedInbox(limit: number): Promise<Array<{ seq: number; wireText: string }>> {
    return this.inbox.filter(e => !e.processed).slice(0, limit).map(e => ({ seq: e.seq, wireText: e.wireText }));
  }

  async markProcessed(seq: number): Promise<void> {
    const entry = this.inbox.find(e => e.seq === seq);
    if (entry) entry.processed = true;
  }

  async saveCursor(providerId: string, cursor: string): Promise<void> {
    this.cursors.set(providerId, cursor);
  }

  async loadCursor(providerId: string): Promise<string | null> {
    return this.cursors.get(providerId) ?? null;
  }

  async saveAckState(sessionId: string, direction: string, receivedUpTo: number, missing: number[]): Promise<void> {
    this.acks.set(`${sessionId}:${direction}`, { receivedUpTo, missing });
  }

  async getAckState(sessionId: string, direction: string): Promise<{ receivedUpTo: number; missing: number[] } | null> {
    return this.acks.get(`${sessionId}:${direction}`) ?? null;
  }

  async savePeer(nodeId: string, publicKeyEd: string, publicKeyX: string, inviteCode?: string): Promise<void> {
    this.peers.set(nodeId, { publicKeyEd, publicKeyX, inviteCode });
  }

  async getPeer(nodeId: string): Promise<{ publicKeyEd: string; publicKeyX: string } | null> {
    return this.peers.get(nodeId) ?? null;
  }
}
