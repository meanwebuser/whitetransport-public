import Foundation

public enum PacketTunnelProviderLifecycleError: Error, Equatable, LocalizedError, Sendable {
    case operationCancelled
    case operationInProgress
    case profileExpired

    public var errorDescription: String? {
        switch self {
        case .operationCancelled: return "packet tunnel operation was cancelled"
        case .operationInProgress: return "a different packet tunnel operation is in progress"
        case .profileExpired: return "packet tunnel runtime profile expired"
        }
    }
}

public protocol PacketTunnelExpiryTask: AnyObject, Sendable {
    func cancel()
}

public protocol PacketTunnelExpiryScheduling: AnyObject, Sendable {
    func schedule(at deadline: Date, handler: @escaping @Sendable () -> Void) -> any PacketTunnelExpiryTask
}

public final class DispatchPacketTunnelExpiryScheduler: PacketTunnelExpiryScheduling, @unchecked Sendable {
    private let queue: DispatchQueue

    public init(queue: DispatchQueue = DispatchQueue(label: "com.meanwebuser.whitetransport.profile-expiry")) {
        self.queue = queue
    }

    public func schedule(at deadline: Date, handler: @escaping @Sendable () -> Void) -> any PacketTunnelExpiryTask {
        let workItem = DispatchWorkItem(block: handler)
        queue.asyncAfter(deadline: .now() + max(0, deadline.timeIntervalSinceNow), execute: workItem)
        return DispatchPacketTunnelExpiryTask(workItem: workItem)
    }
}

private final class DispatchPacketTunnelExpiryTask: PacketTunnelExpiryTask, @unchecked Sendable {
    private let workItem: DispatchWorkItem

    init(workItem: DispatchWorkItem) { self.workItem = workItem }

    func cancel() { workItem.cancel() }
}

public protocol PacketTunnelNetworkSettingsDriving: AnyObject {
    func apply(configuration: PacketTunnelConfiguration, completion: @escaping @Sendable (Error?) -> Void)
    func clear(completion: @escaping @Sendable (Error?) -> Void)
}

public protocol PacketTunnelBridgeBuilding: AnyObject {
    func makeBridge(
        configuration: PacketTunnelConfiguration,
        failureHandler: @escaping @Sendable (Error) -> Void
    ) throws -> PacketFlowBridgeControlling
}

/// Serializes provider start/stop/settings/bridge transitions and rejects stale async callbacks by generation.
public final class PacketTunnelProviderLifecycle: @unchecked Sendable {
    public typealias Completion = @Sendable (Error?) -> Void

    public var state: ConnectionLifecycleState { queue.sync { lifecycleState } }

    private let settings: PacketTunnelNetworkSettingsDriving
    private let bridgeFactory: PacketTunnelBridgeBuilding
    private let statusStore: AppGroupStatusStore?
    private let expiryScheduler: any PacketTunnelExpiryScheduling
    private let cancelTunnel: @Sendable (Error) -> Void
    private let queue = DispatchQueue(label: "com.meanwebuser.whitetransport.provider-lifecycle")
    private let callbackQueue = DispatchQueue(label: "com.meanwebuser.whitetransport.provider-callbacks")

    private var lifecycleState: ConnectionLifecycleState = .disconnected
    private var generation: UInt64 = 0
    private var cleanupGeneration: UInt64?
    private var activeConfiguration: PacketTunnelConfiguration?
    private var statusIdentity: RuntimeProfileIdentity?
    private var statusProfileValidUntil: Date?
    private var bridge: PacketFlowBridgeControlling?
    private var expiryTask: (any PacketTunnelExpiryTask)?
    private var settingsAttempted = false
    private var settingsApplyGeneration: UInt64?
    private var cleanupWaitingForSettingsApply = false
    private var cleanupInProgress = false
    private var startCompletions: [Completion] = []
    private var stopCompletions: [Completion] = []
    private var pendingStartError: Error?
    private var pendingCancelError: Error?
    private var cleanupFinalState: ConnectionLifecycleState = .disconnected

    public init(
        settings: PacketTunnelNetworkSettingsDriving,
        bridgeFactory: PacketTunnelBridgeBuilding,
        statusStore: AppGroupStatusStore? = nil,
        expiryScheduler: any PacketTunnelExpiryScheduling = DispatchPacketTunnelExpiryScheduler(),
        cancelTunnel: @escaping @Sendable (Error) -> Void = { _ in }
    ) {
        self.settings = settings
        self.bridgeFactory = bridgeFactory
        self.statusStore = statusStore
        self.expiryScheduler = expiryScheduler
        self.cancelTunnel = cancelTunnel
    }

    public func start(configuration: PacketTunnelConfiguration, completion: @escaping Completion) {
        queue.async { [weak self] in self?.startLocked(configuration: configuration, completion: completion) }
    }

    public func stop(completion: @escaping Completion) {
        queue.async { [weak self] in self?.stopLocked(completion: completion) }
    }

    public func statusResponse() -> ProviderMessageResponse {
        let current = state
        return ProviderMessageResponse(success: current != .error, state: current)
    }

    private func startLocked(configuration: PacketTunnelConfiguration, completion: @escaping Completion) {
        let requestedIdentity: RuntimeProfileIdentity
        do {
            try configuration.validated()
            requestedIdentity = try configuration.profileIdentity.validated()
        }
        catch { dispatch(completion, error); return }
        switch lifecycleState {
        case .connecting:
            guard activeConfiguration == configuration else { dispatch(completion, PacketTunnelProviderLifecycleError.operationInProgress); return }
            startCompletions.append(completion)
            return
        case .connected:
            guard activeConfiguration == configuration else { dispatch(completion, PacketTunnelProviderLifecycleError.operationInProgress); return }
            dispatch(completion, nil)
            return
        case .disconnecting:
            dispatch(completion, PacketTunnelProviderLifecycleError.operationInProgress)
            return
        case .disconnected, .error, .degraded, .permissionRequired, .unsupported:
            break
        }

        generation &+= 1
        let activeGeneration = generation
        activeConfiguration = configuration
        statusIdentity = requestedIdentity
        statusProfileValidUntil = configuration.profileValidUntil
        startCompletions = [completion]
        pendingStartError = nil
        pendingCancelError = nil
        cleanupInProgress = false
        settingsAttempted = true
        settingsApplyGeneration = activeGeneration
        cleanupWaitingForSettingsApply = false
        scheduleExpiryLocked(at: configuration.profileValidUntil, generation: activeGeneration)
        setStateLocked(.connecting)

        settings.apply(configuration: configuration) { [weak self] error in
            self?.queue.async { [weak self] in
                guard let self, self.settingsApplyGeneration == activeGeneration else { return }
                self.settingsApplyGeneration = nil
                if self.cleanupWaitingForSettingsApply {
                    self.cleanupWaitingForSettingsApply = false
                    self.beginCleanupLocked(
                        finalState: self.cleanupFinalState,
                        startError: self.pendingStartError,
                        cancelError: self.pendingCancelError
                    )
                    return
                }
                guard self.generation == activeGeneration, self.lifecycleState == .connecting else { return }
                if let error {
                    self.recordErrorLocked(error)
                    self.beginCleanupLocked(finalState: .error, startError: error, cancelError: nil)
                    return
                }
                do {
                    let bridge = try self.bridgeFactory.makeBridge(configuration: configuration) { [weak self] error in
                        self?.queue.async { [weak self] in self?.bridgeFailedLocked(error, generation: activeGeneration) }
                    }
                    try bridge.start()
                    self.bridge = bridge
                    self.setStateLocked(.connected)
                    self.completeStartLocked(nil)
                } catch {
                    self.recordErrorLocked(error)
                    self.beginCleanupLocked(finalState: .error, startError: error, cancelError: nil)
                }
            }
        }
    }

    private func stopLocked(completion: @escaping Completion) {
        if lifecycleState == .disconnected {
            dispatch(completion, nil)
            return
        }
        stopCompletions.append(completion)
        if cleanupInProgress || cleanupWaitingForSettingsApply { return }

        generation &+= 1
        cancelExpiryLocked()
        if !startCompletions.isEmpty { pendingStartError = PacketTunnelProviderLifecycleError.operationCancelled }
        setStateLocked(.disconnecting)
        if settingsApplyGeneration != nil {
            cleanupFinalState = .disconnected
            pendingCancelError = nil
            cleanupWaitingForSettingsApply = true
            return
        }
        beginCleanupLocked(
            finalState: .disconnected,
            startError: pendingStartError,
            cancelError: nil
        )
    }

    private func bridgeFailedLocked(_ error: Error, generation expectedGeneration: UInt64) {
        guard generation == expectedGeneration, lifecycleState == .connected || lifecycleState == .connecting else { return }
        generation &+= 1
        recordErrorLocked(error)
        setStateLocked(.error)
        beginCleanupLocked(finalState: .error, startError: error, cancelError: error)
    }

    private func profileExpiredLocked(generation expectedGeneration: UInt64) {
        guard generation == expectedGeneration, lifecycleState == .connecting || lifecycleState == .connected else { return }
        generation &+= 1
        cancelExpiryLocked()
        let error = PacketTunnelProviderLifecycleError.profileExpired
        recordErrorLocked(error)
        setStateLocked(.error)
        if settingsApplyGeneration != nil {
            cleanupFinalState = .error
            pendingStartError = error
            pendingCancelError = error
            cleanupWaitingForSettingsApply = true
            return
        }
        beginCleanupLocked(finalState: .error, startError: error, cancelError: error)
    }

    private func beginCleanupLocked(
        finalState: ConnectionLifecycleState,
        startError: Error?,
        cancelError: Error?
    ) {
        guard !cleanupInProgress else { return }
        cancelExpiryLocked()
        cleanupInProgress = true
        cleanupFinalState = finalState
        pendingStartError = startError
        pendingCancelError = cancelError
        let activeCleanupGeneration = generation
        cleanupGeneration = activeCleanupGeneration

        bridge?.stop()
        bridge = nil

        guard settingsAttempted else {
            finishCleanupLocked(error: nil, generation: activeCleanupGeneration)
            return
        }
        settings.clear { [weak self] error in
            self?.queue.async { [weak self] in self?.finishCleanupLocked(error: error, generation: activeCleanupGeneration) }
        }
    }

    private func finishCleanupLocked(error: Error?, generation expectedGeneration: UInt64) {
        guard cleanupInProgress, cleanupGeneration == expectedGeneration else { return }
        cleanupInProgress = false
        cleanupGeneration = nil
        settingsAttempted = false
        settingsApplyGeneration = nil
        cleanupWaitingForSettingsApply = false
        activeConfiguration = nil
        if let error { recordErrorLocked(error) }
        let finalState = error == nil ? cleanupFinalState : .error
        if finalState == .disconnected {
            statusIdentity = nil
            statusProfileValidUntil = nil
        }
        setStateLocked(finalState)

        let startError = pendingStartError ?? error
        pendingStartError = nil
        completeStartLocked(startError)
        completeStopLocked(error)

        if let cancelError = pendingCancelError {
            pendingCancelError = nil
            callbackQueue.async { [cancelTunnel] in cancelTunnel(cancelError) }
        }
    }

    private func completeStartLocked(_ error: Error?) {
        let completions = startCompletions
        startCompletions.removeAll(keepingCapacity: false)
        for completion in completions { dispatch(completion, error) }
    }

    private func completeStopLocked(_ error: Error?) {
        let completions = stopCompletions
        stopCompletions.removeAll(keepingCapacity: false)
        for completion in completions { dispatch(completion, error) }
    }

    private func dispatch(_ completion: @escaping Completion, _ error: Error?) {
        callbackQueue.async { completion(error) }
    }

    private func scheduleExpiryLocked(at deadline: Date, generation expectedGeneration: UInt64) {
        cancelExpiryLocked()
        expiryTask = expiryScheduler.schedule(at: deadline) { [weak self] in
            self?.queue.async { [weak self] in self?.profileExpiredLocked(generation: expectedGeneration) }
        }
    }

    private func cancelExpiryLocked() {
        expiryTask?.cancel()
        expiryTask = nil
    }

    private func setStateLocked(_ state: ConnectionLifecycleState) {
        lifecycleState = state
        guard let statusStore else { return }
        let transport: TransportState
        let systemVPN: SystemVPNState
        switch state {
        case .disconnected: transport = .disconnected; systemVPN = .disconnected
        case .permissionRequired: transport = .disconnected; systemVPN = .permissionRequired
        case .connecting: transport = .connecting; systemVPN = .connecting
        case .connected: transport = .connected; systemVPN = .connected
        case .degraded: transport = .degraded; systemVPN = .degraded
        case .disconnecting: transport = .disconnecting; systemVPN = .disconnecting
        case .unsupported: transport = .unsupported; systemVPN = .unsupported
        case .error: transport = .error; systemVPN = .error
        }
        do {
            try statusStore.write(status: ConnectionStatus(
                state: state,
                transport: transport,
                systemVPN: systemVPN,
                providerState: state,
                profileIdentity: statusIdentity,
                profileValidUntil: statusProfileValidUntil
            ))
        } catch {
            NSLog("WhiteTransport App Group status persistence failed: %@", String(describing: type(of: error)))
        }
    }

    private func recordErrorLocked(_ error: Error) {
        guard let statusStore else { return }
        do {
            try statusStore.appendError(
                error,
                status: ConnectionStatus(
                    state: lifecycleState,
                    transport: lifecycleState == .connected ? .connected : .error,
                    systemVPN: lifecycleState == .connected ? .connected : .error,
                    providerState: lifecycleState,
                    profileIdentity: statusIdentity
                )
            )
        }
        catch { NSLog("WhiteTransport App Group error persistence failed: %@", String(describing: type(of: error))) }
    }
}
