# WhiteTransport Go runtime

This directory is the new canonical runtime direction for WhiteTransport.

WhiteTransport is an adaptive carrier fabric. VK messages, VK documents, OK
messages, Yandex Disk folders, WBStream DataChannel, WBStream VP8, Telemost,
DION, browser hooks, and future audio carriers are all delivery mechanisms with
different constraints. Discovery, control, admin, logs, stream, bulk, and repair
are traffic classes moved through the same encrypted envelope model.

The first daemon target is `cmd/whitetransportd`.

## Roles

- `client`: discovers nodes, opens sessions, probes candidates, and chooses the
  active route for local SOCKS5/HTTP CONNECT traffic.
- `node`: listens on all configured carriers, publishes advertisements, accepts
  sessions, and serves egress.

Clients are the primary policy decision makers because they know local platform
permissions, carrier reachability, foreground/background state, and health.
Nodes should be broad listeners.

## Upstream proxy rule

Node mode may use an upstream proxy for client egress only. Carrier reads/writes,
bootstrap, control, health, and admin traffic must remain direct unless config
explicitly opts a carrier into upstream proxying.

This matters for weak VPS pools: client internet traffic may exit through the
pool, but provider authentication/control traffic should not be accidentally
rerouted through it.

## Current implementation

- `internal/fabric`: traffic classes and carrier-independent envelopes.
- `internal/carriers`: carrier interface, in-memory test carrier, and the
  current standard carrier catalog:
  - `vk.messages` for primary encrypted bootstrap/control/admin/health/log
    mailboxes. A real VK adapter exists behind `VKMessagesCarrier` using
    `messages.send` and `messages.getHistory`.
  - `ok.messages` for secondary mirrored or hedged mailbox delivery. A real OK
    Graph adapter exists behind `OKMessagesCarrier` using `/me/messages`.
  - `wbstream.vp8` for realtime primary stream and client egress traffic.
  - `vk.docs.1024` and `vk.docs.256` for high-throughput bulk/repair fallback
    or parallel transfer. A real VK document adapter exists behind
    `VKDocsCarrier`; the TS YTP PNG codec is still a higher-level payload codec
    to port, not part of the carrier contract.
  - `ok.docs.256` for high-throughput cross-provider fallback. A real OK
    document adapter exists behind `OKDocsCarrier` using signed OK `fb.do`
    calls and document attachments.
  - `ok.photos` as a pending cross-provider image candidate; keep it separate
    from OK documents because photo APIs may re-encode payloads.
  - `vk.photos` as a retained low-throughput repair candidate because VK photo
    re-encoding prevents treating it like a raw PNG document channel.
- `internal/session`: node advertisements plus session offer/answer exchange.
- `internal/policy`: traffic-class carrier selection and default adaptive
  routing order. `Plan()` returns `single`, `striped`, `mirrored`, or `hedged`
  delivery plans; `SchedulePayload()` turns those plans into concrete chunk
  placements; `DispatchScheduledPayload()` writes primary and mirrored chunks to
  carrier adapters and returns pending hedges for an ACK-aware retry loop.
  `DeliveryTracker` tracks ACKed chunks, due hedges, and repair placements.
  `Select()` remains a primary-carrier compatibility helper.
- `internal/config`: daemon config, enabled carrier validation, and upstream
  proxy policy. Upstream proxying applies to explicit `egress` traffic by
  default, not carrier `stream` or `bulk` traffic.
- `internal/runtime`: executable carrier binding construction from
  `carrier_configs`. Planner descriptors can run without credentials; real
  executors use env-backed carrier configs to instantiate VK/OK message and
  document adapters plus their endpoints. `DispatchPayload()` is the first
  single runtime entry point that plans, schedules, and writes one payload
  through configured bindings.

`mirrored` is intentionally reserved for small bootstrap/control messages by
default. Bulk payloads use `striped` plus repair placements, and admin/health/log
messages use delayed `hedged` duplication. Full stream/egress cloning is a
policy option to add later only if live measurements justify the quota and
bandwidth cost.

### Autonomous egress recovery safety

The recovery loop probes at most one degraded endpoint on a sparse, bounded
schedule. A probe never migrates an open TCP stream; a recovered route becomes
eligible only for a later connection after the policy hysteresis succeeds.

`ProviderCarrier` is deliberately not a generic recovery prober because its
ordinary `Probe` path can use an active provider session. A provider must
explicitly implement `provider.SafeEgressRecoveryProber` before the runtime
will unwrap the bridge for background recovery. The DION implementation uses a
fresh in-memory session with explicit access and refresh tokens, validates the
token and calls the authenticated `WhoAmI` endpoint. It never reads or writes
the configured cookie file, creates or joins a room, starts a call, or sends
egress payloads. Recovery cancellation is injected into every preflight HTTP
request and ends the scheduler wait even if upstream retry code is backing off.
Cookie-only and guest configurations fail closed. WBStream does not currently
expose an audited equivalent and therefore remains excluded from autonomous
provider recovery probes.

Generated node configs enable the DION carrier automatically only when the
TokenStore has an enabled creator/video binding with both access and refresh
parts. `WT_ENABLE_DION=0` is the explicit operational kill switch. Client
bootstrap projections never receive DION provider credentials; the node creates
the room and returns a session-scoped endpoint instead.

Run:

```bash
go test ./...
go run ./cmd/whitetransportd --config config.example.json
go run ./cmd/whitetransportd --config config.example.json --plan --traffic bulk --payload-bytes 1048576
go run ./cmd/whitetransportd --config config.example.json --serve
```

Guarded dispatch smoke:

```bash
WT_VK_TOKEN=... WT_OK_GRAPH_TOKEN=... WT_OK_ACCESS_TOKEN=... WT_OK_APPLICATION_KEY=... WT_OK_SESSION_SECRET_KEY=... \
go run ./cmd/whitetransportd --config config.example.json --dispatch --dispatch-confirm-write \
  --traffic control --payload-type session.state --payload-string "hello"
```

`--dispatch` writes to configured providers and therefore requires
`--dispatch-confirm-write`. The JSON summary prints route and write metadata,
not payload contents or secrets.

Local API:

- `GET /health`
- `GET /v1/plan?traffic=bulk&payload_bytes=1048576`

The HTTP API is intentionally planner-only for now. Runtime writes go through
`internal/runtime.DispatchPayload()` so Android, desktop, and creator code can
share one contract before a guarded local dispatch endpoint is exposed.
