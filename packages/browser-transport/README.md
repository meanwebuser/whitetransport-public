# browser-transport

Correct no-SOCKS direction for iPhone/PWA.

## Final flow

```text
Scramjet/libcurl/wisp client
  -> Wisp packets
  -> WispOverWbSocket
  -> whitelist-bypass tunnel frames
  -> WB Stream WebRTC DataChannel/VP8 tunnel
  -> exit-node creator RelayBridge
  -> TCP dial to OpenWebUI/internet
```

## Implemented now

- `wb-frame-codec.js` — whitelist-bypass tunnel frame codec compatible with `relay/tunnel/protocol.go`.
- `wisp-packet-codec.js` — minimal Wisp packet parser/encoder.
- `wisp-over-wb-adapter.js` — WebSocket-like Wisp endpoint backed by WB DataChannel.
- `whitelist-bypass-byte-duplex.js` — shared `ByteDuplex` connector boundary for `any-transport`.
- `install-wisp-over-wb.js` — patches `window.WebSocket` for `/wisp/` URLs and joins WBStream through LiveKit by default.
- `wbstream-livekit-joiner.js` — browser LiveKit/WBStream data transport joiner.

## Still hardening

The browser LiveKit data joiner exists, but production parity still needs
explicit coverage for WBStream session lifecycle, reconnects, and VP8/video
tunnel mode.

Those details should stay aligned with upstream whitelist-bypass internals:

```text
relay/wbstream/api.go
relay/wbstream/session.go
relay/livekit/*
```

## Why no SOCKS

The creator already accepts `MsgConnect` with payload `host:port` and dials TCP on the configured exit node. So the iPhone side only needs to translate Wisp CONNECT/DATA/CLOSE into whitelist-bypass MsgConnect/MsgData/MsgClose.
