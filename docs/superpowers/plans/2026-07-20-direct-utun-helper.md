# Direct utun macOS backend implementation plan

> **Owner:** root
>
> **Input revisions:** canonical monorepo `93f80e9c2ac82f60791db7a389946f02b8fd48f3`; comparison-only legacy checkout `90690e4322e6f4c4a4b543f573bafa28b2959b04`.
>
> **Goal:** make direct `utun → unprivileged tun2socks → system routes` the primary free macOS backend. The Wails GUI never runs as root. NetworkExtension remains an explicitly selectable experimental backend.

## Non-negotiable constraints

- The GUI, daemon, TokenStore, carrier credentials, and normal logs remain unprivileged.
- A root helper owns only utun creation, interface configuration, route transactions, its lease journal, and rollback/recovery.
- The worker receives a duplicated utun descriptor and runs the existing `core/go/mobile.StartTun2Socks` lifecycle as the console user.
- No caller may supply a shell fragment, executable, path, environment, interface name, gateway, route deletion, process ID, or DNS name.
- Full tunnel uses only helper-generated `/1` routes. Carrier/control bypasses are literal `/32` and `/128` routes captured before tunnel routes.
- A failed start, explicit stop, worker death, GUI disconnect, helper restart, network change, or stale journal must restore only helper-owned mutations.
- Test mode uses the same helper, utun, worker, and route transaction as the power button. Explicit SOCKS probes are not system-VPN proof.

## Chosen topology

```text
Wails GUI (user) ── launchd Mach XPC ──> wt-macos-net-helper (root, launchd)
     │                                                │
     │                                    utun / addresses / owned routes / journal
     │                                                │ inherited FD 3 + liveness FD 4
     └──── whitetransportd + fixed wt-tun-worker <────┘
                         │
                  loopback SOCKS only
```

The helper has one global lease. Its state directory is root-owned under
`/var/db/com.meanwebuser.whitetransport/`; launchd owns the Mach service. It
validates the XPC audit token, a custom Authorization Services right, a
root-owned installed-manifest CDHash, profile identity/hash/freshness, and the
exact fixed protocol before any mutation.

## P0 trust, authorization, and worker contracts

- The protocol is versioned and bounded: every request carries a version,
  request ID, deadline, and 256-bit nonce. Requests above 64 KiB, duplicate
  IDs/nonces, expired deadlines, malformed types, unknown fields, or replayed
  mutation requests fail before planning.
- The helper obtains the XPC audit token itself. It accepts only the active
  console UID and the installed app's root-owned manifest/CDHash; the caller
  cannot claim a UID, bundle ID, profile hash, or executable path. This is
  deliberate provenance enforcement, not merely a same-UID trust assumption.
- Every mutating request validates a fresh Authorization Services external form
  for the installed custom right. Installation creates that right; uninstall
  removes it. Authorization is never inferred from a cached GUI session.
- Successful `start` returns an unpredictable lease capability. The journal
  stores only its hash. `stop` and `reconcile` require the matching capability,
  generation, audit identity, console UID, and session; status/logs expose no
  capability material.
- The helper, not the GUI, spawns a fixed root-owned `wt-tun-worker`. It creates
  a duplicate utun descriptor as FD 3 and a liveness pipe as FD 4, sets a clean
  fixed environment, then calls `initgroups`, `setgid`, and `setuid` for the
  audited console user. The worker rejects extra/truncated descriptors, sets
  `FD_CLOEXEC`, owns and closes FD 3, while the tun2socks engine borrows it.
  EOF on FD 4 is a rollback trigger; the helper retains its own descriptor until
  teardown completes.

## Reusable inputs

- `core/go/mobile/tun2socks.go` and `tun2socks_shared.go` provide the existing
  FD-based start/stop lifecycle and must remain the worker engine.
- `apps/native-gui/system_vpn.go` supplies the authoritative profile identity,
  lifecycle semantics, expiry, and GUI status contract.
- `apps/native-gui/macos/WhiteTransportPacketTunnel/PacketTunnelConfiguration.swift`
  supplies the existing full/split route intent and control bypass rules.
- `whitelist-bypass/relay/desktoptun/bypass.go` may supply only pure endpoint-IP
  extraction ideas. Its Darwin shell route implementation is comparison-only:
  it has no tests, discovers utun ambiguously, ignores failures, is IPv4-only,
  and must not be imported or copied as the helper runner.
- The pinned MIT tun2socks engine remains the source at
  `third_party/tun2socks/upstream` with its existing notice requirements.

## Implementation sequence

### 1. Pure helper policy and route transaction model

Create `core/go/internal/directutun/policy.go`, `types.go`, and
`policy_test.go`. Keep this package platform-neutral and side-effect free.

Define typed protocol requests for `hello`, `start`, `stop`, `status`, `logs`,
and `reconcile`. `start` takes only an authoritative `SystemVPNProfile`; it
does not accept raw commands. Its output is a validated route plan consisting
of helper-derived utun identity, literal physical bypasses, and owned
split/full routes. The pure policy layer receives verified caller provenance
and authorization facts from the Darwin XPC boundary; it never trusts values
claimed by the request body.

First RED tests must prove that an interface such as `utun0;touch /tmp/pwned`,
an unknown operation, a user-supplied default route, a non-loopback SOCKS
endpoint, a stale profile, a malformed/noncanonical CIDR, an invalid MTU, a
second active lease, forged profile hash, expired Authorization form, mismatched
audit identity, and duplicate request nonce are rejected before a recording
runner receives any call. The first GREEN implementation creates only canonical
typed operations and fixed Darwin argv templates; it never builds a shell
command.

### 2. Lease journal and ownership-safe reconciliation

Create `core/go/internal/directutun/journal.go`, `journal_test.go`, and
`reconcile_test.go`. Use a write-ahead state machine
`planned → applied → reverting → reverted`: fsync the temporary journal and
its parent directory before each route/interface mutation. The mode is `0600`.
It stores profile hash, owner UID/audit identity, helper generation, exact utun
name, worker PID/start token, physical-route epoch, exact route key plus prior
value/mutation result, and a route digest; it stores only the lease-capability
hash.

Write RED tests for interrupted writes, stale generation, a route changed by
another VPN, duplicate lease startup, phase-specific crashes, and recovery
after worker death. GREEN must delete or restore only a route whose current
key/value still matches the journal. It must never restore an entire
routing-table snapshot or delete an unrelated VPN route.

### 3. Darwin helper and unprivileged FD handoff

Create `core/go/cmd/wt-macos-net-helper/` plus Darwin-only implementation in
`core/go/internal/directutun/darwin_*.go`. The helper creates the kernel-chosen
utun, records its exact name from the descriptor, configures fixed point-to-
point IPv4/IPv6 addresses, and registers the launchd Mach XPC service.

Add `core/go/cmd/wt-tun-worker/`, which receives its exactly two inherited
descriptors through fixed FD numbers, validates the fixed loopback SOCKS
endpoint, and calls `mobile.StartTun2Socks`/`StopTun2Socks` as the audited
console UID. The root helper never reads TokenStore data and never runs the
packet engine.

The helper permits only absolute `/sbin/ifconfig` and `/sbin/route` argv
templates until direct route-socket/ioctl implementations replace them. It
rejects every other executable and never invokes `sh`, `sudo`, `launchctl`,
`kill`, `networksetup`, or arbitrary `scutil`.

### 4. Installation and GUI integration

Add `apps/native-gui/macos/direct-helper/` installation assets:

- `com.meanwebuser.whitetransport.net-helper.plist` for the root LaunchDaemon;
- `install-direct-helper.sh` and `uninstall-direct-helper.sh`, both explicitly
  user-run with `sudo`, root:wheel ownership, non-user-writable modes, and no
  password capture;
- `test-install-direct-helper.sh` shell contract for paths, modes, and exact
  launchd command policy.

The installer additionally installs the custom Authorization right, manifest
and CDHash allowlist, helper and worker with fixed absolute paths, and the Mach
service definition. It resolves no user-controlled symlink, checks every parent
directory, uses atomic replacement, verifies a signed manifest before load, and
passes no inherited user environment. Upgrade/uninstall stops an active lease
and rolls back only clean helper-owned state; it never deletes a dirty journal.
The helper and worker perform a version handshake before a route mutation.

Add the Darwin direct-helper `systemVPNHost` implementation in
`apps/native-gui/system_vpn_direct_darwin.go`. It maps the existing immutable
profile to the helper protocol, publishes lease/generation/route digest status,
uses the existing logs surface, and refuses with `install_required` or
`permission_required` when the helper is absent or unauthorized. Keep
`system_vpn_darwin.go` as the explicit NetworkExtension experimental backend;
there is no silent fallback between backends.

Expose backend selection, real helper telemetry, split-route mode, and a
visible route-recovery error through the existing Home/Endpoints/Settings UI.
The main Home action remains the power button and server selector; it invokes
the same backend transaction as test mode.

### 5. Deterministic test mode and real acceptance harness

Create `apps/native-gui/macos/direct-helper/test-direct-utun-system-vpn.sh`
and reusable Go/Node support beneath `core/go/internal/directutun/`. The script
requires an explicit `WT_DIRECT_UTUN_ACCEPT=1`; without it it runs only
non-mutating `route -t` syntax rehearsal and reports that live acceptance was
not requested.

Live test mode captures baseline interfaces, IPv4/IPv6 defaults, DNS, control
endpoint routes, build IDs, profile hash, and physical-route epoch. It writes
an atomic `0600` JSON result containing run ID, stage, lease/generation, utun
name, exact before/active/after route entries, nonce challenge and payload
evidence, direct/tunnel IPs, worker/helper PIDs, journal transitions, and
cleanup result. The result contains no authorization form or lease capability.

Pass criteria, all with ordinary processes and proxy environment scrubbed:

1. IPv4 TCP and UDP plus IPv6 payloads transfer through system routing; no
   explicit SOCKS client is used.
2. Full tunnel preserves literal control/carrier bypasses on the original
   physical route.
3. Destination split routes an included CIDR to utun and leaves an excluded
   target direct; both assertions use kernel route lookup plus payload evidence.
4. Ten start/stop cycles restore baseline IPv4/IPv6 routes, DNS, interfaces,
   worker processes, and control endpoint reachability.
5. Failure injection after every persisted helper phase, and `SIGKILL` of GUI,
   worker, and helper, reconciles the journal within a bounded deadline.
6. A second existing VPN/utun, stale profile, malformed request, non-loopback
   SOCKS, missing helper, unauthorized peer, and network-change event fail
   closed without unrelated route mutation.
7. Replayed/expired authorization forms, forged profile hashes, changed audit
   identities, duplicate nonces, malformed XPC frames, extra/truncated worker
   FDs, and a worker with the wrong UID fail before route mutation.

## Explicit authority boundary

Initial live acceptance requires a user-authorized admin session for the helper
installer and for direct utun/route mutations. The GUI never requests or stores
the password. Do not run a live full-route test while V2BOX or another VPN owns
the active default route; the helper must refuse before mutation. NetworkExtension
profiles are not required for this backend. The helper captures and rechecks the
physical-route epoch immediately before the transaction; a network change aborts
and restores only the helper-owned journal entries. Direct is selected only after
its own health validation; it never silently falls back to NetworkExtension.

## Completion evidence

The direct backend is not ready from unit tests alone. Completion requires:

- committed RED/GREEN policy, journal, helper, worker, GUI, and installer tests;
- independent security review of protocol, authorization, argv validation, FD
  ownership, and rollback semantics;
- a built installed helper/app on macOS;
- the structured test-mode artifact proving every live acceptance criterion;
- `PROJECT_STATUS.md` updated only after that evidence exists.
