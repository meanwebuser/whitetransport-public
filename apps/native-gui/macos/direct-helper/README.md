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

JSON output is written to stdout. Logs default to `~/Library/Logs/WhiteTransport/direct-helper.log`; state and the `test` result default to `~/Library/Application Support/WhiteTransport/direct-helper/`.

The helper invokes `tun2socks` with a fixed argument vector (`-device tun://utun`, `-proxy socks5://...`, `-mtu ...`) and never evaluates a shell command. The installer and Wails GUI copy the bundled Darwin-arm64 sidecar into `~/Library/Application Support/WhiteTransport/bin/tun2socks`; set `tun2socks_path` only for an explicit test override. The Wails GUI uses this direct backend by default and primes administrator access with `sudo -v`; set `WT_MACOS_VPN_BACKEND=networkextension` only to opt into the experimental Network Extension backend.
