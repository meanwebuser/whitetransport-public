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

For the Chrome or Edge extension, start a persistent local bridge:

```bash
.tmp/cli/wt-browser-credential-bridge --keep-alive
```

It prints one `READY` line containing a loopback URL, the `/browser-export`
path, and a random nonce. Paste the line into the extension's pairing field.
For local command-line testing, send one export with the printed nonce:

```bash
curl --fail-with-body -sS -X POST \
  -H "X-WT-Bridge-Nonce: $nonce" \
  --data-binary @export.json \
  "$url"
```

Omit `--keep-alive` for a single import. The extension can poll the
bridge for commands once paired and enabled by the user. Keep the READY line
out of shared logs and do not use a fixed development nonce for pairing.
