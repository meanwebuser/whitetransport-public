# `wt-browser-credential-bridge`

This is the headless CLI path for importing one canonical browser export into
the local WhiteTransport client. It does not start Wails or any desktop GUI.
The command reuses the native runtime parser, replaces the imported provider
credentials atomically, and refreshes the managed daemon config when managed
mode is enabled.

Build it from the repository root:

```bash
make build-browser-credential-bridge
```

The binary is written to `.tmp/cli/wt-browser-credential-bridge`. Import an
export through stdin:

```bash
cat export.json | \
  WT_NATIVE_GUI_CONFIG_DIR="$HOME/.config/WhiteTransport" \
  .tmp/cli/wt-browser-credential-bridge --stdin
```

The command prints a JSON summary such as:

```json
{"platforms":["wbstream"],"count":1}
```

Credential values are never part of the summary. The input must use the
canonical browser-export shape with a `source.host` and provider cookies or
storage values. The current parser supports the providers already handled by
`internal/runtime`.

For the future MV3 transport, run the same binary without `--stdin`:

```bash
.tmp/cli/wt-browser-credential-bridge --port 0
```

It prints one `READY` line containing a loopback URL, the `/browser-export`
path, and a nonce. Send one export with the printed nonce:

```bash
curl --fail-with-body -sS -X POST \
  -H "X-WT-Bridge-Nonce: $nonce" \
  --data-binary @export.json \
  "$url"
```

After a successful import the bridge exits. The first slice intentionally uses
loopback HTTP and one explicit request; automatic refresh, extension identity,
WSS/certificate onboarding, and stronger replay/origin controls are tracked in
the project roadmap.
