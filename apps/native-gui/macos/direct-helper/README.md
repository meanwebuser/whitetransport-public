# macOS direct-utun helper

This is an isolated, administrator-run CLI for the direct macOS backend. The Wails GUI remains unprivileged and calls this helper through an explicit local integration that can be added later. The helper has no dependency on the ignored `whitelist-bypass/relay/desktoptun` checkout.

Build and install on a Mac:

```bash
cd apps/native-gui/macos/direct-helper
./install-direct-helper.sh
./run-direct-helper.sh test
sudo ./run-direct-helper.sh start
./run-direct-helper.sh status
sudo ./run-direct-helper.sh stop
```

The helper accepts only loopback SOCKS endpoints (`127.0.0.1` or `::1`). `full` installs the two `/1` routes, `bypass` installs those routes plus exact gateway routes for `bypass_cidrs`, and `only` installs only exact `only_cidrs` routes. The start state stores every route so stop can delete exactly what start installed. When invoked through `sudo`, `run-direct-helper.sh` resolves the original console user's home (`SUDO_USER`) so root start/stop use the same config and state files.

The current utun setup tunnels IPv4 only; native IPv6 traffic remains governed by the existing system routes. IPv6 provider bypasses use the physical interface's IPv6 default gateway, including its link-local scope. If no physical IPv6 default exists, these redundant bypasses are omitted without adding IPv6 tunnel routes. Explicit IPv6 `only_cidrs` are rejected rather than silently bypassed. Installed IPv6 next hops are saved with each route for exact rollback and stop.

Each start selects a point-to-point IPv4 address from `198.18.0.0/15` outside every other interface's IPv4 prefix. An existing VPN using `198.18.0.0/16`, for example, makes this helper choose `198.19.0.1`. The owned peer is saved in state and each tunnel route, then reused unchanged during refresh, rollback and stop. If the pool is occupied, startup fails without modifying the other VPN's interfaces or routes.

`refresh --config PATH` updates an active session or lease without stopping tun2socks or removing tunnel routes. Physical bypass additions precede removals; a failed refresh rolls back only those bypass changes. SOCKS endpoint, tunnel mode/MTU, tunnel routes and existing-prefix next-hop changes require an explicit reconnect. The GUI retains the current tunnel on refresh or profile-confirmation failure; authorization cancellation waits for an explicit retry, while other refresh failures have a 30-second retry delay. Refresh remains a privileged operation and can require macOS authorization.

JSON output is written to stdout. Logs default to `~/Library/Logs/WhiteTransport/direct-helper.log`; state and the `test` result default to `~/Library/Application Support/WhiteTransport/direct-helper/`.

The helper invokes `tun2socks` with a fixed argument vector (`-device tun://utun`, `-proxy socks5://...`, `-mtu ...`) and never evaluates a shell command. The installer and Wails GUI copy the bundled sidecar for the selected Darwin architecture (`x86_64`/Go `amd64` or `arm64`) into `~/Library/Application Support/WhiteTransport/bin/tun2socks`; set `WT_MACOS_GOARCH` only for an explicit cross-architecture build and `tun2socks_path` only for an explicit test override. The Wails GUI uses this direct backend by default and primes administrator access with `sudo -v`; set `WT_MACOS_VPN_BACKEND=networkextension` only to opt into the experimental Network Extension backend.
