# Y Transport — Threat Model

## 1. Introduction

This document describes the threat model for Y Transport (YTP), an encrypted delay-tolerant operation transport over unreliable append-only provider logs.

## 2. Assets

| Asset                     | Location          | Protection Level |
|---------------------------|-------------------|------------------|
| Node identity keys        | OS credential vault | Critical         |
| Provider tokens (Telegram, VK, OK) | OS credential vault | Critical |
| Session encryption keys   | Memory / encrypted DB | High           |
| Operation payloads        | Encrypted in transit | High           |
| Provider cursors / state  | Local SQLite      | Medium           |
| Provider metadata (who, when, size) | Visible to provider | Unavoidable |

## 3. Adversaries

### 3.1 Passive Provider

**Capabilities:**
- Read all message metadata (sender, timestamp, size)
- Observe communication patterns (frequency, volume, timing)
- Correlate messages across chats/users

**Mitigations:**
- End-to-end encryption of all payloads
- AEAD authentication prevents forgery
- Metadata leakage is ACCEPTED and documented

### 3.2 Active Provider

**Capabilities:**
- All of Passive Provider
- Delay, reorder, duplicate, or drop messages
- Inject forged messages (will fail AEAD)
- Rate-limit or block communication

**Mitigations:**
- Sequence numbers + epoch prevent replay
- AEAD authentication detects forged/injected messages
- Protocol is delay-tolerant by design
- Multi-provider failover mitigates single-provider blocking

### 3.3 Passive Network Observer

**Capabilities:**
- Observe HTTPS traffic to provider APIs
- See which API endpoints are called, when, and how often

**Mitigations:**
- All provider APIs use HTTPS (TLS)
- Payload is encrypted before reaching the API

### 3.4 Compromised Peer

**Capabilities:**
- Decrypt all received messages
- Send arbitrary operations
- Impersonate the node

**Mitigations:**
- Key rotation limits exposure window
- Revocation requires out-of-band verification
- No forward secrecy in current design (future: double ratchet)

### 3.5 Local Attacker

**Capabilities:**
- Read local files (if not encrypted)
- Access local proxy ports (127.0.0.1:1080, :8080)

**Mitigations:**
- Tokens stored in OS credential vault, not plain files
- Local proxy binds to loopback only
- SQLite can be encrypted with SQLCipher (future)

## 4. What Y Transport Does NOT Protect Against

- **Metadata analysis by providers**: who communicates, when, how much
- **Traffic correlation attacks**: linking local activity to remote activity via timing
- **Provider account compromise**: if the bot token is stolen, the attacker can read/send messages (but cannot decrypt payloads without session keys)
- **Endpoint compromise**: if the user's machine is compromised, all keys and data are at risk
- **Denial of service**: providers can always rate-limit or block

## 5. Design Principles

1. **Never trust the provider** — all payloads encrypted end-to-end
2. **Respect provider ToS** — built-in budget, rate limiting, conservative defaults
3. **Delay-tolerant** — works even with high latency, intermittent connectivity
4. **Degrade gracefully** — multi-provider failover, no hard dependency on any single provider
5. **No obfuscation** — we don't try to hide what we're doing from the provider; the protocol is visible as structured text messages
6. **Tokens are local-only** — never synced, shared, or stored in plaintext files

## 6. Key Management

- **Identity keys**: Generated once, stored in OS credential vault
- **Ephemeral keys**: Generated per-handshake, used for DH key exchange
- **Session keys**: Derived via HKDF from shared secret, rotated regularly
- **Key rotation triggers**: time-based, bundle-count-based, provider-change-based, manual

## 7. Reporting Security Issues

If you discover a security vulnerability in Y Transport, please report it responsibly via a private channel (e.g., encrypted email to the maintainers). Do not file public issues for security vulnerabilities.
