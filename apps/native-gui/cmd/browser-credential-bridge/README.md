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

For the Chrome or Edge extension, run the CLI next to the unpacked
`WhiteTransport` directory or pass its path:

```bash
.tmp/cli/wt-browser-credential-bridge --extension-dir /path/to/WhiteTransport --keep-alive
```

The CLI writes its per-run connection file into the extension directory
and listens on `127.0.0.1:17890` by default. The extension uses it to send
and poll automatically. For local command-line testing without the
extension, run the CLI on its own and use the address and nonce in its
`READY` line:

```bash
curl --fail-with-body -sS -X POST \
  -H "X-WT-Bridge-Nonce: $nonce" \
  --data-binary @export.json \
  "$url"
```

Omit `--keep-alive` for a single import. Keep the READY line and the
per-run connection file out of shared logs and source control.
