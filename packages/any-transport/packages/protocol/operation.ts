/**
 * YTP Operation — the atomic unit of work inside a Bundle.
 *
 * Operations represent *intents*: open a stream, send data, resolve DNS,
 * close a stream, etc.  They are NOT raw bytes — they are typed, structured
 * payloads that the remote node interprets.
 */

// ── Operation discriminant ─────────────────────────────────────────────
export type OperationType =
  | 'open-stream'
  | 'stream-data'
  | 'close-stream'
  | 'half-close-stream'
  | 'resolve-dns'
  | 'dns-result'
  | 'http-request-hint'
  | 'ack-state'
  | 'provider-health'
  | 'checkpoint'
  | 'key-update';

// ── Base ───────────────────────────────────────────────────────────────
interface OpBase {
  op: OperationType;
  id: string; // unique per-operation id (uuid)
}

// ── Stream operations ──────────────────────────────────────────────────
export interface OpenStreamOp extends OpBase {
  op: 'open-stream';
  streamId: number;
  target: string; // e.g. "example.com:443"
  protocol: 'tcp' | 'udp'; // MVP: tcp only
}

export interface StreamDataOp extends OpBase {
  op: 'stream-data';
  streamId: number;
  seq: number; // per-stream sequence
  payload: string; // base64url
}

export interface CloseStreamOp extends OpBase {
  op: 'close-stream';
  streamId: number;
  reason?: string;
}

export interface HalfCloseStreamOp extends OpBase {
  op: 'half-close-stream';
  streamId: number;
  direction: 'read' | 'write';
}

// ── DNS operations ─────────────────────────────────────────────────────
export interface ResolveDnsOp extends OpBase {
  op: 'resolve-dns';
  name: string;
  qtype: 'A' | 'AAAA' | 'TXT' | 'MX';
}

export interface DnsResultOp extends OpBase {
  op: 'dns-result';
  queryId: string; // references ResolveDnsOp.id
  answers: string[];
  ttl: number;
}

// ── HTTP-aware hint ────────────────────────────────────────────────────
export interface HttpRequestHintOp extends OpBase {
  op: 'http-request-hint';
  streamId: number;
  method: string;
  host: string;
  path: string;
  contentType?: string;
  contentLength?: number;
}

// ── Ack ────────────────────────────────────────────────────────────────
export interface AckStateOp extends OpBase {
  op: 'ack-state';
  direction: 'a2b' | 'b2a';
  receivedUpTo: number;
  missing: number[];  // seq numbers missing
  bitmap?: string;    // optional compact bitmap
}

// ── Provider health ────────────────────────────────────────────────────
export interface ProviderHealthOp extends OpBase {
  op: 'provider-health';
  providerId: string;
  latencyMs: number;
  lossEstimate: number;
  budgetRemaining: number;
}

// ── Checkpoint ─────────────────────────────────────────────────────────
export interface CheckpointOp extends OpBase {
  op: 'checkpoint';
  epoch: number;
  receivedUpTo: number;
  stateHash: string;
}

// ── Key update ─────────────────────────────────────────────────────────
export interface KeyUpdateOp extends OpBase {
  op: 'key-update';
  newEpochId: number;
  ephemeralPubKey: string; // base64url X25519 public key
}

// ── Union ──────────────────────────────────────────────────────────────
export type Operation =
  | OpenStreamOp
  | StreamDataOp
  | CloseStreamOp
  | HalfCloseStreamOp
  | ResolveDnsOp
  | DnsResultOp
  | HttpRequestHintOp
  | AckStateOp
  | ProviderHealthOp
  | CheckpointOp
  | KeyUpdateOp;
