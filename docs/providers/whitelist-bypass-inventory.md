# Original whitelist-bypass provider and transport inventory

This document maps the original `kulikov0/whitelist-bypass` relay revision
`0f9f9088749cacd8ae754fa58fea3bc7d1e5e78a` to WhiteTransport. It is an
import boundary, not a runtime readiness report; `PROJECT_STATUS.md` remains
the source of truth for readiness.

## Provider platforms

| Original provider | Original implementation | WhiteTransport boundary |
| --- | --- | --- |
| VK Call | `headless/vk`, `relay/pion/video_vk.go`, `VKHeadlessJoiner` | `adapters/vkcall` wraps the upstreamable `relay/vkcall` HTTP API and `VKHeadlessJoiner`; browser cookie is resolved from a role-scoped TokenStore binding. |
| Yandex Telemost | `headless/telemost`, `relay/telemost`, `TelemostHeadlessJoiner` | `adapters/telemost` wraps the joiner and its `DataTunnel`. |
| WB Stream | `headless/wbstream`, `relay/wbstream`, LiveKit client | `adapters/whitelist` wraps the WB Stream client and `DataTunnel`. |
| DION | `headless/dion`, `relay/dion`, `DionHeadlessJoiner` | `adapters/dion` wraps creator and joiner flows plus `DataTunnel`. |

The DION vertical lane is exercised by `core/go/tests/test-dion.sh`: it creates
an isolated DION room, waits for the live DataTunnel, then verifies raw, custom
TCP packet, and HTTP payloads through the client SOCKS endpoint. An authorised
BrowserOS DION session is imported into the canonical source by
`secrets/import_dion_browseros.py`; the generated TokenStore remains the only
credential input for deployed daemons.

The Telemost live lane is exercised by `core/go/tests/test-telemost.sh`. It
requires an existing authorised Telemost join link, starts headless VP8 tunnels
on both node and client, and verifies raw, packet, and HTTP payloads through
SOCKS. VP8 delivery is intentionally given a 20-second provider budget rather
than treating its observed multi-second frame latency as an egress failure.

The WBStream lane is exercised by
`core/go/tests/test-provider-wbstream-smoke.sh`. A BrowserOS scheduler export
can now update the explicitly assigned `node` credential even when the UI has
no phone value; the client intentionally registers a fresh guest identity.
The reliable MultiTrack KCP lane passed raw, packet, and HTTP SOCKS payload
proof on 2026-08-02. Its harness waits for the provider DataTunnel instead of
treating an accepted session request as a ready egress path.

The original relay has no standalone OK.ru/"Odnoklassniki" transport provider.
Its VK Call authentication performs an OK.ru anonymous login as one step of
that provider protocol; it does not provide OK messages, documents, or photos.

WhiteTransport's `vk.messages`, `vk.docs.*`, `vk.photos`, `ok.messages`,
`ok.docs.*`, and `ok.photos` are native fabric carriers. They must not be
described as imports from `whitelist-bypass`.

## Reusable transport primitives

| Primitive | Original implementation | WhiteTransport use |
| --- | --- | --- |
| WebRTC DataChannel framing | `relay/tunnel/DCTunnel` | Reused through provider `DataTunnel` and `DataTunnelEgress`. |
| VP8 frame tunnel | `relay/tunnel/VP8DataTunnel` | Reused by video provider joiners. |
| Multi-track KCP | `MultiTrackTunnel`, `MultiTrackKCPTunnel` | WBStream exposes it as `reliable: true` for video multi-track sessions; the upstream tunnel remains behind the common `DataTunnel` contract. |
| Symmetric screen tunnel | `SymmetricScreenTunnel`, `ScreenWriter` | VK-specific upstream capability; belongs with VK Call import, not messaging VK. |
| Session obfuscation | `TunnelObfuscator`, `DeriveSecretFromJoinLink` | Used by DION and video provider paths. |
| SOCKS bridge | `RelayBridge` | Upstream headless app concern; WhiteTransport uses `DataTunnelEgress` and its own SOCKS server. |
| VK WebTransport/HTTP3 signaling | `relay/wtsignal.Conn` | It is used internally by the upstream VK joiner. `adapters/vkcall` receives it through `VKHeadlessJoiner`; there is no separate provider adapter or credential type. |

## Modes that are not standalone providers

The original relay executable also exposes `dc-joiner` and `dc-creator`.
They are generic local WebSocket bridges for a browser hook and do not identify
an external site, account, or discoverable remote endpoint. WhiteTransport
does not import them as a fake carrier: its browser track owns browser
integration and its native track already supplies the SOCKS bridge.

`relay/wtsignal` is likewise not an independent exit provider. It is the
HTTP/3/WebTransport signaling client used by the upstream VK flow, and is
therefore imported transitively by the VK headless joiner. A direct
WhiteTransport `wtsignal` carrier is only justified if we also own and test a
matching authenticated signal endpoint; the original source contains no
general-purpose standalone provider for that purpose.

## Import acceptance contract

Every provider import must preserve one contract:

```text
provider adapter -> VideoTunnelAdapter -> ProviderCarrier -> UnifiedCarrierTunnel
-> active session endpoint -> SOCKS5 CONNECT -> exact target payload
```

The deterministic test must record the selected endpoint, a provider-branch
trace, the target nonce, and an explicit absence of direct fallback. A live
test is additional evidence and must use an isolated provider room/session,
then disconnect and clean up.

## VK Call import boundary

The browser-cookie authentication, fresh-call creation, and join flow is now
an importable upstreamable `relay/vkcall` API on the
`meanwebuser/whitelist-bypass` `provider-vkcall-api` branch. The WhiteTransport
adapter remains a thin wrapper: it resolves a `vk/calls` role-scoped TokenStore
credential, asks the API to create or join a room, and passes the returned
authentication values to `VKHeadlessJoiner`. It does not copy VK HTTP or WebRTC
protocol code into `core/go`.

`core/go/tests/test-vkcall.sh` is the live vertical harness. It accepts either
an explicitly supplied existing call link or an explicit peer ID; it never
chooses a peer itself. In existing-link mode both node and client join the same
room, then the harness waits for the DataTunnel and checks raw, packet, and
HTTP payloads through client SOCKS. The two roles must resolve to different
TokenStore token IDs; a real two-principal call is required for the provider
proof, because VK terminates a same-principal pair after WebTransport join.
