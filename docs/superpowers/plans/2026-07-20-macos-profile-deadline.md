# macOS Profile Deadline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development when a task can be isolated; keep the cross-layer Swift lifecycle integration under one owner.

**Goal:** Fail closed when an authoritative macOS System VPN profile or any dependency DNS snapshot expires, and require an explicit completed Stop before replacing an active runtime-profile identity.

**Architecture:** The native Go projection computes one required `profile_valid_until` as the earliest of `SystemVPNProfile.ExpiresAt` and every dependency `DNSExpiresAt`. Swift decodes that ISO-8601 instant into the immutable packet-tunnel configuration, validates it against an injected clock, persists it with provider status, and schedules one cancellable expiry callback bound to the active lifecycle generation. Expiry uses the existing serialized cleanup path to stop the bridge, clear Network Extension settings once, write error state, and cancel the tunnel; profile replacement remains `stop -> disconnected -> start`, never an in-place update.

**Tech Stack:** Go 1.24, Swift 5.9, NetworkExtension, XCTest, App Group JSON, Wails C ABI JSON.

## Global Constraints

- Work only under the retained `.agents/locks/macos-extension.lock`; leave all files unstaged and uncommitted for root integration.
- Do not edit `PROJECT_STATUS.md` or unrelated Go/Wails runtime files.
- Add regression tests before implementation and run every non-Xcode proof available locally; record the external Mac/Xcode blocker exactly.
- `profile_valid_until` is required and equals the minimum of the profile expiry and every dependency DNS expiry.
- Missing, malformed, or expired deadlines fail closed; no compatibility default or optional fallback is allowed.
- Expiry cleanup must reject stale callbacks by lifecycle generation, stop the bridge, clear settings at most once, persist error state, and cancel the tunnel.
- A different active identity must return `operationInProgress` until Stop has completed; no provider `update` command is introduced.

---

### Task 1: Native Go deadline projection

**Files:**
- Modify: `apps/native-gui/system_vpn_native.go`
- Test: `apps/native-gui/system_vpn_native_test.go`

**Interfaces:**
- Consumes: validated `runtimeapi.SystemVPNProfile.ExpiresAt` and `SystemVPNDependency.DNSExpiresAt`.
- Produces: `macOSPacketTunnelConfiguration.ProfileValidUntil time.Time` encoded as required `profile_valid_until` RFC3339 JSON.

- [ ] Add a failing projection test that gives the profile and dependencies different future deadlines and asserts the exact earliest instant in decoded extension JSON.
- [ ] Run `cd apps/native-gui && go test . -run 'TestBuildMacOSPacketTunnelConfiguration.*ProfileValidUntil' -count=1`; expect failure because the field is absent.
- [ ] Add a typed JSON field and compute the minimum only after `profile.Validate(now)` succeeds.
- [ ] Re-run the focused Go test; expect pass.

### Task 2: Required Swift configuration and status schema

**Files:**
- Modify: `apps/native-gui/macos/WhiteTransportPacketTunnel/PacketTunnelConfiguration.swift`
- Modify: `apps/native-gui/macos/Shared/ConnectionContract.swift`
- Modify: `apps/native-gui/macos/WhiteTransportVPNControl/WailsVPNBridge.swift`
- Test: `apps/native-gui/macos/WhiteTransportPacketTunnelTests/PacketTunnelConfigurationTests.swift`
- Test: `apps/native-gui/macos/WhiteTransportPacketTunnelTests/AppGroupStatusStoreTests.swift`
- Test: `apps/native-gui/macos/WhiteTransportPacketTunnelTests/VPNManagerTests.swift`

**Interfaces:**
- Consumes: required ISO-8601 `profile_valid_until` from Go.
- Produces: `PacketTunnelConfiguration.profileValidUntil: Date`, `ConnectionStatus.profileValidUntil: Date?`, and `WailsVPNBridgeResponse.profileValidUntil: Date?`, each encoded with `profile_valid_until`.

- [ ] Extend the exact Go JSON fixture with `profile_valid_until`; add missing/malformed decode rejection and `validated(now:)` expiry rejection tests.
- [ ] Add App Group round-trip and Wails response wire assertions for the exact deadline.
- [ ] Run the focused Swift tests on macOS; expect failures because the field and validation error do not exist.
- [ ] Add `invalidProfileValidityDeadline` and `expiredRuntimeProfile` errors, a required configuration field, explicit ISO-8601 decoders/encoders at Go/Wails and provider preference boundaries, and optional status/response propagation for states with no active profile.
- [ ] Re-run focused tests; expect pass.

### Task 3: Generation-bound provider expiry and explicit replacement

**Files:**
- Modify: `apps/native-gui/macos/WhiteTransportPacketTunnel/PacketTunnelProviderLifecycle.swift`
- Modify: `apps/native-gui/macos/WhiteTransportVPNControl/WailsVPNBridge.swift`
- Test: `apps/native-gui/macos/WhiteTransportPacketTunnelTests/PacketTunnelProviderLifecycleTests.swift`
- Test: `apps/native-gui/macos/WhiteTransportPacketTunnelTests/VPNManagerTests.swift`

**Interfaces:**
- Consumes: a validated future `PacketTunnelConfiguration.profileValidUntil` and its exact identity.
- Produces: one scheduled expiry cancellation handle per connected generation and a host-side active-identity guard.

- [ ] Add a deterministic injected expiry scheduler fake. Test that active expiry records an error, stops the bridge once, clears settings once, completes cleanup, and invokes `cancelTunnel` once.
- [ ] Test that a cancelled callback from a stopped generation cannot affect a later start.
- [ ] Test that `WailsVPNBridgeHost.start` rejects a different active identity before preference save/start and accepts it only after successful Stop clears active identity.
- [ ] Run the focused Swift tests on macOS; expect failures before the scheduler/guard exist.
- [ ] Schedule only after settings and bridge start succeed; capture generation, cancel the prior task during every cleanup, and route a matching expiry through existing `beginCleanupLocked(finalState: .error, cancelError:)`.
- [ ] Add an explicit active-identity precondition in the host before `generationGuard.accept` and mutate expected identity only after a successful Start/Stop boundary.
- [ ] Re-run focused and full Swift suites; expect pass.

### Task 4: Acceptance and audit trail

**Files:**
- Modify: `apps/native-gui/macos/README.md`
- Modify: `.agents/worklog.md`

**Interfaces:**
- Consumes: focused Go/Swift results and independent critic findings.
- Produces: exact operator-facing deadline/replacement contract and evidence/blockers.

- [ ] Run `cd apps/native-gui && go test . -run 'Test.*SystemVPN.*' -count=1` and `go test . -count=1`.
- [ ] Run focused Swift tests and the full Swift suite on macOS when available; otherwise record the missing Swift/Xcode environment without polling an unavailable host.
- [ ] Run focused whitespace/source checks and `./secrets/detect-secrets.sh fast high` only if root requests commit readiness.
- [ ] Apply independent critic findings that expose an unproved boundary; document remaining external Mac proof.
- [ ] Keep all changes unstaged/uncommitted and retain the lock for root.
