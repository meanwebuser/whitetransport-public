# Headless Mac M1 consumer route

The M1 daemon remains a client of the existing WhiteTransport route. Its
daemon and SOCKS listeners stay on loopback. This is deliberate: changing the
SOCKS bind to a LAN or wildcard address would create an unauthenticated proxy
surface and would conflict with the system-VPN loopback contract.

The background lifecycle is provided by
apps/native-gui/macos/headless/install-whitetransport-launchagent.sh. The
LaunchAgent starts whitetransportd, connects the configured session, and
writes the actual API/SOCKS endpoint to the mode-0600 state file:

    ~/Library/Application Support/WhiteTransport/headless/state/status.env

An operator device reaches that loopback SOCKS endpoint through an
SSH-authenticated local forward. The helper reads the actual port from
status.env, validates that the daemon is running and connected, then keeps
the forward in the foreground:

    apps/native-gui/macos/headless/forward-m1-socks.sh --listen 1088

When the operator device reaches the M1 through server-100, add an SSH jump
host. The final SSH target is still the M1 reverse endpoint, so host-key and
user checks remain attached to the M1 connection:

    apps/native-gui/macos/headless/forward-m1-socks.sh --proxy-jump roomhacker@server-100.example --listen 1088

The local application then uses socks5h://127.0.0.1:1088. DNS is resolved
through SOCKS, and the forward exposes only the requesting device's loopback;
the M1 SOCKS port is never published as a raw LAN/WAN listener. Each device
gets its own SSH-authenticated forward and can close it independently.

The minimum acceptance chain is:

1. Verify the M1 identity and the LaunchAgent state.
2. Read the actual SOCKS endpoint from the M1 status file.
3. Start the helper from the operator device.
4. Run an external-IP, DNS, HTTPS, and Yandex Telemost request through the
   operator-side SOCKS port.
5. Reconnect the M1 session, repeat the payload, and close the forward.

An open port is not acceptance evidence. The external payload must pass
through the forwarded M1 SOCKS endpoint, and the existing server-44 route must
remain available as the rollback path.
