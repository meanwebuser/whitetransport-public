# Y Transport — Provider Documentation

## Overview

Providers are the transport layer for Y Transport. Each provider wraps a message/chat API into a unified `append + scan` interface.

## Provider Interface

```typescript
interface Provider {
  readonly id: string;

  append(frame: OutboundFrame): Promise<AppendResult>;
  scan(cursor: ProviderCursor): Promise<{
    messages: ProviderMessage[];
    nextCursor: ProviderCursor;
  }>;
  capabilities(): ProviderCapabilities;
  rateHint(): RateHint;
  start?(): Promise<void>;
  stop?(): Promise<void>;
}
```

## Built-in Providers

### MemoryProvider

**ID**: `memory`

In-memory provider for local loopback testing. No external dependencies.

- **Max payload**: 64 KB
- **Latency**: ~0 ms
- **Poll interval**: 50 ms
- **Use case**: Milestone 1 — verify core protocol works

### FileProvider

**ID**: `file`

Append-only log file provider. Two nodes communicate by writing/reading a shared file.

- **Max payload**: 1 MB
- **Latency**: Depends on file system
- **Poll interval**: 1000 ms
- **Use case**: Milestone 2 — debug append-only model without chat APIs

**Configuration**:
```typescript
new FileProvider('/path/to/shared/log.txt')
```

### TelegramProvider

**ID**: `telegram`

Uses Telegram Bot API for sending and receiving messages.

- **Max payload**: 2048 bytes (conservative text-safe mode)
- **Latency**: 1–5 seconds
- **Poll interval**: 3000 ms (conservative)
- **Min send interval**: 1500 ms
- **Use case**: Milestone 3 — first real provider

**Configuration**:
```typescript
new TelegramProvider({
  botToken: process.env.YTP_TELEGRAM_BOT_TOKEN,  // from OS vault
  chatId: process.env.YTP_TELEGRAM_CHAT_ID,
})
```

**Token Safety**:
- Bot tokens MUST be stored in OS credential vault (Keychain / Credential Manager / Secret Service)
- NEVER hardcode tokens in source code
- NEVER commit tokens to version control
- NEVER sync tokens between devices

**Rate Limits**:
- Telegram allows ~30 messages/second to one chat, but Y Transport uses conservative defaults
- Default budget: 30 messages/hour, 500 KB/day
- User can adjust budget in settings

## Planned Providers

### VKProvider

**ID**: `vk`

VK Messenger Bot API.

- **Status**: Planned (Milestone 5)
- **Requires**: VK Community token, valid app registration
- **Rate limits**: TBD

### OKProvider

**ID**: `ok`

OK.ru Graph API for chat messages.

- **Status**: Planned (Milestone 5)
- **Requires**: OK access token, registered application
- **Rate limits**: TBD
- **Notes**: Messages are returned in reverse chronological order; cursor is timestamp-based

### DiscordProvider

**ID**: `discord`

Discord Bot API.

- **Status**: Future
- **Notes**: Rich text may complicate wire format; may need embed or code block

### MatrixProvider

**ID**: `matrix`

Matrix Client-Server API.

- **Status**: Future
- **Notes**: Good fit — Matrix is designed for messaging and has well-documented API

### EmailProvider

**ID**: `email`

Email (SMTP/IMAP) as a provider.

- **Status**: Future
- **Notes**: Extremely high latency but very resilient; good for dormant mode

## Provider Capabilities

Each provider reports its capabilities:

```typescript
interface ProviderCapabilities {
  maxTextBytes: number;
  supportsAttachments: boolean;
  supportsEdit: boolean;
  supportsDelete: boolean;
  supportsMessageIds: boolean;
  supportsServerTimestamp: boolean;
  minSafeSendIntervalMs: number;
  recommendedPollIntervalMs: number;
}
```

Y Transport does NOT require:
- Message editing or deletion
- Stable message IDs
- Server timestamps
- Attachments

It only requires `append` and `scan`, and handles all other concerns at the protocol level.

## Adding a Custom Provider

1. Implement the `Provider` interface
2. Add the provider to the `YTransportNode` config
3. Set budget via `TransportBudget`
4. Store tokens in OS credential vault

Example:

```typescript
import { Provider } from '@y-transport/providers';

class MyCustomProvider implements Provider {
  readonly id = 'custom';

  async append(frame) { /* ... */ }
  async scan(cursor) { /* ... */ }
  capabilities() { /* ... */ }
  rateHint() { /* ... */ }
}

const node = new YTransportNode({
  providers: [new MyCustomProvider()],
  // ...
});
```

## Budget Management

Every provider has an associated budget that limits usage:

```typescript
interface TransportBudget {
  maxMessagesPerHour: number;
  maxBytesPerDay: number;
  minSendIntervalMs: number;
  maxPollsPerHour: number;
  quietHours?: {
    from: string;  // "23:00"
    to: string;    // "07:00"
    timezone: string;
  };
}
```

The scheduler enforces these limits and will not send messages when the budget is exhausted. This is critical for:
- Avoiding account bans
- Respecting provider ToS
- Keeping the transport profile low
- Allowing the user to control costs

## Provider Selection

The `ProviderSelector` chooses the best provider for each outbound frame based on:
- Provider health (latency, loss estimate, consecutive failures)
- Budget remaining
- Frame priority (control messages may be sent via quorum)
- Provider capabilities vs frame requirements

## WhiteTransport Shared Channel Adapter

Existing any-transport providers can be exposed through the monorepo-wide
`ProviderChannel` contract with `LegacyProviderChannelAdapter` from
`packages/providers/channel-contract.ts`.

The adapter encodes `ChannelPayload` bytes into a `WTPC1.` text envelope, then
uses the provider's existing `append` and `scan` methods. This keeps VK/TG/OK
provider code usable while room state, client feedback, admin commands,
provider probes, and endpoint announcements migrate to the shared
`@whitetransport/provider-channels` control-message schema.

`packages/providers/room-discovery.ts` is the first runtime helper on top of
that schema. It publishes `room_state` to multiple channels, reads room
announcements back with per-provider cursors, and reports provider failures
without discarding successful announcements from other channels.

`packages/providers/control-bus.ts` owns the generic multi-provider publish/read
path. New helpers for client feedback, admin commands, provider probes, and
endpoint announcements should call that bus instead of reimplementing quorum,
cursor, and failure handling.

`packages/providers/control-helpers.ts` contains those typed helper builders
and publish/read wrappers. Use it from admin/client integration code to keep
message body shapes consistent across web, native clients, servers, and
provider adapters.
