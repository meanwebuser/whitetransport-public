# anYTransportProxy (Y Transport)

Encrypted, delay-tolerant, **peer-to-peer** stream multiplexer over social/messaging APIs.

**Обе ноды равноправные — нет "сервера" и "клиента".** Любая нода может быть proxy-клиентом (локальный SOCKS5), exit-нодой (выход в интернет), или обоими сразу. VK/TG/OK — это просто транспортный канал (общий mailbox), а не "сервер".

## Как это работает (простыми словами)

```
Твоя нода (Mac)                          Нода партнёра (Vercel/VPS/другой Mac)
┌─────────────────────┐                  ┌─────────────────────┐
│  Браузер → SOCKS5   │                  │                     │
│  :1080 → YTP Core ──┼── VK/TG/OK ──────┼──▶ YTP Core        │
│                     │   (общий         │   (как mailbox)     │
│  VK token + TG bot  │    чат)          │   VK token + TG bot │
│                     │                  │          │          │
│                     │                  │    Exit: TCP →       │
│                     │                  │    example.com:443   │
└─────────────────────┘                  └─────────────────────┘

  Обе ноды ОДИНАКОВЫЕ. Запускаешь один и тот же код.
  Разница только в MODE:
    full    = SOCKS5 + exit + long poll (по умолчанию)
    client  = только SOCKS5, long poll
    exit    = только exit node, long poll
    webhook = webhook receiver (Vercel serverless)
```

## One-liner Quick Start

```bash
# Просто даёшь токен VK — получаешь SOCKS5 прокси
VK_TOKEN_1=vk1.a.xxx VK_PEER_ID=123 npx ts-node packages/core/run.ts
# → SOCKS5 на :1080, HTTP proxy на :8080

# Или через Telegram
TG_TOKEN_1=123:ABC TG_CHAT_ID=456 npx ts-node packages/core/run.ts

# Или всё вместе (максимальная пропускная способность)
VK_TOKEN_1=vk1.a.xxx VK_PEER_ID=123 TG_TOKEN_1=123:ABC TG_CHAT_ID=456 \
  npx ts-node packages/core/run.ts
```

## Режимы работы (MODE)

| MODE | Что делает | Кому |
|------|-----------|------|
| `full` (default) | SOCKS5 + exit node + long poll | VPS, Mac, всегда онлайн |
| `client` | Только SOCKS5 proxy, long poll | Твой ноут, за NAT |
| `exit` | Только exit node (принимает подключения), long poll | VPS партнёра |
| `webhook` | Webhook receiver вместо long poll | Vercel / Cloudflare |

```bash
# На Mac — ты клиент, хочешь ходить в инет через партнёра
MODE=client VK_TOKEN_1=vk1.a.xxx npx ts-node packages/core/run.ts

# На Vercel — нода-партнёр всегда онлайн
MODE=webhook VK_TOKEN_1=vk1.a.xxx npx ts-node packages/core/run.ts
```

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                       Y Transport Node                           │
│                                                                 │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │  Local Proxy (optional — MODE=client/full)                │  │
│  │  SOCKS5 :1080  ─┐                                        │  │
│  │  HTTP   :8080  ─┤──▶ YTP Stream Multiplexer              │  │
│  └─────────────────┘                                        │  │
│                                                              │  │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │  Exit Node (optional — MODE=exit/full)                    │  │
│  │  Incoming YTP streams ──▶ TCP socket to target host       │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                              │  │
│  ┌──────────┐  ┌──────────┐  ┌──────────────────────────┐   │  │
│  │  Crypto   │  │ Protocol │  │    Scheduler / Failover   │   │  │
│  │  (NaCl)   │  │  (YTP1)  │  │  Cascade·Parallel·Quorum │   │  │
│  └──────────┘  └──────────┘  └──────────────────────────┘   │  │
│                                                              │  │
│  Providers = Transport Channel (shared mailbox)              │  │
│  ┌─────────┐ ┌────────┐ ┌────────┐ ┌────────┐ ┌─────────┐ │  │
│  │ VK Text │ │ VK Doc │ │ TG Bot │ │ OK Doc │ │ Yandex  │ │  │
│  │ (LP/WH) │ │(PNG)   │ │(LP/WH) │ │(PNG)   │ │  Disk   │ │  │
│  └─────────┘ └────────┘ └────────┘ └────────┘ └─────────┘ │  │
└─────────────────────────────────────────────────────────────────┘

LP = Long Poll    WH = Webhook (Vercel)
```

## Vercel Deployment (Webhook Mode)

Vercel — это просто **вторая нода**, которая всегда онлайн и принимает webhook'и вместо long poll. Она равноправная, не "сервер".

```
┌──────────────┐     POST /api/webhook/vk      ┌──────────┐
│  VK Callback  │ ─────────────────────────────▶│  Vercel  │
└──────────────┘                                │ (YTP     │
┌──────────────┐     POST /api/webhook/tg      │  Node)   │
│  TG Webhook   │ ─────────────────────────────▶│          │
└──────────────┘                                │  Равный  │
┌──────────────┐     POST /api/webhook/ok      │  партнёр  │
│  OK Streaming │ ─────────────────────────────▶│          │
└──────────────┘                                └──────────┘
```

```bash
# Deploy
cd vercel && vercel --prod

# Set env vars
vercel env add VK_TOKEN_1 TG_TOKEN_1

# Register webhooks
curl -X POST https://your-app.vercel.app/api/webhook/register \
  -H 'Content-Type: application/json' \
  -d '{"base_url":"https://your-app.vercel.app"}'
```

## Providers

WhiteTransport integration note: shared provider identity and budget contracts
live in `@whitetransport/provider-channels`. The local adapter is
`packages/providers/channel-contract.ts`; new provider/failover work should use
that boundary instead of introducing app-specific shapes.

`packages/providers/whitelist-bypass.ts` defines the first WBStream stream
transport descriptor. It exposes whitelist-bypass endpoint metadata and tunnel
message constants while the real WBStream adapter remains in
`packages/browser-transport`.
Runtime integration is intentionally injected through a `WhitelistBypassConnector`
so browser, server, and native clients can reuse the same provider shape without
putting product code into the upstream `whitelist-bypass` checkout.

`packages/providers/channel-contract.ts` also exposes
`LegacyProviderChannelAdapter`. It wraps existing any-transport text providers
as `ProviderChannel` instances by carrying shared `ChannelPayload` bytes inside
an explicit `WTPC1.` text envelope. This is the migration path for room
discovery, client feedback, admin commands, and provider probes before VK/TG/OK
implementations move fully into `packages/provider-channels`.

`packages/providers/room-discovery.ts` builds on that contract for room
announcements. It publishes the same `room_state` envelope through multiple
provider channels and reports per-provider failures, so clients can keep
discovering rooms when one provider is unavailable.

`packages/providers/control-bus.ts` is the generic layer under room discovery.
Use it for client feedback, admin commands, provider probes, and endpoint
announcements so all control-plane traffic gets the same multi-provider
failure handling and cursor behavior.

`packages/providers/control-helpers.ts` provides typed builders and publish/read
helpers for client feedback, admin commands, provider probes, and transport
endpoint announcements. Client and admin surfaces should use those helpers
instead of constructing control bodies directly.

### Text Transport (low bandwidth, ubiquitous)

| Provider | Mode | Max Msg Size | Throughput | Latency | Rate Limit | Daily Cap |
|----------|------|-------------|------------|---------|------------|-----------|
| VK Text | Long Poll / Webhook | 4 KB | 12 KB/s | ~200ms | 3 req/s/token | ~1 GB |
| VK Text (2 tokens) | Multi-token | 4 KB | 24 KB/s | ~200ms | 6 req/s | ~2 GB |
| VK Browser Bridge | JSONP + WS | 4 KB | 12 KB/s | ~350ms | 3 req/s (separate pool) | ~1 GB |
| Telegram Bot | Long Poll / Webhook | 4 KB | 30 KB/s | ~100ms | 30 msg/s/chat | ~10 GB |
| Telegram (2 bots) | Dual bots | 4 KB | 60 KB/s | ~100ms | 60 msg/s | ~20 GB |
| OK Text | Long Poll / Webhook | 4 KB | 10 KB/s | ~250ms | ~2.5 req/s | ~844 MB |
| Yandex Disk | File upload | 4 KB/file | 40 KB/s | ~500ms | ~10-30 req/s | ~3.4 GB |

### Document Transport (HIGH BANDWIDTH — data preserved exactly)

| Provider | Mode | Image Size | Data/Image | Throughput | Daily Cap |
|----------|------|-----------|-----------|------------|-----------|
| **VK Document** | PNG in doc upload | 256x256 | **192 KB** | **576 KB/s** | **~49 GB** |
| **VK Document** | PNG in doc upload | 1024x1024 | **3 MB** | **9.2 MB/s** | **~798 GB** |
| **OK Document** | PNG in doc upload | 256x256 | **192 KB** | **~480 KB/s** | **~40 GB** |

### Photo Transport (steganographic cover)

| Provider | Mode | Notes | Throughput |
|----------|------|-------|------------|
| VK Photo | Cover image + text | VK re-encodes to JPEG (pixel data lost) | ~12 KB/s |
| OK Photo | PNG pixel encoding | OK may preserve PNG data | ~480 KB/s |

> **KEY INSIGHT**: VK `docs.getMessagesUploadServer` uploads documents **without re-encoding**, unlike photos which VK converts to JPEG (destroying pixel data). Always use `VKDocumentProvider` for reliable high-bandwidth transport. Photo providers exist for steganographic cover when you need messages to look like normal photo messages.

### Aggregate Throughput (all channels combined)

| Strategy | Providers | Total Throughput | Daily Cap |
|----------|-----------|-----------------|-----------|
| Minimal | VK Text + TG Bot | 42 KB/s | ~11 GB |
| Balanced | VK Doc + TG Bot + OK Text | 616 KB/s | ~51 GB |
| **Maximum** | **VK Doc(2t) + TG(2b) + OK Doc + YDisk** | **1.7 MB/s** | **~162 GB** |
| Ultra Doc | VK Doc(1024², 2t) + OK Doc(1024²) | **9.7 MB/s** | **~839 GB** |
| Stealth | Yandex Disk only | 40 KB/s | ~3.4 GB |

## YTP Protocol

```
YT1.<session>.<epoch>.<A.seq>.<pri>.<D>.<nonce>.<ciphertext>.<tag>.<expires>
```

- NaCl authenticated encryption (X25519 + XSalsa20-Poly1305)
- Forward secrecy via key rotation
- Each envelope is self-contained and idempotent
- Out-of-order delivery tolerated (sequence numbers + reassembly)

## Transport Modes Explained

### VK Document Transport (`VKDocumentProvider`) — RECOMMENDED FOR HIGH BANDWIDTH

Binary data is encoded into PNG pixel RGB channels (3 bytes/pixel), uploaded as a VK document via `docs.getMessagesUploadServer`. VK stores documents **as-is** (no re-encoding). Recipient downloads the document, decodes the PNG, and extracts the original data.

- 256x256 PNG = ~192 KB of data per message
- 1024x1024 PNG = ~3 MB of data per message
- Rate: 3 req/s per token, but ~50x more data per request

### VK Photo Transport (`VKPhotoProvider`) — STEGANOGRAPHIC COVER

VK re-encodes all photos to JPEG, which destroys pixel-level data. This provider sends data as text in the message body and attaches a visual cover photo for camouflage. Use when you need messages to look like normal photo messages.

TODO: DCT-domain steganography for JPEG-resistant data encoding in actual photo pixels.

### OK Document Transport (`OKDocumentProvider`) — HIGH BANDWIDTH

Similar to VK Document — encodes data into PNG, uploads as OK document attachment. OK's document upload API preserves files without re-encoding.

### OK Photo Transport (`OKPhotoProvider`)

Encodes data into PNG pixel encoding, uploads via OK photos API. May fall back to text if photo upload fails.

### VK Browser Bridge (`VKBrowserBridgeProvider`)

Uses Kate Mobile OAuth (app_id=2685278) to get a **separate VK rate limit pool** via browser-based JSONP API calls. Gives an additional 3 req/s beyond server-side tokens.

### Yandex Disk (`YandexDiskProvider`)

File-based transport: upload chunks as files, share, download. Harder to detect, generous rate limits (~30 req/s), files up to 50 GB. OAuth auto-refresh built in.

## Webhook Providers (Serverless)

### VKWebhookProvider
Uses VK Callback API. Requires a VK Community (group) with Messages enabled. Set the Callback URL to `https://your-vercel.app/api/webhook/vk`.

### TGWebhookProvider
Uses Telegram `setWebhook`. After deploying, register the webhook URL with `POST /api/webhook/register`.

### OKWebhookProvider
Uses OK Streaming API. Set the callback URL in OK application settings to `https://your-vercel.app/api/webhook/ok`.

All webhook providers use a `WebhookStore` interface for message persistence. Default: `MemoryWebhookStore` (single-instance). For production, use Vercel KV or Upstash Redis.

## Quick Start

### Самый простой способ (Mac)

```bash
# Clone
git clone https://github.com/meanwebuser/anYTransportProxy.git
cd anYTransportProxy
npm install

# Настрой .env
cp .env.example .env
# Открой .env и вставь свой VK token + peer ID

# Запусти — одна команда
source .env && npx ts-node packages/core/run.ts
# → SOCKS5 :1080, HTTP :8080

# Настрой браузер: SOCKS5 → 127.0.0.1:1080
```

### У партнёра (Vercel — всегда онлайн)

```bash
cd vercel && vercel --prod
vercel env add VK_TOKEN_1 TG_TOKEN_1
curl -X POST https://your-app.vercel.app/api/webhook/register \
  -H 'Content-Type: application/json' \
  -d '{"base_url":"https://your-app.vercel.app"}'
```

### У партнёра (другой Mac / VPS)

```bash
git clone https://github.com/meanwebuser/anYTransportProxy.git
cd anYTransportProxy && npm install
MODE=exit source .env && npx ts-node packages/core/run.ts
# → Ждёт входящие YTP стримы, открывает TCP, релеит данные
```

### Benchmarks

```bash
source .env && npx ts-node scripts/benchmark.ts
```

## Environment Variables

```bash
# ── Telegram ──────────────────────────────────
TG_TOKEN_1=123456:ABC-DEF...
TG_TOKEN_2=789012:GHI-JKL...     # optional second bot
TG_CHAT_ID=123456789
TG_WEBHOOK_SECRET=your_secret    # optional, for webhook verification

# ── VK ────────────────────────────────────────
VK_TOKEN_1=vk1.a.your_token...
VK_TOKEN_2=vk1.a.second_token... # optional second token
VK_PEER_ID=your_peer_id
VK_GROUP_ID=123456               # for Callback API
VK_CONFIRMATION_TOKEN=abc123     # for Callback API confirmation
VK_CALLBACK_SECRET=your_secret   # for Callback API verification

# ── OK ────────────────────────────────────────
OK_TOKEN=your_token:APP_KEY
OK_APP_KEY=your_app_key
OK_APP_SECRET=your_session_secret
OK_CHAT_ID=chat:${WT_OK_CHAT_ID}
OK_RECIPIENT_ID=your_recipient_id

# ── Yandex Disk ───────────────────────────────
YDISK_TOKEN=y0__your_token...
YDISK_REFRESH_TOKEN=2:AAA:your_refresh...
YDISK_CLIENT_ID=your_client_id
YDISK_CLIENT_SECRET=your_client_secret

# ── Serverless ────────────────────────────────
BASE_URL=https://your-app.vercel.app
```

## File Structure

```
packages/
├── core/
│   ├── run.ts              # ← ЕДИНЫЙ ENTRY POINT (просто запусти это)
│   ├── node.ts             # YTransportNode (orchestrator)
│   ├── peer.ts             # Peer info + invite codes
│   └── session.ts          # Session manager
├── crypto/         # NaCl box, handshake, identity, key rotation
├── protocol/       # YTP1 envelope, ack, bundle, checkpoint
├── providers/      # Transport adapters (shared mailbox)
│   ├── provider.ts          # Provider interface (append/scan)
│   ├── telegram.ts          # TG Bot API (long poll)
│   ├── vk.ts                # VK Long Poll (text, multi-token)
│   ├── vk-photo.ts          # VK Photo (steganographic cover)
│   ├── vk-document.ts       # VK Document (HIGH BANDWIDTH — PNG в doc)
│   ├── vk-browser-bridge.ts # VK via browser JSONP
│   ├── ok.ts                # OK Long Poll (text)
│   ├── ok-photo.ts          # OK Photo upload
│   ├── ok-document.ts       # OK Document upload (HIGH BANDWIDTH)
│   ├── yandex-disk.ts       # Yandex Disk file transport
│   ├── webhook.ts           # Webhook providers (VK/TG/OK + WebhookStore)
│   ├── image-codec.ts       # PNG encode/decode for data-in-pixels
│   ├── memory.ts            # In-memory (testing)
│   └── file.ts              # Local file (testing)
├── scheduler/      # Failover, priority, budget, retransmit
├── storage/        # SQLite message store
└── proxy/          # SOCKS5, HTTP CONNECT, DNS

vercel/
├── api/
│   ├── webhook/
│   │   ├── vk.ts        # VK Callback API handler
│   │   ├── tg.ts        # Telegram webhook handler
│   │   ├── ok.ts        # OK Streaming handler
│   │   ├── store.ts     # Message store query API
│   │   └── register.ts  # Webhook registration helper
│   └── _lib/
│       └── store.ts     # Shared message store
├── vercel.json
└── package.json

scripts/
├── benchmark.ts         # Real speed tests
├── full-demo.ts         # Test all providers
├── yandex-disk-oauth.ts # Token acquisition
└── bridge-server.ts     # VK Browser Bridge WS server

docs/
├── protocol.md          # YTP1 protocol spec
├── providers.md         # Provider comparison
├── threat-model.md      # Security analysis
└── vk-browser-bridge.html  # Browser companion page
```

WhiteTransport monorepo note: the Electron client shell is maintained in
`apps/desktop`; this package stays focused on reusable transport engine code.

## Benchmark Results

### Text Transport

| Provider | Avg Latency | Throughput | Msg Size | Rate Limit |
|----------|-------------|------------|----------|------------|
| VK Text (1 token) | ~200ms | 12 KB/s | 4 KB | 3 req/s |
| VK Text (2 tokens) | ~200ms | 24 KB/s | 4 KB | 6 req/s |
| TG Bot | ~100ms | 30 KB/s | 4 KB | 30 msg/s |
| TG (2 bots) | ~100ms | 60 KB/s | 4 KB | 60 msg/s |
| OK Text | ~250ms | 10 KB/s | 4 KB | 2.5 req/s |
| Yandex Disk | ~500ms | 40 KB/s | 4 KB | 30 req/s |

### Document Transport (PNG pixel encoding)

| Provider | Image Size | Data/Msg | Throughput | Daily Cap |
|----------|-----------|----------|------------|-----------|
| VK Doc | 256x256 | 192 KB | 576 KB/s | ~49 GB |
| VK Doc | 1024x1024 | 3 MB | 9.2 MB/s | ~798 GB |
| OK Doc | 256x256 | 192 KB | ~480 KB/s | ~40 GB |

### Webhook Latency (Serverless)

| Provider | Webhook Latency | vs Long Poll |
|----------|----------------|-------------|
| VK Callback | ~50-100ms | 2x faster (no poll) |
| TG Webhook | ~30-50ms | 2x faster (no poll) |
| OK Streaming | ~100-150ms | Similar |

## Security Notes

- All credentials must be in `.env` (gitignored) — **never commit tokens**
- Each YTP envelope is encrypted with NaCl (authenticated encryption)
- Forward secrecy via session key rotation
- Rate limit isolation: each token/app has independent pools
- VK Browser Bridge uses Kate Mobile OAuth for separate identity
- Webhook secret verification supported for all platforms
- VK Callback API secret key verification built in

## License

MIT
