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
