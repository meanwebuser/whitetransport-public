# Provider channels

This package is the boundary for shared communication provider contracts.

Target responsibilities:

- normalize VK, Telegram, OK, and future provider send/receive APIs;
- expose provider health, budget, and failover signals to `@whitetransport/any-transport`;
- keep provider-specific tokens and operational details out of app code;
- make room discovery, client feedback, server commands, and admin control events use the same provider abstraction.

Existing provider implementations are currently split between:

- `packages/any-transport/packages/providers`;
- `services/creator-node`;
- Android/iOS client discovery code.

Future work should move shared contracts here before changing multiple clients.

## Current exports

`src/index.ts` defines the first stable contract surface:

- `ProviderIdentity`, `ProviderBudget`, and `ProviderHealth` for admin and scheduler state;
- `ChannelPayload`, `PublishedMessage`, and `ReceivedMessage` for logical messages before provider-specific encoding;
- `ProviderChannel` for publish/read provider implementations;
- `TransportEndpoint`, `ByteDuplex`, and `StreamTransportChannel` for stream transports such as whitelist-bypass/WBStream;
- versioned control envelopes for `room_state`, `client_feedback`, `admin_command`, `provider_probe`, `transport_endpoint`, and `transport_payload`;
- `createControlEnvelope`, `encodeControlPayload`, and `decodeControlPayload` for shared provider-message JSON bytes;
- `isProviderUsable` for shared failover gating.

The package intentionally has no VK/TG/OK implementation yet. Implementations
should depend on these contracts instead of inventing local message shapes.

`packages/any-transport/packages/providers/channel-contract.ts` is the first
adapter from imported any-transport providers into this shared contract.
The package emits CommonJS-compatible runtime helpers because
`@whitetransport/any-transport` currently builds as CommonJS.
