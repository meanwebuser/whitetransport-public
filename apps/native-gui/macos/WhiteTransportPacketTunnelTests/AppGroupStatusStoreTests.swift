import Darwin
import Foundation
import XCTest
@testable import WhiteTransportMacOS

final class AppGroupStatusStoreTests: XCTestCase {
    func testVersionedStatusRoundTripUsesSnakeCaseAndRedactsSecrets() throws {
        let directory = temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = AppGroupStatusStore(directoryURL: directory, now: { Date(timeIntervalSince1970: 123) })
        let profileValidUntil = Date(timeIntervalSince1970: 2_000_000_000)
        let status = ConnectionStatus(
            state: .connecting,
            transport: .connected,
            systemVPN: .connecting,
            providerState: .connecting,
            profileIdentity: testProfileIdentity(),
            profileValidUntil: profileValidUntil,
            message: "token=secret https://user:pass@control.example.test/path?access_token=secret"
        )

        try store.write(status: status)
        let record = try XCTUnwrap(store.readStatus())

        XCTAssertEqual(record.schemaVersion, WhiteTransportAppGroup.schemaVersion)
        XCTAssertEqual(record.status.state, .connecting)
        XCTAssertEqual(record.status.profileIdentity, testProfileIdentity())
        XCTAssertEqual(record.status.profileValidUntil, profileValidUntil)
        XCTAssertFalse(record.status.message?.contains("secret") ?? true)
        XCTAssertFalse(record.status.message?.contains("control.example.test") ?? true)

        let raw = try String(contentsOf: store.statusFileURL, encoding: .utf8)
        XCTAssertTrue(raw.contains("schema_version"))
        XCTAssertTrue(raw.contains("system_vpn"))
        XCTAssertTrue(raw.contains("profile_valid_until"))
        XCTAssertTrue(raw.contains("2033-05-18T03:33:20Z"))
        XCTAssertFalse(raw.contains("access_token"))
        XCTAssertEqual(try fileMode(store.statusFileURL), 0o600)
    }

    func testVersionedJSONLLogExchangeRedactsBeforePersistence() throws {
        let directory = temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = AppGroupStatusStore(directoryURL: directory, now: { Date(timeIntervalSince1970: 456) })

        try store.appendLog(level: .error, message: "Authorization=Bearer abc cookie=session host=https://node.example.test/a?q=1")
        let records = try store.readLogs()

        XCTAssertEqual(records.count, 1)
        XCTAssertEqual(records[0].schemaVersion, WhiteTransportAppGroup.schemaVersion)
        XCTAssertEqual(records[0].level, .error)
        let message = try XCTUnwrap(records[0].message)
        XCTAssertFalse(message.contains("abc"))
        XCTAssertFalse(message.contains("session"))
        XCTAssertFalse(message.contains("node.example.test"))
        XCTAssertEqual(try fileMode(store.logFileURL), 0o600)
    }

    func testSequenceRemainsMonotonicAcrossFreshStoreInstances() throws {
        let directory = temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let firstStore = AppGroupStatusStore(directoryURL: directory)
        let secondStore = AppGroupStatusStore(directoryURL: directory)

        try firstStore.write(status: status(.connecting))
        try secondStore.write(status: status(.connected))

        let restartedStore = AppGroupStatusStore(directoryURL: directory)
        let record = try XCTUnwrap(restartedStore.readStatus())
        XCTAssertEqual(record.sequence, 2)
        XCTAssertEqual(record.status.state, .connected)
        XCTAssertEqual(try fileMode(restartedStore.lockFileURL), 0o600)
    }

    func testDarwinFileLockSerializesASeparateWriterProcess() throws {
        let directory = temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = AppGroupStatusStore(directoryURL: directory)
        try store.write(status: status(.connecting))

        let descriptor = store.lockFileURL.path.withCString {
            Darwin.open($0, O_RDWR)
        }
        XCTAssertGreaterThanOrEqual(descriptor, 0)
        defer { if descriptor >= 0 { Darwin.close(descriptor) } }
        XCTAssertEqual(fcntl(descriptor, F_SETFD, FD_CLOEXEC), 0)
        XCTAssertEqual(flock(descriptor, LOCK_EX), 0)

        let readyMarkerURL = directory.appendingPathComponent("lock-ready.marker")
        let acquiredMarkerURL = directory.appendingPathComponent("lock-acquired.marker")
        let helperURL = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .appendingPathComponent("TestFixtures/AppGroupLockProbe.swift")
        XCTAssertTrue(FileManager.default.fileExists(atPath: helperURL.path))
        let worker = Process()
        worker.executableURL = URL(fileURLWithPath: "/usr/bin/xcrun")
        worker.arguments = ["swift", helperURL.path, store.lockFileURL.path, readyMarkerURL.path, acquiredMarkerURL.path]
        let output = Pipe()
        worker.standardOutput = output
        worker.standardError = output

        try worker.run()
        let readyDeadline = Date().addingTimeInterval(10)
        while !FileManager.default.fileExists(atPath: readyMarkerURL.path), Date() < readyDeadline {
            Thread.sleep(forTimeInterval: 0.02)
        }
        XCTAssertTrue(FileManager.default.fileExists(atPath: readyMarkerURL.path), "lock helper did not reach acquisition boundary")
        XCTAssertTrue(worker.isRunning, "writer bypassed the Darwin file lock")
        XCTAssertFalse(FileManager.default.fileExists(atPath: acquiredMarkerURL.path))
        XCTAssertEqual(flock(descriptor, LOCK_UN), 0)
        worker.waitUntilExit()

        if worker.terminationStatus != 0 {
            let data = output.fileHandleForReading.readDataToEndOfFile()
            XCTFail("writer process failed: \(String(decoding: data, as: UTF8.self))")
        }
        XCTAssertEqual(try String(contentsOf: acquiredMarkerURL, encoding: .utf8), "acquired")
    }

    func testStatusTransitionsAndErrorsUseStructuredRecursivelyRedactedJSONL() throws {
        let directory = temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = AppGroupStatusStore(directoryURL: directory, now: { Date(timeIntervalSince1970: 789) })
        let metadata: [String: AppGroupJSONValue] = [
            "attempt": .integer(3),
            "nested": .object([
                "accessToken": .string("token-value"),
                "child": .array([
                    .object(["PASSWORD_hint": .string("password-value")]),
                    .object(["safe": .string("Authorization=Bearer auth-value")]),
                ]),
            ]),
            "endpointHost": .string("node.example.test"),
            "request_url": .string("https://control.example.test/private"),
            "peer": .string("peer.example.test"),
        ]

        try store.write(
            status: ConnectionStatus(
                state: .connecting,
                transport: .connected,
                systemVPN: .connecting,
                providerState: .connecting,
                profileIdentity: testProfileIdentity(),
                message: "dial host=node.example.test url=https://control.example.test/private token=status-value"
            ),
            metadata: metadata
        )
        try store.appendError(
            NSError(
                domain: "Authorization=Bearer domain-value",
                code: 42,
                userInfo: [NSLocalizedDescriptionKey: "cookie=error-value host=error.example.test"]
            ),
            status: status(.error),
            metadata: ["clientSecret": .string("metadata-value")]
        )

        let records = try store.readLogs()
        XCTAssertEqual(records.map(\.event), [.statusTransition, .error])
        XCTAssertEqual(records[0].previousState, nil)
        XCTAssertEqual(records[0].status?.state, .connecting)
        XCTAssertEqual(records[0].metadata["attempt"], .integer(3))
        XCTAssertEqual(records[0].metadata["nested"], .object([
            "accessToken": .string("<redacted>"),
            "child": .array([
                .object(["PASSWORD_hint": .string("<redacted>")]),
                .object(["safe": .string("<redacted>")]),
            ]),
        ]))
        XCTAssertEqual(records[0].metadata["endpointHost"], .string("<endpoint>"))
        XCTAssertEqual(records[0].metadata["request_url"], .string("<endpoint>"))
        XCTAssertEqual(records[0].metadata["peer"], .string("<endpoint>"))
        XCTAssertEqual(records[1].error?.code, 42)
        XCTAssertEqual(records[1].metadata["clientSecret"], .string("<redacted>"))

        let raw = try String(contentsOf: store.logFileURL, encoding: .utf8)
        for forbidden in [
            "token-value", "password-value", "auth-value", "node.example.test",
            "control.example.test", "peer.example.test", "status-value", "domain-value", "error-value",
            "error.example.test", "metadata-value",
        ] {
            XCTAssertFalse(raw.contains(forbidden), "persisted forbidden value: \(forbidden)")
        }
    }

    func testLogRotationBoundsCurrentAndPreviousFiles() throws {
        let directory = temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let maximumBytes = 420
        let store = AppGroupStatusStore(directoryURL: directory, maxLogBytes: maximumBytes)

        for index in 0..<12 {
            try store.appendLog(level: .info, message: "event-\(index)-01234567890123456789")
        }

        let currentSize = try fileSize(store.logFileURL)
        let previousSize = try fileSize(store.rotatedLogFileURL)
        XCTAssertLessThanOrEqual(currentSize, maximumBytes)
        XCTAssertLessThanOrEqual(previousSize, maximumBytes)
        let records = try store.readLogs()
        XCTAssertFalse(records.isEmpty)
        XCTAssertLessThan(records.count, 12)
        XCTAssertEqual(records.last?.message, "event-11-01234567890123456789")
    }

    func testAsyncReadsDeliverOnCallerProvidedQueue() throws {
        let directory = temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = AppGroupStatusStore(directoryURL: directory)
        try store.write(status: status(.connected))

        let key = DispatchSpecificKey<String>()
        let callbackQueue = DispatchQueue(label: "test.app-group-callback")
        callbackQueue.setSpecific(key: key, value: "callback")
        let statusExpectation = expectation(description: "status callback")
        let logsExpectation = expectation(description: "logs callback")

        store.readStatus(callbackQueue: callbackQueue) { result in
            XCTAssertEqual(DispatchQueue.getSpecific(key: key), "callback")
            XCTAssertEqual(try? result.get()?.status.state, .connected)
            statusExpectation.fulfill()
        }
        store.readLogs(callbackQueue: callbackQueue) { result in
            XCTAssertEqual(DispatchQueue.getSpecific(key: key), "callback")
            XCTAssertEqual(try? result.get().last?.event, .statusTransition)
            logsExpectation.fulfill()
        }

        wait(for: [statusExpectation, logsExpectation], timeout: 2)
    }

    private func temporaryDirectory() -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("wt-app-group-\(UUID().uuidString)", isDirectory: true)
    }

    private func fileMode(_ url: URL) throws -> Int {
        let attributes = try FileManager.default.attributesOfItem(atPath: url.path)
        return (attributes[.posixPermissions] as? NSNumber)?.intValue ?? -1
    }

    private func fileSize(_ url: URL) throws -> Int {
        let attributes = try FileManager.default.attributesOfItem(atPath: url.path)
        return (attributes[.size] as? NSNumber)?.intValue ?? -1
    }

    private func status(_ state: ConnectionLifecycleState) -> ConnectionStatus {
        ConnectionStatus(
            state: state,
            transport: .connected,
            systemVPN: .connected,
            providerState: state,
            profileIdentity: testProfileIdentity(),
            profileValidUntil: Date(timeIntervalSince1970: 2_000_000_000)
        )
    }

    private func testProfileIdentity() -> RuntimeProfileIdentity {
        RuntimeProfileIdentity(
            daemonInstanceID: "daemon-app-group",
            profileRevision: 3,
            profileHash: String(repeating: "d", count: 64),
            sessionID: "session-app-group"
        )
    }
}
