# Y Transport Protocol (YTP/0.1)

## Overview

Y Transport is an **encrypted delay-tolerant operation transport** over unreliable append-only provider logs.

It is NOT a VPN, NOT a proxy, and NOT a bypass tool. It is a transport protocol that treats any message/chat API as an unreliable mailbox, and builds reliable, encrypted, multiplexed communication on top.

## Transport Assumptions

- Provider supports `append(message)` — add a message to a mailbox
- Provider supports `scan(cursor)` — read messages since a cursor
- Provider may **duplicate**, **delay**, **reorder**, **truncate**, **rate-limit**, or **delete** messages
- Provider is **untrusted** — all payloads are end-to-end encrypted

## Architecture

```
Intent Router      → what the user wants (open URL, DNS resolve, TCP connect)
Operation Queue    → typed operations (OpenStream, StreamData, ResolveDns, …)
Bundle Builder     → accumulate operations over a window, compress, encrypt
Crypto Box         → XChaCha20-Poly1305 or AES-256-GCM
Provider Scheduler → choose provider, respect budget, handle retransmit
Provider Adapter   → Telegram / VK / OK / File / Memory
```

### Data Flow (Outbound)

```
Local Proxy (SOCKS5/HTTP)
  → Intent Router (parse request type)
  → Operation Queue (OpenStream, StreamData, etc.)
  → Bundle Builder (accumulate over 500–3000ms window)
  → Crypto Box (compress → encrypt → MAC)
  → Provider Scheduler (select provider, check budget)
  → Provider.append() (send as chat message)
```

### Data Flow (Inbound)

```
Provider.scan() (poll for new messages)
  → Dedup (skip already-seen envelopes)
  → Crypto Open (decrypt → decompress)
  → Bundle Parser (extract operations)
  → Operation Dispatcher (handle each operation type)
  → Stream/DNS/HTTP handlers (forward to local sockets)
```

## Envelope Format

Every message on the provider log is an **Envelope** — the outer wrapper visible to the provider.

### Wire Format (Compact)

```
YT1.<session>.<epoch>.<dir>.<seq>.<pri>.<kind>.<nonce>.<ciphertext>.<tag>.<expires>
```

| Field      | Description                              |
|------------|------------------------------------------|
| YT1        | Magic + version                          |
| session    | Session identifier (16-char hex)         |
| epoch      | Epoch number (incremented on key rotate) |
| dir        | A = a2b, B = b2a                         |
| seq        | Monotonic sequence within epoch+direction|
| pri        | Priority 0–4                             |
| kind       | D=bundle, K=ack, C=checkpoint, U=key-update |
| nonce      | 24-byte base64url nonce                  |
| ciphertext | Encrypted bundle base64url               |
| tag        | 16-byte AEAD tag base64url               |
| expires    | Deadline epoch-ms                        |

## Operation Types

| Type               | Code | Description                          |
|--------------------|------|--------------------------------------|
| open-stream        |      | Open a new stream to target          |
| stream-data        |      | Data chunk on an open stream         |
| close-stream       |      | Close a stream                       |
| half-close-stream  |      | Half-close (read or write only)      |
| resolve-dns        |      | DNS query                            |
| dns-result         |      | DNS response                         |
| http-request-hint  |      | HTTP metadata for optimization       |
| ack-state          |      | Acknowledgement state                |
| provider-health    |      | Provider health report               |
| checkpoint         |      | Logical GC marker                    |
| key-update         |      | Key rotation trigger                 |

## Bundle

A Bundle collects multiple Operations into a single Envelope:

```json
{
  "bundleId": "hash-of-compressed-payload",
  "operations": [ ... ],
  "createdAt": 1780412345123,
  "deadline": 1780412375123
}
```

### Bundle Window

Operations are accumulated over 500–3000ms before being sent as a bundle. This reduces message count and improves compression.

## Handshake

```
A → B:  HELLO    { nodeId, protocolVersion, ephemeralPubKeyX, providersSupported }
B → A:  HELLO_ACK { nodeId, ephemeralPubKeyX, chosenCipher, maxFrameBytes }
A → B:  KEY_CONFIRM { confirmHmac }
B → A:  READY    { accepted: true }
```

After READY, both sides derive session keys via HKDF and begin exchanging bundles.

## Encryption

- Key exchange: X25519
- Signing: Ed25519
- Symmetric cipher: XChaCha20-Poly1305 (preferred) or AES-256-GCM
- Key derivation: HKDF-SHA256
- Key rotation: every 10,000 bundles or 24 hours

## Reliability

### Sequence Numbers

Each envelope has a monotonically increasing `seq` number within `(session, epoch, direction)`.

### Acknowledgement

Receivers periodically send AckState:

```json
{
  "receivedUpTo": 1042,
  "missing": [1007, 1013, 1014]
}
```

### Retransmission

- Base RTO: 5 seconds (fast provider) to 120 seconds (slow provider)
- Exponential backoff with cap
- Maximum 5 retries per envelope

### Checkpoint

Logical garbage-collection marker. After a checkpoint is acknowledged, all earlier envelopes can be safely ignored.

## Priority

| Level | Name        | Use Cases                         |
|-------|-------------|-----------------------------------|
| 0     | Control     | ACK, CLOSE, KEY_UPDATE            |
| 1     | Interactive | SSH terminal, small HTTP          |
| 2     | Normal      | Regular stream data               |
| 3     | Bulk        | File transfer, large responses    |
| 4     | Maintenance | Checkpoint, stats                 |

## Provider Quorum

Critical control messages (KEY_UPDATE, CLOSE, CHECKPOINT) may be sent via multiple providers simultaneously for resilience.

## Store-and-Forward

Y Transport is inherently asynchronous. Messages have a `deadline` field; if the deadline passes, the remote node must not process the request.

## Sessions and Epochs

- **Session**: long-lived communication channel between two nodes
- **Epoch**: a period within a session with a specific set of encryption keys
- Epochs change on: key rotation, provider change, token rotation, manual action

## Provider Interface

```typescript
interface Provider {
  append(frame: OutboundFrame): Promise<AppendResult>
  scan(cursor: ProviderCursor): Promise<{ messages: ProviderMessage[]; nextCursor: ProviderCursor }>
  capabilities(): ProviderCapabilities
  rateHint(): RateHint
}
```

## Security Model

### Threat Model

**Provider is untrusted.** The provider can:
- Read envelope metadata (who, when, how often, approximate size)
- Delay, reorder, duplicate, or drop messages
- Rate-limit or block messages
- Correlate traffic patterns

**Provider CANNOT:**
- Read operation payloads (end-to-end encrypted)
- Forge messages (AEAD authentication)
- Replay old messages (seq + epoch + nonce validation)

### Metadata Leakage

The following metadata is visible to the provider:
- Which bot/account is sending/receiving
- Message frequency and timing
- Approximate message sizes
- Total volume of communication

This is an accepted trade-off. Y Transport is NOT designed to hide communication metadata from the provider.

## MVP Scope

- SOCKS5 TCP CONNECT only
- DNS resolution
- HTTP CONNECT tunneling
- No UDP, no QUIC, no raw IP
- MemoryProvider + FileProvider + TelegramProvider
- AES-256-GCM (XChaCha20-Poly1305 with libsodium in production)
- SQLite persistence
