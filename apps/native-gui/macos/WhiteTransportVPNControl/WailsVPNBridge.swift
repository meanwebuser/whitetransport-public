#if canImport(NetworkExtension)
import Darwin
import Foundation

struct WailsVPNBridgeResponse: Codable {
    let success: Bool
    let state: ConnectionLifecycleState
    let error: String?
    let daemonInstanceID: String?
    let profileRevision: UInt64?
    let profileHash: String?
    let sessionID: String?
    let profileValidUntil: Date?
    let providerState: ConnectionLifecycleState?
    let providerStatusMatched: Bool
    let status: AppGroupStatusRecord?
    let logs: [AppGroupLogRecord]?

    private enum CodingKeys: String, CodingKey {
        case success
        case state
        case error
        case daemonInstanceID = "daemon_instance_id"
        case profileRevision = "profile_revision"
        case profileHash = "profile_hash"
        case sessionID = "session_id"
        case profileValidUntil = "profile_valid_until"
        case providerState = "provider_state"
        case providerStatusMatched = "provider_status_matched"
        case status
        case logs
    }
}

private enum WailsVPNBridgeError: Error, LocalizedError {
    case invalidUTF8
    case operationTimedOut
    case providerStatusMismatch
    case replacementRequiresStop

    var errorDescription: String? {
        switch self {
        case .invalidUTF8: return "system VPN request is not valid UTF-8"
        case .operationTimedOut: return "system VPN operation timed out"
        case .providerStatusMismatch: return "packet-tunnel provider did not confirm the requested runtime profile"
        case .replacementRequiresStop: return "active packet-tunnel lease must be stopped before replacement"
        }
    }
}

private final class WailsVPNResultBox: @unchecked Sendable {
    private let lock = NSLock()
    private var operationError: Error?

    func set(_ error: Error?) {
        lock.lock()
        operationError = error
        lock.unlock()
    }

    func get() -> Error? {
        lock.lock()
        defer { lock.unlock() }
        return operationError
    }
}

/// Process-local host used by the Wails executable through the framework C ABI.
final class WailsVPNBridgeHost: @unchecked Sendable {
    private let manager: VPNManager
    private let statusReader: @Sendable () throws -> AppGroupStatusRecord?
    private let callbackQueue = DispatchQueue(label: "com.meanwebuser.whitetransport.wails-vpn-callback")
    private let generationGuard = RuntimeProfileGenerationGuard()
    private let operationLock = NSLock()
    private let leaseLock = NSLock()
    private var expectedLease: RuntimeProfileLease?
    private let providerReadyTimeout: TimeInterval = 10

    init(
        manager: VPNManager = VPNManager(providerBundleIdentifier: "com.meanwebuser.whitetransport.packet-tunnel"),
        expectedLease: RuntimeProfileLease? = nil,
        statusReader: @escaping @Sendable () throws -> AppGroupStatusRecord? = {
            try AppGroupStatusStore().readStatus()
        }
    ) {
        self.manager = manager
        self.expectedLease = expectedLease
        self.statusReader = statusReader
    }

    func permission() -> WailsVPNBridgeResponse {
        operationLock.lock()
        defer { operationLock.unlock() }
        return operationResponse { completion in manager.load(completion: completion) }
    }

    func start(configurationJSON: String) -> WailsVPNBridgeResponse {
        operationLock.lock()
        defer { operationLock.unlock() }
        do {
            let configuration = try PacketTunnelConfigurationCodec.decode(Data(configurationJSON.utf8)).validated()
            let identity = try configuration.profileIdentity.validated()
            let lease = RuntimeProfileLease(identity: identity, validUntil: configuration.profileValidUntil)
            let load = wait { completion in manager.load(completion: completion) }
            if let load { return response(error: load, lease: lease) }
            if let activeLease = try activeLeaseForStart() {
                guard activeLease == lease else { throw WailsVPNBridgeError.replacementRequiresStop }
                try generationGuard.accept(identity)
                setExpectedLease(activeLease)
                return waitForProviderReady(lease: activeLease)
            }
            try generationGuard.accept(identity)
            setExpectedLease(lease)
            manager.updateTransportState(.connecting)
            let startError = wait { completion in manager.start(configuration: configuration, completion: completion) }
            if let startError {
                setExpectedLease(nil)
                generationGuard.reset()
                return response(error: startError, lease: lease)
            }
            return waitForProviderReady(lease: lease)
        } catch {
            return response(error: error)
        }
    }

    func stop() -> WailsVPNBridgeResponse {
        operationLock.lock()
        defer { operationLock.unlock() }
        let load = wait { completion in manager.load(completion: completion) }
        if let load { return response(error: load) }
        let lease = getExpectedLease()
        manager.updateTransportState(.disconnecting)
        let stopError = wait { completion in manager.stop(completion: completion) }
        if let stopError { return response(error: stopError, lease: lease) }
        manager.updateTransportState(.disconnected)
        let result = response(
            error: nil,
            lease: lease,
            state: .disconnected,
            providerState: .disconnected
        )
        setExpectedLease(nil)
        generationGuard.reset()
        return result
    }

    func status() -> WailsVPNBridgeResponse {
        operationLock.lock()
        defer { operationLock.unlock() }
        let load = wait { completion in manager.load(completion: completion) }
        if let load { return response(error: load) }
        do {
            let record = try statusReader()
            let lease = getExpectedLease() ?? record?.status.profileLease
            guard let lease else {
                return response(error: nil, status: record)
            }
            if getExpectedLease() == nil, manager.systemVPNState == .connected { setExpectedLease(lease) }
            let matched = providerStatusMatches(record, expected: lease)
            if matched, manager.systemVPNState == .connected {
                manager.updateTransportState(.connected)
                return response(error: nil, lease: lease, status: record, providerStatusMatched: true)
            }
            manager.updateTransportState(.connecting)
            let error: Error? = manager.systemVPNState == .connected ? WailsVPNBridgeError.providerStatusMismatch : nil
            return response(error: error, lease: lease, status: record)
        } catch {
            return response(error: error)
        }
    }

    func logs() -> WailsVPNBridgeResponse {
        do {
            let store = try AppGroupStatusStore()
            let semaphore = DispatchSemaphore(value: 0)
            let result = WailsVPNLogsBox()
            store.readLogs(callbackQueue: callbackQueue) { value in
                result.set(value)
                semaphore.signal()
            }
            guard semaphore.wait(timeout: .now() + 10) == .success else {
                throw WailsVPNBridgeError.operationTimedOut
            }
            let lease = getExpectedLease()
            return WailsVPNBridgeResponse(
                success: true,
                state: manager.state,
                error: nil,
                daemonInstanceID: lease?.identity.daemonInstanceID,
                profileRevision: lease?.identity.profileRevision,
                profileHash: lease?.identity.profileHash,
                sessionID: lease?.identity.sessionID,
                profileValidUntil: lease?.validUntil,
                providerState: nil,
                providerStatusMatched: false,
                status: nil,
                logs: try result.get().get()
            )
        } catch {
            return response(error: error)
        }
    }

    private func operationResponse(
        _ operation: (@escaping @Sendable (Error?) -> Void) -> Void
    ) -> WailsVPNBridgeResponse {
        response(error: wait(operation))
    }

    private func wait(_ operation: (@escaping @Sendable (Error?) -> Void) -> Void) -> Error? {
        let semaphore = DispatchSemaphore(value: 0)
        let result = WailsVPNResultBox()
        operation { error in
            result.set(error)
            semaphore.signal()
        }
        guard semaphore.wait(timeout: .now() + 10) == .success else {
            return WailsVPNBridgeError.operationTimedOut
        }
        return result.get()
    }

    private func response(
        error: Error?,
        lease: RuntimeProfileLease? = nil,
        status: AppGroupStatusRecord? = nil,
        providerStatusMatched: Bool = false,
        state: ConnectionLifecycleState? = nil,
        providerState: ConnectionLifecycleState? = nil
    ) -> WailsVPNBridgeResponse {
        let responseLease = lease ?? getExpectedLease() ?? status?.status.profileLease
        return WailsVPNBridgeResponse(
            success: error == nil,
            state: state ?? manager.state,
            error: error.map { SharedDataRedactor.redact($0.localizedDescription) },
            daemonInstanceID: responseLease?.identity.daemonInstanceID,
            profileRevision: responseLease?.identity.profileRevision,
            profileHash: responseLease?.identity.profileHash,
            sessionID: responseLease?.identity.sessionID,
            profileValidUntil: responseLease?.validUntil,
            providerState: providerState ?? status?.status.providerState,
            providerStatusMatched: providerStatusMatched,
            status: status,
            logs: nil
        )
    }

    private func waitForProviderReady(lease: RuntimeProfileLease) -> WailsVPNBridgeResponse {
        let deadline = Date().addingTimeInterval(providerReadyTimeout)
        var latestRecord: AppGroupStatusRecord?
        repeat {
            do {
                latestRecord = try statusReader()
                if manager.systemVPNState == .connected, providerStatusMatches(latestRecord, expected: lease) {
                    manager.updateTransportState(.connected)
                    return response(error: nil, lease: lease, status: latestRecord, providerStatusMatched: true)
                }
            } catch {
                if Date() >= deadline { return response(error: error, lease: lease, status: latestRecord) }
            }
            Thread.sleep(forTimeInterval: 0.1)
        } while Date() < deadline
        return response(error: WailsVPNBridgeError.providerStatusMismatch, lease: lease, status: latestRecord)
    }

    private func providerStatusMatches(_ record: AppGroupStatusRecord?, expected: RuntimeProfileLease) -> Bool {
        guard let record else { return false }
        return (try? RuntimeProviderStatusValidator.requireConnected(record.status, expected: expected)) != nil
    }

    private func activeLeaseForStart() throws -> RuntimeProfileLease? {
        if manager.systemVPNState == .disconnected {
            setExpectedLease(nil)
            return nil
        }
        if let lease = getExpectedLease() { return lease }
        switch manager.systemVPNState {
        case .connected, .connecting, .degraded, .disconnecting:
            guard let record = try statusReader(), let lease = record.status.profileLease else {
                throw WailsVPNBridgeError.replacementRequiresStop
            }
            setExpectedLease(lease)
            return lease
        case .disconnected, .permissionRequired, .unsupported, .error:
            return nil
        }
    }

    private func setExpectedLease(_ lease: RuntimeProfileLease?) {
        leaseLock.lock()
        expectedLease = lease
        leaseLock.unlock()
    }

    private func getExpectedLease() -> RuntimeProfileLease? {
        leaseLock.lock()
        defer { leaseLock.unlock() }
        return expectedLease
    }

}

private final class WailsVPNLogsBox: @unchecked Sendable {
    private let lock = NSLock()
    private var value: Result<[AppGroupLogRecord], Error> = .success([])

    func set(_ value: Result<[AppGroupLogRecord], Error>) {
        lock.lock()
        self.value = value
        lock.unlock()
    }

    func get() -> Result<[AppGroupLogRecord], Error> {
        lock.lock()
        defer { lock.unlock() }
        return value
    }
}

private let wailsVPNBridgeHost = WailsVPNBridgeHost()

private func encodeWailsVPNResponse(_ response: WailsVPNBridgeResponse) -> UnsafeMutablePointer<CChar>? {
    let encoder = JSONEncoder()
    encoder.dateEncodingStrategy = .iso8601
    encoder.outputFormatting = [.sortedKeys]
    guard let data = try? encoder.encode(response), let value = String(data: data, encoding: .utf8) else { return nil }
    return strdup(value)
}

@_cdecl("WTSystemVPNPermission")
public func WTSystemVPNPermission() -> UnsafeMutablePointer<CChar>? {
    encodeWailsVPNResponse(wailsVPNBridgeHost.permission())
}

@_cdecl("WTSystemVPNStart")
public func WTSystemVPNStart(_ configurationJSON: UnsafePointer<CChar>?) -> UnsafeMutablePointer<CChar>? {
    guard let configurationJSON, let value = String(validatingCString: configurationJSON) else {
        return encodeWailsVPNResponse(WailsVPNBridgeResponse(
            success: false,
            state: .error,
            error: WailsVPNBridgeError.invalidUTF8.localizedDescription,
            daemonInstanceID: nil,
            profileRevision: nil,
            profileHash: nil,
            sessionID: nil,
            profileValidUntil: nil,
            providerState: nil,
            providerStatusMatched: false,
            status: nil,
            logs: nil
        ))
    }
    return encodeWailsVPNResponse(wailsVPNBridgeHost.start(configurationJSON: value))
}

@_cdecl("WTSystemVPNStop")
public func WTSystemVPNStop() -> UnsafeMutablePointer<CChar>? {
    encodeWailsVPNResponse(wailsVPNBridgeHost.stop())
}

@_cdecl("WTSystemVPNStatus")
public func WTSystemVPNStatus() -> UnsafeMutablePointer<CChar>? {
    encodeWailsVPNResponse(wailsVPNBridgeHost.status())
}

@_cdecl("WTSystemVPNLogs")
public func WTSystemVPNLogs() -> UnsafeMutablePointer<CChar>? {
    encodeWailsVPNResponse(wailsVPNBridgeHost.logs())
}

@_cdecl("WTSystemVPNFreeCString")
public func WTSystemVPNFreeCString(_ value: UnsafeMutablePointer<CChar>?) {
    free(value)
}
#endif
