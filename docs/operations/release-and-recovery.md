# Release and recovery entrypoints

Project readiness is recorded in [PROJECT_STATUS.md](../../PROJECT_STATUS.md).

## Update the existing daemon fleet

```bash
WT_BUILD_VERSION=0.5.1 bash ops/deploy/deploy-all.sh
```

The fleet inventory is `ops/deploy/targets.json`. Existing node configuration
is preserved; the TokenStore is refreshed in a generated `.tmp/` copy. One
Linux daemon build is reused for all targets. Uploads finish before stopping
the old daemon. A failed switch or health check restores the old files and
restarts the service. Use `--dry-run` to inspect the rollout. Health checks
prove startup; run the supported client payload canary for traffic proof.

This updates installed nodes; it does not provision a new server, install a
new provider, or build desktop/mobile packages.

## Resume a frozen release

```bash
GH_CONFIG_DIR="$HOME/.config/gh-meanwebuser" \
  python3 ops/release/publish-release.py .tmp/release-0.5.1-ready \
  --publish --public-commit e46b5fff3192f6fc1c7bada21de372abe72cc14b
```

The release directory contains `assets/`, original hashed receipts in
`evidence/`, `release-manifest.json`, `site-manifest.json`, `update.json`, and
release notes. Optional `private-assets/` is sent only to private GitHub.
Without `--publish`, the command validates locally. Both `vVERSION` tags must
already identify the reviewed private and sanitized public source commits.

The publisher compares remote hashes and skips identical files, retries
transient GitHub/curl failures, verifies download-back bytes, and updates the
site metadata last. A same-name file with different bytes is an error, never
an implicit replacement. To change a published binary, build and test a new
version. The client archive gate rejects server directories; private server
bundles must remain separate from downloadable clients.

All four `0.5.1` clients were built from `be348292`. Historical `0.5.0`
desktop artifacts were built from `7a2741ac`, Android from `41c48e5e`.
Later runtime or operation commits do not retroactively change those bytes.
Use the per-artifact source commit and receipt when deciding what to rebuild.

## Checks for changed behavior

### Managed client ports

Managed desktop and Android startup request loopback port zero: the operating
system assigns the port while binding the socket. Desktop consumes the owned
daemon's API startup announcement; SOCKS consumers use its actual bound status.
The managed launch copy overrides historical API/SOCKS port settings without
rewriting the source configuration. Explicit external `local-api` mode retains
the operator's address. Test harnesses discover bound listeners instead of
checking a free port, closing it and hoping it stays free.
The managed sing-box sidecar follows the same rule: its inbound binds port zero,
and readiness uses the actual address announced by that child. An occupied old
port cannot masquerade as a ready sidecar.

### Channel ordering and calibration

The runtime scores eligible carriers using their capabilities and catalog
metrics. Egress then races a diverse batch of three candidates and takes the
first successful connection; this measures connection establishment, not
throughput. A successful fallback is tried first on later connections in the
same session. Current health checks detect availability and failure, not speed.

`carrier-benchmark` reports catalog models and performs no network measurement.
There is no persisted real-throughput calibration in the current consumer path.
A separate future manual calibration should measure payload through each route,
save the result by stable node/carrier/profile identity, and try the preferred
available route before the normal fallback race. It should not run on every
build or connection. Recalibration can be manual after a network/provider change;
quarterly repetition is an operator choice, not a required timer. Such a
preference must retain failover and must not use the diagnostic manual pin.

### Recovery behavior

Automatic egress preference uses the same alias-aware policy ranking as the
fallback dial race. Only locally supported data endpoints can become the
preference or a background recovery candidate; control-only endpoints are
excluded. A failed destination or cancelled request does not quarantine its
carrier. Explicit carrier failures trigger degradation, and successful bounded
recovery probes plus cooldown allow new connections to return to the preferred
route without closing established fallback streams.

For live smoke of an already accepted daemon, set `WT_SMOKE_CLIENT_BINARY` to
its exact executable path when running `ops/deploy/smoke-e2e-full.sh`. An invalid
explicit path fails closed instead of building or using a different binary.

Connect expresses persistent user intent. A temporary session or route failure
does not cancel it. The existing runtime loop retries discovered nodes with
bounded attempts and backoff; a new node advertisement makes a retry eligible
immediately. During a complete outage, the API reports `reconnecting` and the
client retains its VPN intent. Recovery never adds direct SOCKS egress.

When only the client lost connectivity, recovery retries the existing session
release before offering a new session to the same live node. Pending release
coordinates expire with the old lease and do not delay switching to another
node. A manual endpoint selection also invalidates an in-flight background
liveness failure from the former control route.

Disconnect and Stop cancel running recovery and invalidate older queued Connect
requests. Explicit endpoint selection remains pinned. A restarted node ignores
offers created before its current process started, so retained mailbox history
cannot reserve it for an abandoned session. Desktop and Android consume the
recovery state without turning it into a user disconnect.

Run the complete deterministic outage/return/cancellation canary with:

```bash
bash core/go/tests/test-multinode-autoheal.sh
```

It stops both nodes, returns one with its mailbox intact, requires a connected
session before sending another SOCKS request, verifies payload, then checks
that Disconnect prevents recovery after another node returns. This proves
the common runtime with local carriers; device/provider acceptance is separate.
It does not promise continuity for TCP streams lost with a remote node, create
missing credentials, or restart a crashed application process.

```bash
bash core/go/tests/test-local.sh
bash ops/deploy/tests/test-deploy-targets-transaction.sh
bash ops/deploy/tests/test-deploy-targets-preserve-config.sh
python3 -m unittest discover -s ops/release -p test_publish_release.py
```

The local daemon test transfers payload through an isolated primary/backup
carrier pair. It does not prove a live provider. The deploy fixtures execute
the remote transaction with controlled commands and prove rollback behavior;
live rollout and the subsequent real client payload remain separate checks.
