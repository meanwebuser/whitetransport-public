import Foundation
import XCTest
@testable import WhiteTransportMacOS

final class PacketTunnelProviderLifecycleTests: XCTestCase {
    func testDuplicateStartSharesOneGenerationAndCompletesEachCallerOnce() throws {
        let applyCalled = expectation(description: "settings apply")
        let settings = TestSettingsDriver(onApply: { applyCalled.fulfill() })
        let bridge = TestLifecycleBridge()
        let factory = TestBridgeFactory(bridge: bridge)
        let lifecycle = PacketTunnelProviderLifecycle(settings: settings, bridgeFactory: factory)
        let starts = expectation(description: "both start completions")
        starts.expectedFulfillmentCount = 2
        let results = LockedBox<[String]>([])

        lifecycle.start(configuration: strictConfiguration()) { error in
            results.mutate { $0.append(error.map(String.init(describing:)) ?? "ok") }
            starts.fulfill()
        }
        lifecycle.start(configuration: strictConfiguration()) { error in
            results.mutate { $0.append(error.map(String.init(describing:)) ?? "ok") }
            starts.fulfill()
        }
        wait(for: [applyCalled], timeout: 2)
        settings.completeApply(nil)
        wait(for: [starts], timeout: 2)

        XCTAssertEqual(results.value, ["ok", "ok"])
        XCTAssertEqual(settings.applyCount, 1)
        XCTAssertEqual(factory.makeCount, 1)
        XCTAssertEqual(bridge.startCount, 1)
        XCTAssertEqual(lifecycle.state, .connected)
    }

    func testStopDuringSettingsApplyWaitsForApplyThenClearsExactlyOnce() {
        let applyCalled = expectation(description: "settings apply")
        let clearCalled = expectation(description: "settings clear")
        let settings = TestSettingsDriver(
            onApply: { applyCalled.fulfill() },
            onClear: { clearCalled.fulfill() }
        )
        let factory = TestBridgeFactory(bridge: TestLifecycleBridge())
        let lifecycle = PacketTunnelProviderLifecycle(settings: settings, bridgeFactory: factory)
        let startCompleted = expectation(description: "start cancelled")
        let stopCompleted = expectation(description: "stop complete")
        let startResult = LockedBox<Error?>(nil)

        lifecycle.start(configuration: strictConfiguration()) { error in
            startResult.set(error)
            startCompleted.fulfill()
        }
        wait(for: [applyCalled], timeout: 2)
        lifecycle.stop { error in
            XCTAssertNil(error)
            stopCompleted.fulfill()
        }
        waitUntil { lifecycle.state == .disconnecting }
        XCTAssertEqual(settings.clearCount, 0, "settings must not clear while apply is still in flight")

        settings.completeApply(nil)
        wait(for: [clearCalled], timeout: 2)
        XCTAssertEqual(settings.clearCount, 1)
        settings.completeClear(nil)
        wait(for: [startCompleted, stopCompleted], timeout: 2)

        XCTAssertEqual(startResult.value as? PacketTunnelProviderLifecycleError, .operationCancelled)
        XCTAssertEqual(factory.makeCount, 0)
        XCTAssertEqual(settings.clearCount, 1)
        XCTAssertEqual(lifecycle.state, .disconnected)
    }

    func testConcurrentStopsDuringApplyShareOnePostApplyClear() {
        let applyCalled = expectation(description: "settings apply")
        let clearCalled = expectation(description: "settings clear")
        let settings = TestSettingsDriver(onApply: { applyCalled.fulfill() }, onClear: { clearCalled.fulfill() })
        let lifecycle = PacketTunnelProviderLifecycle(
            settings: settings,
            bridgeFactory: TestBridgeFactory(bridge: TestLifecycleBridge())
        )
        let startCompleted = expectation(description: "start cancelled")
        let stopsCompleted = expectation(description: "concurrent stops")
        stopsCompleted.expectedFulfillmentCount = 2

        lifecycle.start(configuration: strictConfiguration()) { error in
            XCTAssertEqual(error as? PacketTunnelProviderLifecycleError, .operationCancelled)
            startCompleted.fulfill()
        }
        wait(for: [applyCalled], timeout: 2)
        lifecycle.stop { error in XCTAssertNil(error); stopsCompleted.fulfill() }
        lifecycle.stop { error in XCTAssertNil(error); stopsCompleted.fulfill() }
        waitUntil { lifecycle.state == .disconnecting }
        XCTAssertEqual(settings.clearCount, 0)

        settings.completeApply(LifecycleTestError.bridgeFailed)
        wait(for: [clearCalled], timeout: 2)
        XCTAssertEqual(settings.clearCount, 1)
        settings.completeClear(nil)
        wait(for: [startCompleted, stopsCompleted], timeout: 2)
        XCTAssertEqual(settings.clearCount, 1)
        XCTAssertEqual(lifecycle.state, .disconnected)
    }

    func testConnectedStatusPersistsExactExtensionProfileIdentity() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wt-provider-identity-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: directory) }
        let statusStore = AppGroupStatusStore(directoryURL: directory)
        let applyCalled = expectation(description: "profile settings apply")
        let settings = TestSettingsDriver(onApply: { applyCalled.fulfill() })
        let lifecycle = PacketTunnelProviderLifecycle(
            settings: settings,
            bridgeFactory: TestBridgeFactory(bridge: TestLifecycleBridge()),
            statusStore: statusStore
        )
        let started = expectation(description: "profile connected")
        let configuration = strictConfiguration()

        lifecycle.start(configuration: configuration) { error in
            XCTAssertNil(error)
            started.fulfill()
        }
        wait(for: [applyCalled], timeout: 2)
        settings.completeApply(nil)
        wait(for: [started], timeout: 2)

        let status = try XCTUnwrap(statusStore.readStatus()?.status)
        XCTAssertEqual(status.providerState, .connected)
        XCTAssertEqual(status.profileIdentity, configuration.profileIdentity)
        XCTAssertEqual(status.profileValidUntil, configuration.profileValidUntil)
        XCTAssertNoThrow(try RuntimeProviderStatusValidator.requireConnected(
            status,
            expected: RuntimeProfileLease(identity: configuration.profileIdentity, validUntil: configuration.profileValidUntil)
        ))
    }

    func testProfileExpiryStopsBridgeClearsOnceThenCancelsTunnel() {
        let applyCalled = expectation(description: "settings apply")
        let clearCalled = expectation(description: "settings clear")
        let cancelled = expectation(description: "cancel tunnel after cleanup")
        let order = LockedBox<[String]>([])
        let settings = TestSettingsDriver(
            onApply: { applyCalled.fulfill() },
            onClear: { order.mutate { $0.append("settings.clear") }; clearCalled.fulfill() }
        )
        let bridge = TestLifecycleBridge(onStop: { order.mutate { $0.append("bridge.stop") } })
        let scheduler = TestExpiryScheduler()
        let lifecycle = PacketTunnelProviderLifecycle(
            settings: settings,
            bridgeFactory: TestBridgeFactory(bridge: bridge),
            expiryScheduler: scheduler,
            cancelTunnel: { error in
                XCTAssertEqual(error as? PacketTunnelProviderLifecycleError, .profileExpired)
                order.mutate { $0.append("cancelTunnel") }
                cancelled.fulfill()
            }
        )
        let started = expectation(description: "started")
        lifecycle.start(configuration: strictConfiguration()) { error in XCTAssertNil(error); started.fulfill() }
        wait(for: [applyCalled], timeout: 2)
        settings.completeApply(nil)
        wait(for: [started], timeout: 2)

        scheduler.fire(index: 0, evenIfCancelled: true)
        wait(for: [clearCalled], timeout: 2)
        XCTAssertEqual(order.value, ["bridge.stop", "settings.clear"])
        XCTAssertEqual(bridge.stopCount, 1)
        XCTAssertEqual(settings.clearCount, 1)
        settings.completeClear(nil)
        wait(for: [cancelled], timeout: 2)

        XCTAssertEqual(order.value, ["bridge.stop", "settings.clear", "cancelTunnel"])
        XCTAssertEqual(lifecycle.state, .error)
    }

    func testCancelledExpiryFromStoppedGenerationCannotAffectReplacement() {
        let applyCalled = expectation(description: "two settings applies")
        applyCalled.expectedFulfillmentCount = 2
        let clearCalled = expectation(description: "first settings clear")
        let settings = TestSettingsDriver(onApply: { applyCalled.fulfill() }, onClear: { clearCalled.fulfill() })
        let bridge = TestLifecycleBridge()
        let scheduler = TestExpiryScheduler()
        let lifecycle = PacketTunnelProviderLifecycle(
            settings: settings,
            bridgeFactory: TestBridgeFactory(bridge: bridge),
            expiryScheduler: scheduler,
            cancelTunnel: { _ in XCTFail("stale expiry must not cancel the replacement tunnel") }
        )
        let firstStarted = expectation(description: "first started")
        lifecycle.start(configuration: strictConfiguration()) { error in XCTAssertNil(error); firstStarted.fulfill() }
        waitUntil { settings.applyCount == 1 }
        settings.completeApply(nil)
        wait(for: [firstStarted], timeout: 2)

        let stopped = expectation(description: "first stopped")
        lifecycle.stop { error in XCTAssertNil(error); stopped.fulfill() }
        wait(for: [clearCalled], timeout: 2)
        settings.completeClear(nil)
        wait(for: [stopped], timeout: 2)

        let replacementStarted = expectation(description: "replacement started")
        lifecycle.start(configuration: strictConfiguration(profileRevision: 10, sessionID: "session-replacement")) { error in
            XCTAssertNil(error)
            replacementStarted.fulfill()
        }
        waitUntil { settings.applyCount == 2 }
        settings.completeApply(nil)
        wait(for: [replacementStarted, applyCalled], timeout: 2)

        scheduler.fire(index: 0, evenIfCancelled: true)
        XCTAssertEqual(lifecycle.state, .connected)
        XCTAssertEqual(bridge.stopCount, 1)
        XCTAssertEqual(settings.clearCount, 1)
    }

    func testExpiryDuringSettingsApplyWinsConcurrentExplicitStopAndCancelsAfterClear() {
        let applyCalled = expectation(description: "settings apply")
        let clearCalled = expectation(description: "settings clear")
        let cancelled = expectation(description: "expiry cancellation")
        let settings = TestSettingsDriver(onApply: { applyCalled.fulfill() }, onClear: { clearCalled.fulfill() })
        let bridge = TestLifecycleBridge()
        let factory = TestBridgeFactory(bridge: bridge)
        let scheduler = TestExpiryScheduler()
        let lifecycle = PacketTunnelProviderLifecycle(
            settings: settings,
            bridgeFactory: factory,
            expiryScheduler: scheduler,
            cancelTunnel: { error in
                XCTAssertEqual(error as? PacketTunnelProviderLifecycleError, .profileExpired)
                cancelled.fulfill()
            }
        )
        let startCompleted = expectation(description: "expired start")
        let stopCompleted = expectation(description: "explicit stop joins expiry cleanup")
        lifecycle.start(configuration: strictConfiguration()) { error in
            XCTAssertEqual(error as? PacketTunnelProviderLifecycleError, .profileExpired)
            startCompleted.fulfill()
        }
        wait(for: [applyCalled], timeout: 2)

        scheduler.fire(index: 0, evenIfCancelled: true)
        lifecycle.stop { error in XCTAssertNil(error); stopCompleted.fulfill() }
        XCTAssertEqual(settings.clearCount, 0)
        settings.completeApply(nil)
        wait(for: [clearCalled], timeout: 2)
        XCTAssertEqual(factory.makeCount, 0)
        XCTAssertEqual(bridge.stopCount, 0)
        XCTAssertEqual(settings.clearCount, 1)
        settings.completeClear(nil)
        wait(for: [startCompleted, stopCompleted, cancelled], timeout: 2)
        XCTAssertEqual(lifecycle.state, .error)
    }

    func testBridgeFailureStopsAndClearsOnceBeforeCancelTunnel() {
        let applyCalled = expectation(description: "settings apply")
        let clearCalled = expectation(description: "settings clear")
        let cancelled = expectation(description: "cancelTunnelWithError")
        let settings = TestSettingsDriver(onApply: { applyCalled.fulfill() }, onClear: { clearCalled.fulfill() })
        let bridge = TestLifecycleBridge()
        let factory = TestBridgeFactory(bridge: bridge)
        let cancelCount = LockedBox(0)
        let lifecycle = PacketTunnelProviderLifecycle(
            settings: settings,
            bridgeFactory: factory,
            cancelTunnel: { error in
                XCTAssertEqual(error as? LifecycleTestError, .bridgeFailed)
                cancelCount.mutate { $0 += 1 }
                cancelled.fulfill()
            }
        )
        let started = expectation(description: "started")
        lifecycle.start(configuration: strictConfiguration()) { error in
            XCTAssertNil(error)
            started.fulfill()
        }
        wait(for: [applyCalled], timeout: 2)
        settings.completeApply(nil)
        wait(for: [started], timeout: 2)

        factory.fail(LifecycleTestError.bridgeFailed)
        factory.fail(LifecycleTestError.bridgeFailed)
        wait(for: [clearCalled], timeout: 2)
        XCTAssertEqual(cancelCount.value, 0, "cancel must wait for settings cleanup")
        settings.completeClear(nil)
        wait(for: [cancelled], timeout: 2)

        XCTAssertEqual(cancelCount.value, 1)
        XCTAssertEqual(bridge.stopCount, 1)
        XCTAssertEqual(settings.clearCount, 1)
        XCTAssertEqual(lifecycle.state, .error)
    }

    func testDuplicateStopSharesOneCleanup() {
        let applyCalled = expectation(description: "settings apply")
        let settings = TestSettingsDriver(onApply: { applyCalled.fulfill() })
        let bridge = TestLifecycleBridge()
        let lifecycle = PacketTunnelProviderLifecycle(settings: settings, bridgeFactory: TestBridgeFactory(bridge: bridge))
        let started = expectation(description: "started")
        lifecycle.start(configuration: strictConfiguration()) { _ in started.fulfill() }
        wait(for: [applyCalled], timeout: 2)
        settings.completeApply(nil)
        wait(for: [started], timeout: 2)

        let stopped = expectation(description: "both stops")
        stopped.expectedFulfillmentCount = 2
        lifecycle.stop { error in XCTAssertNil(error); stopped.fulfill() }
        lifecycle.stop { error in XCTAssertNil(error); stopped.fulfill() }
        waitUntil { settings.clearCount == 1 }
        settings.completeClear(nil)
        wait(for: [stopped], timeout: 2)

        XCTAssertEqual(settings.clearCount, 1)
        XCTAssertEqual(bridge.stopCount, 1)
        XCTAssertEqual(lifecycle.state, .disconnected)
    }

    private func waitUntil(_ predicate: () -> Bool, timeout: TimeInterval = 2) {
        let deadline = Date().addingTimeInterval(timeout)
        while !predicate(), Date() < deadline { RunLoop.current.run(until: Date().addingTimeInterval(0.01)) }
        XCTAssertTrue(predicate())
    }
}

private enum LifecycleTestError: Error { case bridgeFailed }

private final class LockedBox<Value>: @unchecked Sendable {
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

    func mutate(_ body: (inout Value) -> Void) {
        lock.lock()
        body(&storage)
        lock.unlock()
    }
}

private final class TestSettingsDriver: PacketTunnelNetworkSettingsDriving, @unchecked Sendable {
    private let lock = NSLock()
    private let onApply: () -> Void
    private let onClear: () -> Void
    private var applyCompletion: (@Sendable (Error?) -> Void)?
    private var clearCompletion: (@Sendable (Error?) -> Void)?
    private(set) var applyCount = 0
    private(set) var clearCount = 0

    init(onApply: @escaping () -> Void = {}, onClear: @escaping () -> Void = {}) {
        self.onApply = onApply
        self.onClear = onClear
    }

    func apply(configuration: PacketTunnelConfiguration, completion: @escaping @Sendable (Error?) -> Void) {
        lock.lock()
        applyCount += 1
        applyCompletion = completion
        lock.unlock()
        onApply()
    }

    func clear(completion: @escaping @Sendable (Error?) -> Void) {
        lock.lock()
        clearCount += 1
        clearCompletion = completion
        lock.unlock()
        onClear()
    }

    func completeApply(_ error: Error?) {
        lock.lock()
        let completion = applyCompletion
        applyCompletion = nil
        lock.unlock()
        completion?(error)
    }

    func completeClear(_ error: Error?) {
        lock.lock()
        let completion = clearCompletion
        clearCompletion = nil
        lock.unlock()
        completion?(error)
    }
}

private final class TestLifecycleBridge: PacketFlowBridgeControlling, @unchecked Sendable {
    private let onStop: () -> Void
    private(set) var startCount = 0
    private(set) var stopCount = 0

    init(onStop: @escaping () -> Void = {}) { self.onStop = onStop }

    func start() throws { startCount += 1 }
    func stop() { stopCount += 1; onStop() }
}

private final class TestExpiryTask: PacketTunnelExpiryTask, @unchecked Sendable {
    private let lock = NSLock()
    private var cancelled = false
    let handler: @Sendable () -> Void

    init(handler: @escaping @Sendable () -> Void) { self.handler = handler }

    var isCancelled: Bool {
        lock.lock()
        defer { lock.unlock() }
        return cancelled
    }

    func cancel() {
        lock.lock()
        cancelled = true
        lock.unlock()
    }
}

private final class TestExpiryScheduler: PacketTunnelExpiryScheduling, @unchecked Sendable {
    private let lock = NSLock()
    private var tasks: [TestExpiryTask] = []

    func schedule(at deadline: Date, handler: @escaping @Sendable () -> Void) -> any PacketTunnelExpiryTask {
        let task = TestExpiryTask(handler: handler)
        lock.lock()
        tasks.append(task)
        lock.unlock()
        return task
    }

    func fire(index: Int, evenIfCancelled: Bool) {
        lock.lock()
        let task = tasks[index]
        lock.unlock()
        if evenIfCancelled || !task.isCancelled { task.handler() }
    }
}

private final class TestBridgeFactory: PacketTunnelBridgeBuilding, @unchecked Sendable {
    let bridge: TestLifecycleBridge
    private(set) var makeCount = 0
    private var failureHandler: (@Sendable (Error) -> Void)?

    init(bridge: TestLifecycleBridge) { self.bridge = bridge }

    func makeBridge(
        configuration: PacketTunnelConfiguration,
        failureHandler: @escaping @Sendable (Error) -> Void
    ) throws -> PacketFlowBridgeControlling {
        makeCount += 1
        self.failureHandler = failureHandler
        return bridge
    }

    func fail(_ error: Error) { failureHandler?(error) }
}

private func strictConfiguration(
    profileRevision: UInt64 = 9,
    sessionID: String = "session-test",
    profileValidUntil: Date = Date(timeIntervalSince1970: 4_102_444_800)
) -> PacketTunnelConfiguration {
    PacketTunnelConfiguration(
        remoteAddress: "198.18.0.1",
        daemonInstanceID: "daemon-test",
        profileRevision: profileRevision,
        profileHash: String(repeating: "c", count: 64),
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
