# Unified Native VPN GUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use `subagent-driven-development` task by task. Each runtime defect follows the mandatory red-green loop before production edits.

**Goal:** Ship one simple Home / Endpoints / Settings experience on Android and desktop, backed by the canonical Go runtime, with honest system-VPN state, destination split routing, persistent redacted logs, and deterministic test-mode results. macOS is the first desktop release gate.

**Architecture:** Keep Wails as the desktop host and Capacitor as the Android host. Reuse `apps/client-web` as the shared React presentation layer through a typed host adapter. Keep the Go daemon as transport truth. Android starts its existing `TunnelVpnService` only after the Go data plane is ready. macOS uses a signed Network Extension packet-tunnel system extension; the extension owns the system route and forwards packets through the selected WhiteTransport session. Do not revive Electron, copy the legacy upstream GUI wholesale, or report a local SOCKS listener as a connected VPN.

**Tech stack:** Go 1.24, Wails v2, React 19/Vite/TypeScript, Capacitor 7/Kotlin, Android `VpnService`, Swift 6/NetworkExtension, the existing MIT `xjasonlyu/tun2socks` v2.6.0 engine behind a public-API packet-flow bridge, XCTest/Gradle/Go/Node tests, Playwright/Appium smoke tests.

## Global constraints and proof boundary

- `PROJECT_STATUS.md` is the only readiness source. Update it only after the corresponding proof gate passes.
- Every production artifact embeds a client-safe TokenStore plus the platform sing-box runtime. Never copy an unrestricted production TokenStore into a client package.
- Unified connection state is `disconnected -> permission_required -> connecting -> connected -> degraded|disconnecting|error`. `connected` requires carrier session, data plane, and OS VPN to be active.
- Android supports full tunnel plus per-app `bypass` and `only` modes through `VpnService` application rules.
- macOS v1 supports full tunnel and destination/domain exclusions. Per-app VPN requires managed deployment and is not presented as generally available.
- Unit tests prove contracts only. Release readiness additionally requires an installed app, a system VPN interface, a nonce-bearing payload through that interface, route cleanup, and crash cleanup.
- Keep logs redacted at a stable platform path. Test mode must auto-connect, write one atomic JSON result to an absolute requested path, record the log path, and disconnect on every exit path.

---

## Task 1: Close false-positive runtime and packaging-security boundaries

**Files:**
- Modify: `apps/native-gui/internal/runtime/config_generator.go`
- Modify: `apps/native-gui/internal/runtime/config_generator_test.go`
- Modify: `apps/native-gui/internal/runtime/models.go`
- Modify: `apps/native-gui/internal/runtime/service.go`
- Modify: `apps/native-gui/internal/runtime/service_test.go`
- Modify: `ops/config/filter-client-token-store.py`
- Test: `ops/config/test_filter_client_token_store.py`

**Steps:**
1. Add a failing Go regression proving that the daemon-owned SOCKS listener and any local routing-proxy listener cannot share an address, and that telemetry cannot promote a transport-only state to system-VPN connected.
2. Run `cd apps/native-gui && /usr/local/go/bin/go test ./internal/runtime -run 'Test.*(Listen|VPNState|Telemetry)' -count=1` and record the RED symptom.
3. Allocate independent loopback ports and expose transport state separately from `SystemVPNState` and `ConnectionState`.
4. Add failing Python fixtures containing node/admin/provider credentials and assert that client filtering rejects them while keeping explicit client/bootstrap bindings.
5. Run `python3 -m unittest ops.config.test_filter_client_token_store` and record RED, then implement an explicit role/connection/lifecycle allowlist with hard failure for ambiguous bindings.
6. Run the focused GREEN tests, `./secrets/generate-token-store.sh --check`, and `./secrets/detect-secrets.sh fast high`.

## Task 2: Define and implement the shared host contract

**Files:**
- Modify: `apps/client-web/src/native/wt-transport.ts`
- Create: `apps/client-web/src/native/wails-transport.ts`
- Create: `apps/client-web/src/native/wt-transport.test.ts`
- Modify: `apps/client-web/src/App.tsx`
- Modify: `apps/native-gui/app.go`
- Modify: `apps/native-gui/app_test.go`

**Steps:**
1. Add Vitest and failing contract tests for host detection, server listing, state mapping, permission-required state, logs, capabilities, split settings, and system-VPN start/stop.
2. Extend the TypeScript contract with `getCapabilities`, `getConnectionState`, `requestVPNPermission`, `startSystemVPN`, `stopSystemVPN`, `getSplitRouting`, `setSplitRouting`, `getLogInfo`, and existing transport operations.
3. Implement explicit Capacitor and Wails adapters. Do not synthesize success for unsupported host methods; return a typed unsupported capability.
4. Extend the Wails binding with product-level connection state and log/capability methods. Keep raw runtime details behind the adapter.
5. Run `npm --prefix apps/client-web test -- --run`, `npm --prefix apps/client-web run typecheck`, and `cd apps/native-gui && /usr/local/go/bin/go test ./...`.

## Task 3: Build the simple shared Home / Endpoints / Settings shell

**Files:**
- Create: `apps/client-web/src/components/shell/app-shell.tsx`
- Create: `apps/client-web/src/components/shell/home-screen.tsx`
- Create: `apps/client-web/src/components/shell/endpoints-screen.tsx`
- Create: `apps/client-web/src/components/shell/settings-screen.tsx`
- Create: `apps/client-web/src/components/shell/bottom-navigation.tsx`
- Create: `apps/client-web/src/components/shell/app-shell.test.tsx`
- Modify: `apps/client-web/src/App.tsx`
- Modify: `apps/client-web/src/globals.css`

**Steps:**
1. Write failing interaction tests from the operator flow: large power button, selected endpoint directly underneath, bottom Home / Endpoints / Settings navigation, permission and error states, split-routing controls, log reveal/copy, and advanced diagnostics collapsed by default.
2. Implement a compact responsive shell. Preserve the existing Android visual language, remove dashboard clutter from Home, and use one information hierarchy on narrow and wide layouts.
3. Keep node discovery and manual endpoint editing on Endpoints; keep split routing, logs, test-mode information, DNS and kill-switch capability indicators on Settings.
4. Run unit/type tests, then use Playwright at phone and desktop viewports. Save before/after screenshots under `output/playwright/` for review; do not treat screenshots as runtime proof.

## Task 4: Make Android Capacitor drive the real VpnService

**Files:**
- Modify: `apps/android/whitelist-bypass-client/app/src/main/java/bypass/whitelist/CapacitorMainActivity.kt`
- Modify: `apps/android/whitelist-bypass-client/app/src/main/java/bypass/whitelist/WtTransportPlugin.kt`
- Create: `apps/android/whitelist-bypass-client/app/src/main/java/bypass/whitelist/CapacitorVpnCoordinator.kt`
- Modify: `apps/android/whitelist-bypass-client/app/src/main/java/bypass/whitelist/tunnel/TunnelVpnService.kt`
- Modify: `apps/android/whitelist-bypass-client/app/src/main/java/bypass/whitelist/util/Prefs.kt`
- Create: `apps/android/whitelist-bypass-client/app/src/test/java/bypass/whitelist/CapacitorVpnCoordinatorTest.kt`
- Modify: `apps/android/whitelist-bypass-client/app/src/androidTest/java/bypass/whitelist/RuntimeLaunchInstrumentedTest.kt`

**Steps:**
1. Add failing coordinator tests for consent-required, Go connect failure, VpnService failure rollback, connected only after `TUNNEL_ACTIVE`, split mode persistence, ordered disconnect, and process restart reconciliation.
2. Replace `setMode` no-op with explicit proxy/tunnel selection and real split settings. Route permission through the activity result API and return the final outcome to the originating plugin call.
3. Connect in order: permission -> Go runtime/session -> verified SOCKS payload -> foreground `TunnelVpnService` -> active TUN state. Roll back in reverse order on every failure.
4. Disconnect in order: stop TUN -> stop transport -> publish disconnected state. Ensure app-only modes use `addAllowedApplication`/`addDisallowedApplication` from persisted package sets.
5. Extend instrumentation to assert the launcher activity invokes the coordinator, the VPN consent path is reachable, and the service state is reflected to React.
6. Run the focused JVM test, `./gradlew :app:testDebugUnitTest`, and the available emulator/device instrumentation lane.

## Task 5: Package the shared React UI in Wails and Android

**Files:**
- Create: `apps/native-gui/package.json`
- Modify: `apps/native-gui/wails.json`
- Modify: `apps/native-gui/main.go`
- Modify: root `package.json`
- Modify: `apps/client-web/vite.config.ts`
- Regenerate: `apps/android/whitelist-bypass-client/app/src/main/assets/public/**`
- Regenerate: Wails frontend distribution selected by `go:embed`

**Steps:**
1. Add failing build smoke tests that assert both hosts package the same UI build marker and that Wails has a functioning root build command.
2. Make `apps/client-web` the single source build. Copy its immutable Vite output into each host through explicit scripts; do not hand-edit generated assets.
3. Keep host selection runtime-based (`window.go.main.App` for Wails, Capacitor plugin for Android) and fail clearly in an unsupported browser.
4. Run `npm run build:client`, Android `cap copy`, Wails build, then verify a real hashed JS chunk and icon exist in both packages.

## Task 6: Add UDP to the WhiteTransport egress contract

**Files:**
- Modify: `core/go/internal/proxy/socks5.go`
- Modify: `core/go/internal/proxy/socks5_test.go`
- Modify: `core/go/internal/session/egress.go`
- Create: `core/go/internal/session/packet_egress.go`
- Create: `core/go/internal/session/packet_egress_test.go`
- Modify: the active carrier envelope/session implementation selected by the RED test
- Modify: `core/go/tests/test-local.sh`

**Steps:**
1. Add `TestServerUDPAssociateThroughCarrier`: send SOCKS command `0x03`, a framed UDP nonce, and require the echo through a fake packet carrier. Record the current `unsupported socks command 3` RED.
2. Add an explicit packet-egress interface returning `net.PacketConn`; never translate UDP into a direct host fallback. Define bounded association lifetime, flow/session identity, destination/source metadata, and reverse datagrams.
3. Implement SOCKS5 UDP ASSOCIATE and the local UDP relay with RFC-compatible framing, fragmentation rejection, timeouts, cancellation, and cleanup.
4. Carry UDP envelopes through the selected WhiteTransport session and open the exit-node UDP socket. Preserve the same encrypted fabric/session boundary as TCP.
5. Run focused unit/integration GREEN, then extend the deterministic two-daemon local test with UDP echo and DNS-shaped payloads. A TCP-only pass does not unlock a macOS default route.

## Task 7: Implement macOS Network Extension system-VPN control

**Files:**
- Create: `apps/native-gui/macos/WhiteTransport.xcodeproj/**`
- Create: `apps/native-gui/macos/WhiteTransportPacketTunnel/PacketTunnelProvider.swift`
- Create: `apps/native-gui/macos/WhiteTransportPacketTunnel/PacketTunnelConfiguration.swift`
- Create: `apps/native-gui/macos/WhiteTransportPacketTunnel/PacketFlowBridge.swift`
- Create: `apps/native-gui/macos/WhiteTransportVPNControl/VPNManager.swift`
- Create: `apps/native-gui/macos/Shared/ConnectionContract.swift`
- Create: `apps/native-gui/macos/WhiteTransportPacketTunnelTests/**`
- Create: `core/go/mobileapple/packet_bridge.go`
- Refactor: `core/go/internal/tunbridge/engine.go`
- Modify: `apps/native-gui/app.go`
- Modify: `apps/native-gui/internal/runtime/resources.go`
- Modify: `apps/native-gui/build/package-macos.sh`

**Steps:**
1. Reuse the pinned MIT `github.com/xjasonlyu/tun2socks/v2` v2.6.0 engine. Do not link GPL `Libbox` or use undocumented `packetFlow.socket.fileDescriptor` KVC.
2. Add failing Swift tests for configuration validation, full/destination-split route generation, provider/control-plane exclusions, DNS, extension messages, state mapping, and cleanup.
3. Implement a public-API `PacketFlowBridge`: Swift owns `NEPacketTunnelFlow`; one IP packet maps to one nonblocking `AF_UNIX/SOCK_DGRAM` socketpair datagram; pass `dup(engineFD)` to a macOS gomobile XCFramework with offset zero; map reverse packets to `AF_INET`/`AF_INET6` by IP-version nibble. Pause on `EAGAIN`; fail the tunnel on bounded-queue overflow or `writePackets == false` instead of dropping packets.
4. Add the system extension target and the Wails-side manager. `startTunnel` must install network settings and start the real packet bridge before completing; it must complete with an error on any partial startup.
5. Keep provider discovery/control outside the claimed tunnel routes to avoid recursive self-capture. Resolve every bypass host to `/32` or `/128` before installing settings; fail closed when the bypass set is incomplete.
6. Add App Group state/log exchange and `NETunnelProviderSession.sendProviderMessage` for status and clean shutdown. Redact credentials and endpoint secrets.
7. Package the signed system extension, daemon, client-safe TokenStore, and sing-box runtime. Direct distribution requires the Network Extension entitlement, Developer ID identity, and provisioning profile; an ad-hoc build is development evidence only.
8. Run Swift unit tests and `xcodebuild` compile first. Then run the installed-app TCP+UDP proof gate only after valid signing/provisioning is available.

## Task 8: Preserve and harden deterministic GUI test mode

**Files:**
- Modify: `apps/native-gui/launch.go`
- Modify: `apps/native-gui/launch_test.go`
- Modify: `apps/android/whitelist-bypass-client/app/src/main/java/bypass/whitelist/GuiTestLaunch.kt`
- Create: `apps/android/whitelist-bypass-client/app/src/test/java/bypass/whitelist/GuiTestLaunchTest.kt`
- Create: `docs/testing/native-gui-test-mode.md`

**Steps:**
1. Make this task depend on the Android product coordinator from Task 4 and the macOS VPN manager from Task 7. The runners must not assemble their own transport-only path.
2. Add failing tests that require absolute result paths, atomic replacement, mode `0600` where supported, schema version, run ID, selected endpoint, authoritative transport/system-VPN states, system-route nonce proof, per-run log path, timing, failure stage, and cleanup result.
3. Standardize `vpn-e2e` inputs: Mac receives explicit `--wt-test-run-id`, absolute result/log paths, timeout, probe URL, nonce, node, and exit flags; Android receives equivalent explicit Intent extras. Keep the old fake/Xvfb lane named and reported only as `gui-wiring`.
4. Make test mode execute the same public connect/disconnect path as the button. `passed=true` requires transport connected, OS VPN connected, a nonce response through the ordinary system route without an explicit SOCKS client, and successful reverse cleanup. Permission-required, unsupported system VPN, nonce mismatch, timeout, or partial cleanup fail closed.
5. On every exit path, run bounded reverse rollback: stop OS VPN, disconnect transport, then stop any owned daemon. Write one atomic result after cleanup and use deterministic Mac exit codes (`0` pass, `1` test failure, `2` contract/parse error, `124` timeout). Android must finish the Activity only after service cleanup and result replacement completes.
6. Use stable logs: normal macOS logs remain under `~/Library/Logs/WhiteTransport/`; each test gets its explicit absolute JSONL path; Android writes per-run `result.json` and `run.jsonl` below app-private `files/white-transport-test-results/<run-id>/`. Redact daemon output, errors, endpoints, and credentials before persistence.
7. Run both platform unit tests and deterministic installed-app smokes. Until the Network Extension is provisioned, the Mac `vpn-e2e` lane must produce a truthful failed result at `system-vpn` with successful cleanup; source/CLI/atomic-log tests are not VPN readiness.

## Task 9: Release proof, review, and truthful status

**Files:**
- Modify only after gates: `PROJECT_STATUS.md`
- Modify: `.agents/worklog.md`
- Create: `artifacts/test-results/native-gui-<platform>-<timestamp>.json` as ignored runtime evidence

**Steps:**
1. Run `desktop-local-fast` and prove payload transfer through the daemon path.
2. On Android, install the APK, grant VPN consent, verify the OS VPN indicator/interface, transfer a nonce-bearing payload from a separate helper process without an explicit SOCKS client, exercise one split rule, stop, and confirm route cleanup. Repeat after force-stopping or killing the GUI and require TUN/routes cleanup.
3. On macOS, install the signed app, approve the Network Extension, verify the system VPN/interface, transfer a nonce-bearing payload from an ordinary process with a changed/expected exit path, exercise one destination exclusion, stop, and confirm route and extension cleanup. Repeat after killing the GUI.
4. Run provider smoke only after local gates. A provider failure remains a provider/deployment blocker and does not get relabelled as a GUI success.
5. Request independent implementation review and proof-boundary criticism. Fix all P0/P1 findings.
6. Update `PROJECT_STATUS.md` with only demonstrated states, run secret scan, commit logical units, and attach complete signed binaries only to an actual release.

## Current external gate

The connected Mac currently has Xcode and an Apple Development identity, but no installed Network Extension provisioning profile and no Developer ID identity. Source, unit tests, unsigned compile, shared UI, logs, and test-mode work can proceed. A directly distributed, working macOS system VPN cannot be marked ready until Apple signing/provisioning is obtained and the installed-app proof gate passes.
