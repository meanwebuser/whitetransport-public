# WhiteTransport WBStream Chrome extension

This is a minimal Manifest V3, client-only bridge for refreshing a WBStream
browser session. It is intentionally export-only: it does not contact a
WhiteTransport server, does not use a native messaging host, and does not run
on any site except `https://storage-control-stream.wb.ru/`.

## MVP scope

This directory is a WBStream-only MVP. It does not yet implement VK, Yandex,
DION, Telemost, or other provider collectors and must not be presented as the
finished multi-provider extension.

## User flow

1. In Chrome, open `chrome://extensions`, enable **Developer mode**, and use
   **Load unpacked** to select this directory.
2. Open `https://storage-control-stream.wb.ru/`, sign in, and create or open a
   room if WBStream requires that to refresh its session.
3. Click the extension, review which data types are selected, and choose
   **Save fresh session**.
4. Chrome asks where to save the JSON export. Import that file in the local
   WhiteTransport native client. The client replaces the old local WBStream
   credential atomically.

The extension reads only these values, and only after the explicit button
click: `x_wbaas_token`, `wbx-validation-key`, `_wbauid`, and the
`wb_auth_auth_slice` local-storage record. The generated JSON is the canonical
browser-export shape consumed by `apps/native-gui/internal/runtime`.

The JSON contains plaintext login material. Never share or commit it, and
delete it after the native client imports it. Room paths, query parameters,
URL fragments, unrelated cookies, and the Chrome cookie-store profile ID are
deliberately omitted from the export.

## Development check

```bash
node tests/validate.mjs
```

The validation checks the exact host and permission allowlist, the canonical
export shape, cookie filtering, origin rejection, and the absence of network
APIs in the extension scripts.

## Deliberate next step

A later native-messaging bridge may import this export directly into the local
native client after explicit user consent. It must keep the same allowlist and
must never send browser credentials to a server.
