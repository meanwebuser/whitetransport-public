/**
 * YTP Provider — abstract interface for any append-only message transport.
 *
 * A provider is an unreliable mailbox that supports:
 *   - append:  add a message to a mailbox
 *   - scan:    read messages since a cursor
 *   - capabilities: describe limits
 *
 * Providers may duplicate, delay, reorder, or truncate messages.
 * The protocol handles all of these above the provider layer.
 */

// ── Cursor ─────────────────────────────────────────────────────────────
export type ProviderCursor = string | number | null;

// ── Message on the provider ────────────────────────────────────────────
export interface ProviderMessage {
  id: string;           // provider-specific message ID
  timestamp: number;    // server timestamp if available, else local
  text: string;         // raw text content
  fromSelf: boolean;    // true if sent by our own bot
}

// ── Outbound frame to send ─────────────────────────────────────────────
export interface OutboundFrame {
  text: string;         // wire-format envelope string
  priority: number;     // 0–4
  deadline: number;     // epoch-ms
}

// ── Append result ──────────────────────────────────────────────────────
export interface AppendResult {
  messageId: string;
  timestamp: number;
}

// ── Provider capabilities ──────────────────────────────────────────────
export interface ProviderCapabilities {
  maxTextBytes: number;
  supportsAttachments: boolean;
  supportsEdit: boolean;
  supportsDelete: boolean;
  supportsMessageIds: boolean;
  supportsServerTimestamp: boolean;
  minSafeSendIntervalMs: number;
  recommendedPollIntervalMs: number;
}

// ── Rate hint ──────────────────────────────────────────────────────────
export interface RateHint {
  minIntervalMs: number;
  burst: number;
  mode: 'conservative' | 'moderate' | 'aggressive';
}

// ── Provider interface ─────────────────────────────────────────────────
export interface Provider {
  readonly id: string;

  /** Append a message to the provider */
  append(frame: OutboundFrame): Promise<AppendResult>;

  /** Scan for new messages since cursor */
  scan(cursor: ProviderCursor): Promise<{
    messages: ProviderMessage[];
    nextCursor: ProviderCursor;
  }>;

  /** Describe this provider's limits */
  capabilities(): ProviderCapabilities;

  /** Rate hint for the scheduler */
  rateHint(): RateHint;

  /** Start the provider (connect, auth, etc.) */
  start?(): Promise<void>;

  /** Graceful shutdown */
  stop?(): Promise<void>;
}
