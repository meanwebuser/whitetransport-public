# Direct-utun reset diagnostic harness

This harness is a bounded, diagnostic-only probe for the persistent macOS
direct-utun reset. It never needs WhiteTransport provider credentials or an
internet endpoint. The instrumented loopback SOCKS5 server accepts only the
numeric TEST-NET target `198.51.100.77:18080`, maps that target to a local nonce
HTTP server, records the SOCKS greeting/ATYP/CONNECT/reply, and checks the
payload returned by an ordinary proxy-free `curl`.

With `-tls-probe`, the local fixture serves an exact 64 KiB deterministic body
over HTTPS. The JSON result records `payloadBytes`, the observed and expected
SHA-256 digests, and `payloadHashValid`; the large response body itself is not
copied into the result. This remains a local dataplane diagnostic and does not
contact a provider.

The default invocation is safe and returns JSON with `status=not-run`. It does
not invoke `direct-helper`, `route`, `ifconfig`, or create a utun. The local Go
tests exercise the SOCKS mapping without a route or internet.

The only route-mutating path is the explicit root acceptance wrapper:

```bash
sudo -E env \
  WT_DIRECT_HELPER_BIN="$HOME/Library/Application Support/WhiteTransport/bin/direct-helper" \
  WT_TUN2SOCKS_BIN="$HOME/Library/Application Support/WhiteTransport/bin/tun2socks" \
  ./apps/native-gui/macos/direct-helper/test-direct-reset-macos.sh
```

The wrapper builds a temporary harness, passes the exact helper and tun2socks
paths, and runs `-accept-macos -tls-probe`, making the 64 KiB HTTPS payload
check part of every root-gated acceptance invocation. The harness writes one
structured JSON result
to stdout and always attempts `direct-helper stop --config <temporary config>`
after a successful start. The result includes SHA-256 hashes for the harness,
helper, and tun2socks binaries, the created utun, route decision, SOCKS trace,
nonce result, large-payload digest evidence when TLS is enabled, and explicit
cleanup flags. `diagnostic-complete` is evidence for this probe only; it is not
a product readiness or release claim.
