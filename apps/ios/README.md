# iOS apps

`whitelist-bypass-proxy/` is the iOS client imported from the local `whitelist-bypass` checkout at the checkpoint state.

Responsibility:

- iOS UI and proxy/client behavior;
- iOS discovery parsing and provider failover behavior;
- compatibility with Android for encrypted room/control envelopes.

Do not add iOS-only protocol changes without matching Android/client updates.
