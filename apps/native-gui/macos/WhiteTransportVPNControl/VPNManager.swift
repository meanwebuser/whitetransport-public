import Foundation

public enum VPNManagerError: Error, Equatable, LocalizedError, Sendable {
    case notLoaded
    case permissionRequired
    case invalidConfiguration(String)
    case saveFailed(String)
    case startFailed(String)
    case stopFailed(String)
    case providerMessageUnavailable
    case operationInProgress

    public var errorDescription: String? {
        switch self {
        case .notLoaded: return "VPN manager preferences have not been loaded"
        case .permissionRequired: return "Network Extension permission is required"
        case .invalidConfiguration(let value): return "invalid VPN configuration: \(value)"
        case .saveFailed(let value): return "VPN preferences save failed: \(value)"
        case .startFailed(let value): return "VPN start failed: \(value)"
        case .stopFailed(let value): return "VPN stop failed: \(value)"
        case .providerMessageUnavailable: return "NETunnelProviderSession is unavailable"
        case .operationInProgress: return "another VPN manager operation is in progress"
        }
    }
}

public protocol VPNManagerBackend: AnyObject, Sendable {
    var providerBundleIdentifier: String? { get }
    var systemState: SystemVPNState { get }
    func observeStatus(_ observer: @escaping @Sendable (SystemVPNState) -> Void)
    func configure(
        configuration: PacketTunnelConfiguration,
        providerBundleIdentifier: String,
        localizedDescription: String
    ) throws
    func save(completion: @escaping @Sendable (Error?) -> Void)
    func reload(completion: @escaping @Sendable (Error?) -> Void)
    func start() throws
    func stop()
    func sendProviderMessage(_ data: Data, completion: @escaping @Sendable (Data?) -> Void) throws
}

public protocol VPNManagerBackendStore: Sendable {
    func loadAll(completion: @escaping @Sendable ([any VPNManagerBackend]?, Error?) -> Void)
    func makeManager() -> any VPNManagerBackend
}

/// App-side VPN controller with exact manager selection and serialized preference operations.
public final class VPNManager: @unchecked Sendable {
    public typealias Completion = @Sendable (Error?) -> Void

    public var state: ConnectionLifecycleState { queue.sync { lifecycleState } }
    public var systemVPNState: SystemVPNState { queue.sync { currentSystemVPNState } }
    public var transportState: TransportState { queue.sync { currentTransportState } }

    private let providerBundleIdentifier: String
    private let localizedDescription: String
    private let backendStore: any VPNManagerBackendStore
    private let providerStopGracePeriod: TimeInterval
    private let systemStopTimeout: TimeInterval
    private let queue = DispatchQueue(label: "com.meanwebuser.whitetransport.vpn-manager")
    private let callbackQueue = DispatchQueue(label: "com.meanwebuser.whitetransport.vpn-manager-callbacks")

    private var backend: (any VPNManagerBackend)?
    private var lifecycleState: ConnectionLifecycleState = .disconnected
    private var currentSystemVPNState: SystemVPNState = .disconnected
    private var currentTransportState: TransportState = .disconnected
    private var generation: UInt64 = 0
    private var operationInProgress = false
    private var pendingStopGeneration: UInt64?
    private var systemStopIssuedGeneration: UInt64?
    private var stopCompletions: [Completion] = []

    public init(
        providerBundleIdentifier: String,
        localizedDescription: String = "WhiteTransport",
        backendStore: any VPNManagerBackendStore,
        providerStopGracePeriod: TimeInterval = 1,
        systemStopTimeout: TimeInterval = 10
    ) {
        self.providerBundleIdentifier = providerBundleIdentifier
        self.localizedDescription = localizedDescription
        self.backendStore = backendStore
        self.providerStopGracePeriod = max(0.01, providerStopGracePeriod)
        self.systemStopTimeout = max(0.01, systemStopTimeout)
    }

    public func updateTransportState(_ state: TransportState) {
        queue.sync {
            currentTransportState = state
            recomputeStateLocked()
        }
    }

    public func load(completion: @escaping Completion) {
        queue.async { [weak self] in
            guard let self else { return }
            guard !self.operationInProgress else { self.dispatch(completion, VPNManagerError.operationInProgress); return }
            self.operationInProgress = true
            self.backendStore.loadAll { [weak self] backends, error in
                self?.queue.async { [weak self] in self?.finishLoadLocked(backends: backends, error: error, completion: completion) }
            }
        }
    }

    public func start(configuration: PacketTunnelConfiguration, completion: @escaping Completion) {
        queue.async { [weak self] in self?.startLocked(configuration: configuration, completion: completion) }
    }

    public func stop(completion: @escaping Completion) {
        queue.async { [weak self] in self?.stopLocked(completion: completion) }
    }

    public func sendProviderMessage(
        _ message: ProviderMessage,
        completion: @escaping @Sendable (Result<ProviderMessageResponse, Error>) -> Void
    ) {
        queue.async { [weak self] in
            guard let self, let backend = self.backend else {
                completion(.failure(VPNManagerError.notLoaded))
                return
            }
            do {
                let data = try ProviderMessageCodec.encode(message)
                try backend.sendProviderMessage(data) { responseData in
                    guard let responseData else {
                        completion(.failure(VPNManagerError.providerMessageUnavailable))
                        return
                    }
                    do { completion(.success(try JSONDecoder().decode(ProviderMessageResponse.self, from: responseData))) }
                    catch { completion(.failure(error)) }
                }
            } catch {
                completion(.failure(error))
            }
        }
    }

    private func finishLoadLocked(
        backends: [any VPNManagerBackend]?,
        error: Error?,
        completion: @escaping Completion
    ) {
        operationInProgress = false
        if let error {
            let mapped = mapPermission(error, otherwise: { $0 })
            if mapped as? VPNManagerError == .permissionRequired { applySystemStateLocked(.permissionRequired) }
            dispatch(completion, mapped)
            return
        }
        let loaded = backends ?? []
        let matching = loaded.filter { $0.providerBundleIdentifier == providerBundleIdentifier }
        let active = matching.filter { backend in
            switch backend.systemState {
            case .connecting, .connected, .degraded, .disconnecting: return true
            case .disconnected, .permissionRequired, .error, .unsupported: return false
            }
        }
        guard active.count <= 1 else {
            dispatch(
                completion,
                VPNManagerError.invalidConfiguration("multiple active VPN managers use the same provider bundle identifier")
            )
            return
        }
        // A stale duplicate must never hide the manager that owns the active
        // route. When every exact match is disconnected, configuring the first
        // one is safe; the next load will prefer the single active instance.
        let selected = active.first ?? matching.first ?? backendStore.makeManager()
        backend = selected
        selected.observeStatus { [weak self] state in
            self?.queue.async { [weak self] in self?.observeSystemStateLocked(state) }
        }
        applySystemStateLocked(selected.systemState)
        dispatch(completion, nil)
    }

    private func startLocked(configuration: PacketTunnelConfiguration, completion: @escaping Completion) {
        guard !operationInProgress, pendingStopGeneration == nil else {
            dispatch(completion, VPNManagerError.operationInProgress)
            return
        }
        guard let backend else { dispatch(completion, VPNManagerError.notLoaded); return }
        do {
            try configuration.validated()
            try backend.configure(
                configuration: configuration,
                providerBundleIdentifier: providerBundleIdentifier,
                localizedDescription: localizedDescription
            )
        } catch {
            dispatch(completion, error)
            return
        }

        operationInProgress = true
        generation &+= 1
        let activeGeneration = generation
        applySystemStateLocked(.connecting)
        backend.save { [weak self] error in
            self?.queue.async { [weak self] in
                guard let self, self.generation == activeGeneration, self.operationInProgress else { return }
                if let error {
                    self.finishStartLocked(error: self.mapPermission(error) { VPNManagerError.saveFailed(String(describing: $0)) }, completion: completion)
                    return
                }
                backend.reload { [weak self] error in
                    self?.queue.async { [weak self] in
                        guard let self, self.generation == activeGeneration, self.operationInProgress else { return }
                        if let error {
                            self.finishStartLocked(error: self.mapPermission(error) { VPNManagerError.saveFailed(String(describing: $0)) }, completion: completion)
                            return
                        }
                        do {
                            try backend.start()
                            self.finishStartLocked(error: nil, completion: completion)
                        } catch {
                            self.finishStartLocked(
                                error: self.mapPermission(error) { VPNManagerError.startFailed(String(describing: $0)) },
                                completion: completion
                            )
                        }
                    }
                }
            }
        }
    }

    private func finishStartLocked(error: Error?, completion: @escaping Completion) {
        operationInProgress = false
        if let error {
            if error as? VPNManagerError == .permissionRequired { applySystemStateLocked(.permissionRequired) }
            else { applySystemStateLocked(.error) }
        } else {
            applySystemStateLocked(.connecting)
        }
        dispatch(completion, error)
    }

    private func stopLocked(completion: @escaping Completion) {
        guard !operationInProgress else { dispatch(completion, VPNManagerError.operationInProgress); return }
        guard let backend else { dispatch(completion, VPNManagerError.notLoaded); return }
        stopCompletions.append(completion)
        if pendingStopGeneration != nil { return }

        generation &+= 1
        let activeGeneration = generation
        pendingStopGeneration = activeGeneration
        applySystemStateLocked(.disconnecting)
        do {
            let message = try ProviderMessageCodec.encode(ProviderMessage(command: .stop))
            try backend.sendProviderMessage(message) { [weak self] _ in
                self?.queue.async { [weak self] in self?.issueSystemStopLocked(backend: backend, generation: activeGeneration) }
            }
        } catch {
            issueSystemStopLocked(backend: backend, generation: activeGeneration)
            return
        }

        queue.asyncAfter(deadline: .now() + providerStopGracePeriod) { [weak self] in
            self?.issueSystemStopLocked(backend: backend, generation: activeGeneration)
        }
    }

    private func issueSystemStopLocked(backend: any VPNManagerBackend, generation expectedGeneration: UInt64) {
        guard pendingStopGeneration == expectedGeneration, systemStopIssuedGeneration != expectedGeneration else { return }
        systemStopIssuedGeneration = expectedGeneration
        backend.stop()
        guard backend.systemState != .disconnected else {
            applySystemStateLocked(.disconnected)
            completeStopLocked(error: nil, generation: expectedGeneration)
            return
        }
        queue.asyncAfter(deadline: .now() + systemStopTimeout) { [weak self] in
            self?.failSystemStopLocked(generation: expectedGeneration)
        }
    }

    private func observeSystemStateLocked(_ state: SystemVPNState) {
        applySystemStateLocked(state)
        guard state == .disconnected,
              let expectedGeneration = pendingStopGeneration,
              systemStopIssuedGeneration == expectedGeneration else { return }
        completeStopLocked(error: nil, generation: expectedGeneration)
    }

    private func failSystemStopLocked(generation expectedGeneration: UInt64) {
        guard pendingStopGeneration == expectedGeneration, systemStopIssuedGeneration == expectedGeneration else { return }
        applySystemStateLocked(.error)
        completeStopLocked(
            error: VPNManagerError.stopFailed("system VPN did not report disconnected before timeout"),
            generation: expectedGeneration
        )
    }

    private func completeStopLocked(error: Error?, generation expectedGeneration: UInt64) {
        guard pendingStopGeneration == expectedGeneration else { return }
        pendingStopGeneration = nil
        systemStopIssuedGeneration = nil
        let completions = stopCompletions
        stopCompletions.removeAll(keepingCapacity: false)
        for completion in completions { dispatch(completion, error) }
    }

    private func mapPermission(_ error: Error, otherwise: (Error) -> Error) -> Error {
        let value = error as NSError
        let isPermission =
            (value.domain == "NEVPNErrorDomain" && value.code == 5) ||
            (value.domain == NSPOSIXErrorDomain && (value.code == Int(EPERM) || value.code == Int(EACCES))) ||
            (value.domain == NSCocoaErrorDomain && (value.code == NSFileReadNoPermissionError || value.code == NSFileWriteNoPermissionError))
        return isPermission ? VPNManagerError.permissionRequired : otherwise(error)
    }

    private func applySystemStateLocked(_ state: SystemVPNState) {
        currentSystemVPNState = state
        recomputeStateLocked()
    }

    private func recomputeStateLocked() {
        lifecycleState = ConnectionStateReducer.reduce(transport: currentTransportState, systemVPN: currentSystemVPNState)
    }

    private func dispatch(_ completion: @escaping Completion, _ error: Error?) {
        callbackQueue.async { completion(error) }
    }
}

#if canImport(NetworkExtension)
@preconcurrency import NetworkExtension

public extension VPNManager {
    convenience init(providerBundleIdentifier: String, localizedDescription: String = "WhiteTransport") {
        self.init(
            providerBundleIdentifier: providerBundleIdentifier,
            localizedDescription: localizedDescription,
            backendStore: NetworkExtensionVPNManagerStore()
        )
    }

    static func map(status: NEVPNStatus) -> SystemVPNState {
        switch status {
        case .invalid: return .error
        case .disconnected: return .disconnected
        case .connecting: return .connecting
        case .connected: return .connected
        case .reasserting: return .degraded
        case .disconnecting: return .disconnecting
        @unknown default: return .unsupported
        }
    }
}

private final class NetworkExtensionVPNManagerStore: VPNManagerBackendStore, @unchecked Sendable {
    func loadAll(completion: @escaping @Sendable ([any VPNManagerBackend]?, Error?) -> Void) {
        NETunnelProviderManager.loadAllFromPreferences { managers, error in
            completion(managers?.map(NetworkExtensionVPNManagerBackend.init), error)
        }
    }

    func makeManager() -> any VPNManagerBackend {
        NetworkExtensionVPNManagerBackend(manager: NETunnelProviderManager())
    }
}

private final class NetworkExtensionVPNManagerBackend: VPNManagerBackend, @unchecked Sendable {
    private let manager: NETunnelProviderManager
    private var statusObserver: NSObjectProtocol?

    init(manager: NETunnelProviderManager) { self.manager = manager }

    deinit {
        if let statusObserver { NotificationCenter.default.removeObserver(statusObserver) }
    }

    var providerBundleIdentifier: String? {
        (manager.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier
    }

    var systemState: SystemVPNState { VPNManager.map(status: manager.connection.status) }

    func observeStatus(_ observer: @escaping @Sendable (SystemVPNState) -> Void) {
        if let statusObserver { NotificationCenter.default.removeObserver(statusObserver) }
        statusObserver = NotificationCenter.default.addObserver(
            forName: .NEVPNStatusDidChange,
            object: manager.connection,
            queue: nil
        ) { [weak self] _ in
            guard let self else { return }
            observer(VPNManager.map(status: self.manager.connection.status))
        }
    }

    func configure(
        configuration: PacketTunnelConfiguration,
        providerBundleIdentifier: String,
        localizedDescription: String
    ) throws {
        let providerProtocol = NETunnelProviderProtocol()
        providerProtocol.providerBundleIdentifier = providerBundleIdentifier
        providerProtocol.serverAddress = configuration.remoteAddress
        providerProtocol.providerConfiguration = try configuration.providerConfigurationDictionary()
        manager.protocolConfiguration = providerProtocol
        manager.localizedDescription = localizedDescription
        manager.isEnabled = true
    }

    func save(completion: @escaping @Sendable (Error?) -> Void) {
        manager.saveToPreferences(completionHandler: completion)
    }

    func reload(completion: @escaping @Sendable (Error?) -> Void) {
        manager.loadFromPreferences(completionHandler: completion)
    }

    func start() throws { try manager.connection.startVPNTunnel() }

    func stop() { manager.connection.stopVPNTunnel() }

    func sendProviderMessage(_ data: Data, completion: @escaping @Sendable (Data?) -> Void) throws {
        guard let session = manager.connection as? NETunnelProviderSession else {
            throw VPNManagerError.providerMessageUnavailable
        }
        try session.sendProviderMessage(data, responseHandler: completion)
    }
}

private extension PacketTunnelConfiguration {
    func providerConfigurationDictionary() throws -> [String: Any] {
        try PacketTunnelConfigurationCodec.dictionary(self)
    }
}
#endif
