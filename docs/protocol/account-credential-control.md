# Account credential control protocol

This document defines the provider-independent control plane for browser
credentials used by WhiteTransport carriers. It applies to WBStream, Yandex,
VK, Telemost, DION, and future browser-backed providers. A provider adapter
owns the credential fields and room API; the account ownership and room lease
rules are shared.

## The two deliberate modes

Each account has one explicit placement:

- local: credential material stays on the client. The client creates the
  provider room and sends only a session-scoped room endpoint to the node. The
  node joins as a guest. This is the current role-reversal path represented by
  session.offer.client_room_endpoint.
- server: the client explicitly grants this account to the VPS. The server
  receives the provider credential over the authenticated control channel,
  stores it in its private credential store, and may use it to maintain one
  persistent provider room. The client joins that room as a guest.

The server mode is intentionally an availability trade-off. A server that can
create or keep a provider room must be able to use the provider credential. A
client-only decryption key would make that impossible. Therefore server mode
must never be described as client-only confidentiality. The user sees the
destination and consents before a grant; the local mode remains available for
accounts that must never leave the client.

## Account and lease objects

account_id is a client-generated opaque identifier. It is not a phone number,
provider username, cookie, token, or room URL. A client can keep any number of
accounts for one provider.

    AccountRef {
      account_id: string,
      provider: string,
      label: string,
      placement: local | server,
      consent_id: string,
      revision: integer
    }

The control plane carries metadata and state, never a credential value outside
the explicit credential.grant operation.

    RoomLease {
      lease_id: string,
      account_id: string,
      provider: string,
      host: client | server,
      state: desired | starting | ready | degraded | revoking | closed,
      generation: integer,
      endpoint: string,
      expires_at: timestamp
    }

The endpoint is treated as a secret capability and is returned only through
the authenticated control channel. It is never written to ordinary logs.

## Mac M1 publication model

The persistent M1 consumer uses the local placement by default. The provider
credential remains on the M1, the server receives only the session-scoped room
endpoint, and the M1 SOCKS listener remains loopback-only. Other devices reach
that SOCKS endpoint through their own authenticated SSH local forward; they do
not receive the browser cookie and the M1 does not expose an unauthenticated
LAN or WAN proxy.

The helper reads the actual bound SOCKS port from the mode-0600 M1 headless
status file and refuses to forward unless the daemon is running and the
session is connected. This makes the endpoint dynamic and keeps the old
server-44 route available for rollback.

Server placement remains an explicit separate consent mode. A server grant
allows the VPS to use the credential and maintain a room, so it is an
availability trade-off rather than client-only confidentiality. The current
M1 canary does not silently convert a local account into server placement;
that requires the versioned grant/revoke/room protocol described below.

## Operations

The messages below use the existing versioned control-envelope convention.
Every mutating request has a client-generated request_id; retries with the
same id are idempotent.

### credential.grant

The client sends a provider-specific credential bundle for one account only
after explicit consent. The request includes account_id, provider,
placement: server, consent_id, and revision. The credential fields are carried
inside the authenticated grant body and are stored only in the server-side
private credential store. The server replies with the accepted revision and a
redacted account summary.

The server must reject a grant without a fresh consent id, a matching account
id, or a supported provider schema. It must not echo the credential in a
response, log, metric, error, or audit record.

### room.ensure

The server accepts an account id and desired persistent policy. It loads the
granted server credential, creates or restores one room for that account, and
updates the RoomLease. Repeating room.ensure for the same account and policy
reuses the current lease when healthy; a new generation is created only after
an observed failure, expiry, or explicit rotate request.

### room.state

The server publishes a redacted lease state. ready includes the provider and
generation but does not expose credential fields. A client can use the
endpoint as a guest according to the provider adapter's guest contract.

### room.rotate and credential.revoke

room.rotate closes the old lease after the new room is ready and prevents a
late old-room response from replacing the current generation. credential.revoke
stops room maintenance, closes the lease, deletes the server copy, and marks
the account revision revoked. Re-granting requires a new consent id.

## Recovery behaviour

The server's room keeper is independent from the client's ordinary transport
connection. If the client cannot reach the server temporarily, the server
continues maintaining a granted server account and its one room. When the
client returns, it reads room.state and joins the current generation instead of
creating a duplicate room. If the server loses the provider or credential, it
reports degraded and stops retrying after the adapter's bounded policy; the
client must explicitly grant again after correcting the account.

Local accounts follow the existing role-reversal flow: the client creates the
room, places its endpoint in the session offer, and the node joins as guest.
They do not participate in server room maintenance.

## Current implementation boundary

The repository already has the local credential array and the role-reversal
room path. The following pieces are still required before exposing a real
Send to server button:

1. Preserve multiple account records instead of replacing all credentials for
   one provider, and select an account explicitly for room creation.
2. Add the versioned grant/revoke/ensure/state/rotate messages and the
   server-side private credential store.
3. Add one persistent room keeper per granted account with generation and
   idempotency handling.
4. Add a client consent surface that shows the provider, account label,
   destination, and the availability/confidentiality trade-off.

The MV3 loopback slice and its JSON save path are independent of this server
protocol. A saved JSON file remains a valid manual recovery input to the CLI.
