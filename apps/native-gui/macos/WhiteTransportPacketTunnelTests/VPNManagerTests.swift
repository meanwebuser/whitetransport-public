import Foundation
import XCTest
@testable import WhiteTransportMacOS

final class VPNManagerTests: XCTestCase {
    func testLoadSelectsExactProviderBundleIdentifier() {
        let other = VPNTestBackend(providerBundleIdentifier: "com.example.other")
        let expected = VPNTestBackend(providerBundleIdentifier: "com.meanwebuser.whitetransport.packet-tunnel")
        let store = VPNTestStore(backends: [other, expected])
        let manager = VPNManager(
            providerBundleIdentifier: "com.meanwebuser.whitetransport.packet-tunnel",
            backendStore: store
        )
        let loaded = expectation(description: "loaded")
        manager.load { error in XCTAssertNil(error); loaded.fulfill() }
        wait(for: [loaded], timeout: 2)

        let started = expectation(description: "started")
        manager.start(configuration: vpnConfiguration()) { error in XCTAssertNil(error); started.fulfill() }
        wait(for: [started], timeout: 2)

        XCTAssertEqual(other.events, [])
        XCTAssertEqual(expected.events, ["observe", "configure", "save", "reload", "start"])
        XCTAssertEqual(store.makeCount, 0)
    }

    func testLoadPrefersActiveExactProviderOverStaleDuplicate() {
        let stale = VPNTestBackend(
            providerBundleIdentifier: "com.meanwebuser.whitetransport.packet-tunnel",
            systemState: .disconnected
        )
        let active = VPNTestBackend(
            providerBundleIdentifier: "com.meanwebuser.whitetransport.packet-tunnel",
            systemState: .connected
        )
        let manager = VPNManager(
            providerBundleIdentifier: "com.meanwebuser.whitetransport.packet-tunnel",
            backendStore: VPNTestStore(backends: [stale, active])
        )
        let loaded = expectation(description: "loaded active duplicate")
        manager.load { error in XCTAssertNil(error); loaded.fulfill() }
        wait(for: [loaded], timeout: 2)

        let started = expectation(description: "started active duplicate")
        manager.start(configuration: vpnConfiguration()) { error in XCTAssertNil(error); started.fulfill() }
        wait(for: [started], timeout: 2)

        XCTAssertEqual(stale.events, [])
        XCTAssertEqual(active.events, ["observe", "configure", "save", "reload", "start"])
    }

    func testLoadRejectsMultipleActiveExactProviders() {
        let first = VPNTestBackend(providerBundleIdentifier: "provider.id", systemState: .connected)
        let second = VPNTestBackend(providerBundleIdentifier: "provider.id", systemState: .disconnecting)
        let manager = VPNManager(
            providerBundleIdentifier: "provider.id",
            backendStore: VPNTestStore(backends: [first, second])
        )
        let loaded = expectation(description: "rejected duplicate active managers")
        let result = VPNTestBox<Error?>(nil)

        manager.load { error in result.set(error); loaded.fulfill() }
        wait(for: [loaded], timeout: 2)

        guard let error = result.value as? VPNManagerError, case .invalidConfiguration = error else {
            XCTFail("expected invalidConfiguration for duplicate active managers, got \(String(describing: result.value))")
            return
        }
        XCTAssertEqual(first.events, [])
        XCTAssertEqual(second.events, [])
    }

    func testStartSavesReloadsThenStarts() {
        let backend = VPNTestBackend(providerBundleIdentifier: nil)
        let store = VPNTestStore(backends: [], madeBackend: backend)
        let manager = VPNManager(providerBundleIdentifier: "provider.id", backendStore: store)
        let loaded = expectation(description: "loaded")
        manager.load { _ in loaded.fulfill() }
        wait(for: [loaded], timeout: 2)

        let started = expectation(description: "started")
        manager.start(configuration: vpnConfiguration()) { error in XCTAssertNil(error); started.fulfill() }
        wait(for: [started], timeout: 2)

        XCTAssertEqual(backend.events, ["observe", "configure", "save", "reload", "start"])
    }

    func testPreferencePermissionFailureMapsToPermissionRequired() {
        let permissionError = NSError(domain: "NEVPNErrorDomain", code: 5)
        let store = VPNTestStore(loadError: permissionError)
        let manager = VPNManager(providerBundleIdentifier: "provider.id", backendStore: store)
        let loaded = expectation(description: "loaded")
        let result = VPNTestBox<Error?>(nil)

        manager.load { error in result.set(error); loaded.fulfill() }
        wait(for: [loaded], timeout: 2)

        XCTAssertEqual(result.value as? VPNManagerError, .permissionRequired)
        XCTAssertEqual(manager.state, .permissionRequired)
    }

    func testWailsPermissionReturnsPermissionResponseOnManagerLoadFailure() throws {
        let permissionError = NSError(domain: "NEVPNErrorDomain", code: 5)
        let manager = VPNManager(
            providerBundleIdentifier: "provider.id",
            backendStore: VPNTestStore(loadError: permissionError)
        )
        let host = WailsVPNBridgeHost(manager: manager)

        let response = host.permission()

        XCTAssertFalse(response.success)
        XCTAssertEqual(response.state, .permissionRequired)
        XCTAssertEqual(response.error, "Network Extension permission is required")
        let wire = try XCTUnwrap(JSONSerialization.jsonObject(with: JSONEncoder().encode(response)) as? [String: Any])
        XCTAssertEqual(wire["success"] as? Bool, false)
        XCTAssertEqual(wire["state"] as? String, "permission_required")
        XCTAssertEqual(wire["error"] as? String, "Network Extension permission is required")
    }

    func testStopMessageUsesBoundedFallbackWhenProviderDoesNotReply() {
        let backend = VPNTestBackend(providerBundleIdentifier: "provider.id")
        backend.holdProviderMessage = true
        let manager = VPNManager(
            providerBundleIdentifier: "provider.id",
            backendStore: VPNTestStore(backends: [backend]),
            providerStopGracePeriod: 0.03
        )
        let loaded = expectation(description: "loaded")
        manager.load { _ in loaded.fulfill() }
        wait(for: [loaded], timeout: 2)

        let stopped = expectation(description: "stopped")
        manager.stop { error in XCTAssertNil(error); stopped.fulfill() }
        XCTAssertEqual(backend.stopCount, 0, "system stop must allow the provider a bounded cleanup window")
        wait(for: [stopped], timeout: 2)

        XCTAssertEqual(backend.providerMessages.count, 1)
        XCTAssertEqual(try? ProviderMessageCodec.decode(backend.providerMessages[0]).command, .stop)
        XCTAssertEqual(backend.stopCount, 1)
        XCTAssertEqual(manager.systemVPNState, .disconnected)
        XCTAssertEqual(manager.state, .disconnected)
    }

    func testStopReplyTriggersOneSystemStopBeforeFallbackDeadline() throws {
        let backend = VPNTestBackend(providerBundleIdentifier: "provider.id")
        backend.providerResponse = try ProviderMessageCodec.encode(
            ProviderMessageResponse(success: true, state: .disconnected)
        )
        let manager = VPNManager(
            providerBundleIdentifier: "provider.id",
            backendStore: VPNTestStore(backends: [backend]),
            providerStopGracePeriod: 0.5
        )
        let loaded = expectation(description: "loaded")
        manager.load { _ in loaded.fulfill() }
        wait(for: [loaded], timeout: 2)

        let stopped = expectation(description: "stopped")
        manager.stop { error in XCTAssertNil(error); stopped.fulfill() }
        wait(for: [stopped], timeout: 2)

        XCTAssertEqual(backend.stopCount, 1)
        XCTAssertEqual(manager.systemVPNState, .disconnected)
        XCTAssertEqual(manager.state, .disconnected)
    }

    func testStopCompletionWaitsForObservedSystemDisconnect() throws {
        let backend = VPNTestBackend(providerBundleIdentifier: "provider.id", systemState: .connected)
        backend.providerResponse = try ProviderMessageCodec.encode(
            ProviderMessageResponse(success: true, state: .disconnected)
        )
        let manager = VPNManager(
            providerBundleIdentifier: "provider.id",
            backendStore: VPNTestStore(backends: [backend]),
            providerStopGracePeriod: 0.5,
            systemStopTimeout: 0.5
        )
        let loaded = expectation(description: "loaded")
        manager.load { error in XCTAssertNil(error); loaded.fulfill() }
        wait(for: [loaded], timeout: 2)

        let stopped = expectation(description: "stopped after observed disconnect")
        let completionCount = VPNTestBox(0)
        manager.stop { error in
            XCTAssertNil(error)
            completionCount.set(1)
            stopped.fulfill()
        }
        waitUntil { backend.stopCount == 1 }
        XCTAssertEqual(completionCount.value, 0)
        XCTAssertEqual(manager.systemVPNState, .disconnecting)

        backend.emitSystemState(.disconnected)
        wait(for: [stopped], timeout: 2)
        XCTAssertEqual(manager.systemVPNState, .disconnected)
    }

    func testStopFailsWhenSystemDisconnectIsNotObservedBeforeTimeout() throws {
        let backend = VPNTestBackend(providerBundleIdentifier: "provider.id", systemState: .connected)
        backend.providerResponse = try ProviderMessageCodec.encode(
            ProviderMessageResponse(success: true, state: .disconnected)
        )
        let manager = VPNManager(
            providerBundleIdentifier: "provider.id",
            backendStore: VPNTestStore(backends: [backend]),
            providerStopGracePeriod: 0.5,
            systemStopTimeout: 0.03
        )
        let loaded = expectation(description: "loaded")
        manager.load { error in XCTAssertNil(error); loaded.fulfill() }
        wait(for: [loaded], timeout: 2)

        let stopped = expectation(description: "bounded stop failure")
        manager.stop { error in
            guard let vpnError = error as? VPNManagerError, case .stopFailed = vpnError else {
                XCTFail("expected explicit stopFailed timeout, got \(String(describing: error))")
                stopped.fulfill()
                return
            }
            stopped.fulfill()
        }
        wait(for: [stopped], timeout: 2)

        XCTAssertEqual(backend.stopCount, 1)
        XCTAssertEqual(manager.systemVPNState, .error)
    }

    func testWailsStopReturnsDisconnectedProviderStateAndExactIdentity() throws {
        let backend = VPNTestBackend(providerBundleIdentifier: "com.meanwebuser.whitetransport.packet-tunnel")
        backend.providerResponse = try ProviderMessageCodec.encode(
            ProviderMessageResponse(success: true, state: .disconnected)
        )
        let manager = VPNManager(
            providerBundleIdentifier: "com.meanwebuser.whitetransport.packet-tunnel",
            backendStore: VPNTestStore(backends: [backend]),
            providerStopGracePeriod: 0.5
        )
        let identity = RuntimeProfileIdentity(
            daemonInstanceID: "daemon-stop",
            profileRevision: 31,
            profileHash: String(repeating: "9", count: 64),
            sessionID: "session-stop"
        )
        let validUntil = Date(timeIntervalSince1970: 2_000_000_000)
        let host = WailsVPNBridgeHost(
            manager: manager,
            expectedLease: RuntimeProfileLease(identity: identity, validUntil: validUntil)
        )

        let response = host.stop()

        XCTAssertEqual(backend.stopCount, 1)
        XCTAssertTrue(response.success)
        XCTAssertEqual(response.state, .disconnected)
        XCTAssertEqual(response.providerState, .disconnected)
        XCTAssertEqual(response.daemonInstanceID, identity.daemonInstanceID)
        XCTAssertEqual(response.profileRevision, identity.profileRevision)
        XCTAssertEqual(response.profileHash, identity.profileHash)
        XCTAssertEqual(response.sessionID, identity.sessionID)
        XCTAssertEqual(response.profileValidUntil, validUntil)
        XCTAssertFalse(response.providerStatusMatched)

        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        let wire = try XCTUnwrap(JSONSerialization.jsonObject(with: encoder.encode(response)) as? [String: Any])
        XCTAssertEqual(wire["state"] as? String, "disconnected")
        XCTAssertEqual(wire["provider_state"] as? String, "disconnected")
        XCTAssertEqual(wire["daemon_instance_id"] as? String, identity.daemonInstanceID)
        XCTAssertEqual((wire["profile_revision"] as? NSNumber)?.uint64Value, identity.profileRevision)
        XCTAssertEqual(wire["profile_hash"] as? String, identity.profileHash)
        XCTAssertEqual(wire["session_id"] as? String, identity.sessionID)
        XCTAssertEqual(wire["profile_valid_until"] as? String, "2033-05-18T03:33:20Z")
    }

    func testWailsStartRejectsDifferentActiveLeaseBeforeConfigure() throws {
        let backend = VPNTestBackend(
            providerBundleIdentifier: "com.meanwebuser.whitetransport.packet-tunnel",
            systemState: .connected
        )
        let manager = VPNManager(
            providerBundleIdentifier: "com.meanwebuser.whitetransport.packet-tunnel",
            backendStore: VPNTestStore(backends: [backend])
        )
        let activeConfiguration = vpnConfiguration()
        let activeLease = RuntimeProfileLease(
            identity: activeConfiguration.profileIdentity,
            validUntil: activeConfiguration.profileValidUntil
        )
        let host = WailsVPNBridgeHost(manager: manager, expectedLease: activeLease)
        let replacement = vpnConfiguration(
            profileRevision: 5,
            sessionID: "session-replacement",
            profileValidUntil: Date(timeIntervalSince1970: 2_000_000_060)
        )
        let replacementJSON = try XCTUnwrap(String(
            data: PacketTunnelConfigurationCodec.encode(replacement),
            encoding: .utf8
        ))

        let response = host.start(configurationJSON: replacementJSON)

        XCTAssertFalse(response.success)
        XCTAssertTrue(response.error?.contains("stopped before replacement") ?? false)
        XCTAssertEqual(response.daemonInstanceID, activeLease.identity.daemonInstanceID)
        XCTAssertEqual(response.profileRevision, activeLease.identity.profileRevision)
        XCTAssertEqual(response.profileValidUntil, activeLease.validUntil)
        XCTAssertFalse(backend.events.contains("configure"))
        XCTAssertFalse(backend.events.contains("save"))
        XCTAssertFalse(backend.events.contains("start"))

        let renewedDeadline = vpnConfiguration(profileValidUntil: activeLease.validUntil.addingTimeInterval(60))
        let renewedJSON = try XCTUnwrap(String(
            data: PacketTunnelConfigurationCodec.encode(renewedDeadline),
            encoding: .utf8
        ))
        let renewalResponse = host.start(configurationJSON: renewedJSON)
        XCTAssertFalse(renewalResponse.success, "a freshness-only lease change still requires confirmed stop -> start")
        XCTAssertEqual(renewalResponse.profileValidUntil, activeLease.validUntil)
        XCTAssertFalse(backend.events.contains("configure"))

        let providerRecord = AppGroupStatusRecord(
            schemaVersion: WhiteTransportAppGroup.schemaVersion,
            sequence: 1,
            updatedAt: Date(timeIntervalSince1970: 1_999_999_900),
            status: ConnectionStatus(
                state: .connected,
                transport: .connected,
                systemVPN: .connected,
                providerState: .connected,
                profileIdentity: activeLease.identity,
                profileValidUntil: activeLease.validUntil
            )
        )
        let restartedHost = WailsVPNBridgeHost(manager: manager, statusReader: { providerRecord })
        let restartedResponse = restartedHost.start(configurationJSON: replacementJSON)
        XCTAssertFalse(restartedResponse.success, "process restart must recover the active provider lease before replacement")
        XCTAssertEqual(restartedResponse.profileValidUntil, activeLease.validUntil)
        XCTAssertFalse(backend.events.contains("configure"))
    }

    private func waitUntil(_ predicate: () -> Bool, timeout: TimeInterval = 2) {
        let deadline = Date().addingTimeInterval(timeout)
        while !predicate(), Date() < deadline { RunLoop.current.run(until: Date().addingTimeInterval(0.01)) }
        XCTAssertTrue(predicate())
    }
}

private final class VPNTestStore: VPNManagerBackendStore, @unchecked Sendable {
    private let backends: [any VPNManagerBackend]
    private let madeBackend: any VPNManagerBackend
    private let loadError: Error?
    private(set) var makeCount = 0

    init(
        backends: [any VPNManagerBackend] = [],
        madeBackend: any VPNManagerBackend = VPNTestBackend(providerBundleIdentifier: nil),
        loadError: Error? = nil
    ) {
        self.backends = backends
        self.madeBackend = madeBackend
        self.loadError = loadError
    }

    func loadAll(completion: @escaping @Sendable ([any VPNManagerBackend]?, Error?) -> Void) {
        completion(loadError == nil ? backends : nil, loadError)
    }

    func makeManager() -> any VPNManagerBackend {
        makeCount += 1
        return madeBackend
    }
}

private final class VPNTestBackend: VPNManagerBackend, @unchecked Sendable {
    private let lock = NSLock()
    let providerBundleIdentifier: String?
    var providerResponse: Data?
    var holdProviderMessage = false
    private var currentSystemState: SystemVPNState
    private var statusObserver: (@Sendable (SystemVPNState) -> Void)?
    private(set) var events: [String] = []
    private(set) var providerMessages: [Data] = []
    private(set) var stopCount = 0

    init(providerBundleIdentifier: String?, systemState: SystemVPNState = .disconnected) {
        self.providerBundleIdentifier = providerBundleIdentifier
        self.currentSystemState = systemState
    }

    var systemState: SystemVPNState { locked { currentSystemState } }

    func observeStatus(_ observer: @escaping @Sendable (SystemVPNState) -> Void) {
        locked {
            events.append("observe")
            statusObserver = observer
        }
    }

    func emitSystemState(_ state: SystemVPNState) {
        let observer = locked { () -> (@Sendable (SystemVPNState) -> Void)? in
            currentSystemState = state
            return statusObserver
        }
        observer?(state)
    }

    func configure(
        configuration: PacketTunnelConfiguration,
        providerBundleIdentifier: String,
        localizedDescription: String
    ) throws {
        locked { events.append("configure") }
    }

    func save(completion: @escaping @Sendable (Error?) -> Void) {
        locked { events.append("save") }
        completion(nil)
    }

    func reload(completion: @escaping @Sendable (Error?) -> Void) {
        locked { events.append("reload") }
        completion(nil)
    }

    func start() throws { locked { events.append("start") } }

    func stop() {
        locked {
            events.append("stop")
            stopCount += 1
        }
    }

    func sendProviderMessage(_ data: Data, completion: @escaping @Sendable (Data?) -> Void) throws {
        locked { providerMessages.append(data) }
        if !holdProviderMessage { completion(providerResponse) }
    }

    private func locked<Value>(_ body: () -> Value) -> Value {
        lock.lock()
        defer { lock.unlock() }
        return body()
    }
}

private final class VPNTestBox<Value>: @unchecked Sendable {
    private let lock = NSLock()
    private var storage: Value

    init(_ value: Value) { storage = value }

    var value: Value {
        lock.lock()
        defer { lock.unlock() }
        return storage
    }

    func set(_ value: Value) {
        lock.lock()
        storage = value
        lock.unlock()
    }
}

private func vpnConfiguration(
    profileRevision: UInt64 = 4,
    sessionID: String = "session-vpn-manager",
    profileValidUntil: Date = Date(timeIntervalSince1970: 2_000_000_000)
) -> PacketTunnelConfiguration {
    PacketTunnelConfiguration(
        remoteAddress: "198.18.0.1",
        daemonInstanceID: "daemon-vpn-manager",
        profileRevision: profileRevision,
        profileHash: String(repeating: "f", count: 64),
        sessionID: sessionID,
        profileValidUntil: profileValidUntil,
        socksEndpoint: try! RuntimeLoopbackSocksEndpoint(confirmedAddress: "127.0.0.1:1080"),
        routeMode: .fullTunnel,
        destinationCIDRs: [],
        bypass: BypassSet(
            requiredHosts: ["control.example.test"],
            resolvedCIDRs: ["198.51.100.7/32"],
            resolvedCIDRsByHost: ["control.example.test": ["198.51.100.7/32"]],
            sourceEndpoints: ["https://control.example.test/api"],
            resolutionComplete: true
        ),
        dns: DNSConfiguration(servers: ["1.1.1.1"])
    )
}
