# Protocol Specification

This document outlines the WhiteTransport protocol for node discovery and session establishment.

## 1. Node States

The following states define the lifecycle of a node from the perspective of a client:

- **Advertised:** The node is running, has published its `NodeAdvertisement`, and is ready to accept new sessions. This is the initial state.
- **Busy:** The node has received a `session.offer` and is processing it. It has published a `node.withdraw` and is no longer accepting new session offers until the current session is resolved.
- **Active:** The node has successfully established a session with a client and is providing egress endpoints.
- **Ended:** The session has been terminated by either the client or the node. The node will typically transition back to the **Advertised** state after cleanup.
- **Retry:** (Implicit) If a session offer or answer fails or times out, the client or node may enter a retry state, attempting the operation again after a backoff period.

## 2. Wire Contract

Messages are exchanged using the `fabric.Envelope` structure, with `PayloadType` and `Payload` defining the specific protocol message.

### 2.1. `node.advertise`

- **Source:** Node
- **Traffic Class:** `fabric.TrafficBootstrap`
- **Payload:** `session.NodeAdvertisement` JSON object.
    - `NodeID`: Unique identifier for the node.
    - `Role`: Must be "node".
    - `Label`: Human-readable name for the node.
    - `Country`: Node's country (optional).
    - `Region`: Node's region (optional).
    - `Capabilities`: List of supported features (e.g., "egress", "control").
    - `Carriers`: List of `carriers.Endpoint` that clients can use for control signaling.

### 2.2. `node.withdraw`

- **Source:** Node
- **Traffic Class:** `fabric.TrafficBootstrap`
- **Payload:** `session.NodeWithdrawal` JSON object.
    - `NodeID`: The NodeID that is withdrawing.

### 2.3. `session.offer`

- **Source:** Client
- **Traffic Class:** `fabric.TrafficControl`
- **Payload:** `session.Offer` JSON object.
    - `SessionID`: Unique identifier for the session.
    - `ClientID`: Unique identifier for the client.
    - `Wanted`: List of desired capabilities/features (e.g., "egress.socks5").
    - `UsableCarriers`: List of `carriers.Descriptor` that the client can use.
    - `ReplyEndpoints`: List of `carriers.Endpoint` for the node to send the session answer back to.
    - `Metadata`: Additional client-provided metadata (optional).
    - `ClientRoomEndpoint` *(optional)*: If the client created the egress room locally using its own platform credentials, this field carries the room endpoint in the format `"<carrier>://<address>"` (e.g., `"wbstream://room-xyz"`). The node should join this room as a guest instead of creating its own. If empty, the node creates the room (legacy behavior). Client credentials never leave the device — only the room endpoint is transmitted.

### 2.4. `session.answer`

- **Source:** Node
- **Traffic Class:** `fabric.TrafficControl`
- **Payload:** `session.Answer` JSON object.
    - `SessionID`: The SessionID from the offer.
    - `NodeID`: The NodeID of the responding node.
    - `Label`: Node's label.
    - `Country`: Node's country (optional).
    - `Region`: Node's region (optional).
    - `Endpoints`: List of `carriers.Endpoint` that the client can use for control signaling (same as `NodeAdvertisement.Carriers` if node is available).
    - `EgressEndpoints`: List of `carriers.Endpoint` that the client can use for egress traffic. This is created dynamically per session. Empty when the node joined the client's room (see `JoinedClientRoom`).
    - `JoinedClientRoom` *(optional)*: `true` when the node successfully joined the client's pre-created room (role reversal). In this case `EgressEndpoints` is empty because the room was created on the client side.
    - `ExpiresAt`: Timestamp indicating when the session will expire.

### 2.5. Expiry

- Sessions have a defined `ExpiresAt` time. Clients and nodes must respect this expiry. Typically, sessions are given a short duration (e.g., 2 minutes) and are expected to be renewed or re-established if needed.

### 2.6. Retries

- When an operation (e.g., sending an offer, sending an answer) fails or times out, the client or node should implement a backoff strategy before retrying. Specific retry durations and limits are TBD but should be reasonable (e.g., starting with 1 second and doubling).

## 3. Session Flow

1. **Discovery:** Client polls bootstrap carriers for `node.advertise` messages.
2. **Offer:** Client selects a node and sends a `session.offer` to one of its control endpoints.
3. **Busy State:** Upon receiving the offer, the node immediately publishes `node.withdraw` on its bootstrap carriers and transitions to the "Busy" state.
4. **Answer:** Node selects compatible carriers, creates dynamic egress endpoints (e.g., WBStream room), and sends a `session.answer` back to the client's reply endpoints.
5. **Connect:** Client receives the `session.answer`, validates it, and establishes connections to the provided egress endpoints. The node transitions to "Active".
6. **Session End:** When the session ends (e.g., client disconnects, node restarts), the node cleans up resources and transitions back to the "Advertised" state.

### 3.1 Role Reversal (Client-Created Room)

When the client has local platform credentials (e.g., a WBStream access token stored on the device), it can create the egress room locally and have the node join as a guest. This is the **role-reversal** flow, enabled by `client_room_creation: true` in the client config:

1. **Client Creates Room:** Client reads local credentials, calls `createRoom()`, gets a room endpoint (e.g., `"wbstream://room-xyz"`), and acts as room host.
2. **Offer with `ClientRoomEndpoint`:** Client sends `session.offer` with `client_room_endpoint` set to the room address. Client credentials never leave the device.
3. **Node Joins as Guest:** Node sees `ClientRoomEndpoint`, calls `RegisterGuest`/`JoinRoom` with identity `"node-<NodeID>"`, and connects to the client's room.
4. **Answer with `JoinedClientRoom: true`:** Node sends `session.answer` with `joined_client_room: true` and empty `EgressEndpoints`.
5. **Fallback:** If guest join fails, the node creates its own room and sends a normal `session.answer` with `EgressEndpoints` (legacy flow).

## 4. Error Handling

- Timeouts: All network operations should have timeouts.
- Connection Errors: Carrier-specific errors should be logged and potentially trigger retries or session termination.
- Malformed Messages: Invalid payloads should be logged, and the sender may be ignored or a `node.withdraw` could be sent if the errors are persistent.

## 3. Wire Format

- Envelopes are serialized as JSON using `fabric.Envelope`.
- `v` is mandatory and must be `1`.
- `payload` carries raw JSON bytes and therefore appears on the wire as base64 text.
- `traffic_class` is one of `bootstrap`, `control`, `admin`, `health`, `log`, `stream`, `bulk`, `repair`, or `egress`.

Example envelope:

```json
{
  "v": 1,
  "id": "sess-123:offer",
  "src": "client-1",
  "traffic_class": "control",
  "payload_type": "session.offer",
  "created_at": "2026-06-08T12:00:00Z",
  "payload": "eyJzZXNzaW9uX2lkIjoic2Vzcy0xMjMiLCJjbGllbnRfaWQiOiJjbGllbnQtMSJ9"
}
```

## 4. Version Negotiation

- Writers must emit `v = 1`.
- Readers must reject any other version before payload decode or dispatch.
- The current runtime has no downgrade path.

## 5. Session Key Exchange

- Bootstrap traffic uses the long-lived discovery secret.
- `session.offer.session_key` carries the per-session key encrypted for the bootstrap context.
- After the offer/answer exchange, tunnel envelopes are sealed with the negotiated session cipher.

## 6. ACK, Error, and Heartbeat Semantics

- `session.offer.ack.status` is one of `received`, `busy`, or `error`.
- `retry_after` is advisory and only meaningful for `busy`.
- `error` is a human-readable operator string.
- `node.heartbeat` refreshes node liveness so clients can expire stale advertisements.

## 7. Error Codes and TTL

- Current runtime errors are string-based and are returned in `session.offer.ack.error` or the local HTTP API.
- Envelope `ttl` is optional. When set, receivers should treat the envelope as expired after `created_at + ttl`.
- Payload-level expirations such as `session.answer.expires_at` remain authoritative for session metadata.
