# WhiteTransport Web

This module contains the imported mobile-browser app that is already served
as a separately deployable browser application.

Responsibilities:

- provide the public/mobile browser UI for WhiteTransport;
- expose the Wisp endpoint used by Scramjet/BareMux/Ultraviolet transports;
- install and serve WhiteTransport browser transport assets from
  `packages/browser-transport`;
- keep browser app code separate from native clients, admin UI, and the
  upstream `whitelist-bypass` dependency.

## Commands

```bash
npm --prefix apps/web run build
npm --prefix apps/web start
```

`build` syncs `public/whitetransport/whitetransport-wb.js` from the canonical
`packages/browser-transport` bundle. The app remains a runtime server rather
than a static-only frontend because it owns auth, Wisp upgrade handling,
same-origin WB API proxying, and remote-browser routes.

The source was imported from the standalone mobile-browser repository. Backup
files from that local checkout are intentionally not part of this module.
