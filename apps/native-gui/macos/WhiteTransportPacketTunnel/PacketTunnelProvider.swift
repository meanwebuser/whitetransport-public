import Foundation

public enum PacketTunnelProviderError: Error, Equatable, LocalizedError, Sendable {
    case configurationMissing
    case invalidConfiguration(String)
    case extensionImplementationMissing
    case networkSettingsFailed(String)

    public var errorDescription: String? {
        switch self {
        case .configurationMissing: return "packet-tunnel provider configuration is missing"
        case .invalidConfiguration(let value): return "invalid packet-tunnel provider configuration: \(value)"
        case .extensionImplementationMissing: return "packet-tunnel executable did not provide its engine factory"
        case .networkSettingsFailed(let value): return "network settings failed: \(value)"
        }
    }
}

/// Creates the gomobile engine in the extension process; there is no app-process registry or injection path.
public protocol PacketFlowEngineBuilding: AnyObject {
    func makeEngine(configuration: PacketTunnelConfiguration) throws -> PacketFlowBridgeEngine
}

/// Process-local bridge factory owned by the packet-tunnel extension target.
public final class PacketTunnelExtensionBridgeFactory: PacketTunnelBridgeBuilding {
    private let flow: PacketFlowBridgePacketFlow
    private let engineBuilder: PacketFlowEngineBuilding

    public init(flow: PacketFlowBridgePacketFlow, engineBuilder: PacketFlowEngineBuilding) {
        self.flow = flow
        self.engineBuilder = engineBuilder
    }

    public func makeBridge(
        configuration: PacketTunnelConfiguration,
        failureHandler: @escaping @Sendable (Error) -> Void
    ) throws -> PacketFlowBridgeControlling {
        let engine = try engineBuilder.makeEngine(configuration: configuration)
        return PacketFlowBridge(flow: flow, engine: engine, failureHandler: failureHandler)
    }
}

#if canImport(NetworkExtension)
@preconcurrency import NetworkExtension

open class PacketTunnelProvider: NEPacketTunnelProvider, @unchecked Sendable {
    public var lifecycleState: ConnectionLifecycleState { lifecycle.state }

    private lazy var settingsDriver = PacketTunnelNetworkSettingsDriver(provider: self)
    private lazy var bridgeFactory = makeBridgeFactory(packetFlow: packetFlow)
    private lazy var lifecycle = PacketTunnelProviderLifecycle(
        settings: settingsDriver,
        bridgeFactory: bridgeFactory,
        statusStore: try? AppGroupStatusStore(),
        cancelTunnel: { [weak self] error in self?.cancelTunnelWithError(error) }
    )

    /// The packet-tunnel executable overrides this to bind its statically linked C archive.
    open func makeBridgeFactory(packetFlow: PacketFlowBridgePacketFlow) -> PacketTunnelBridgeBuilding {
        preconditionFailure(PacketTunnelProviderError.extensionImplementationMissing.localizedDescription)
    }

    public override func startTunnel(
        options: [String: NSObject]?,
        completionHandler: @escaping (Error?) -> Void
    ) {
        let completion = ErrorCompletionBox(completionHandler)
        do {
            let configuration = try loadConfiguration()
            lifecycle.start(configuration: configuration) { error in completion.call(error) }
        } catch {
            completion.call(error)
        }
    }

    public override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        let completion = VoidCompletionBox(completionHandler)
        lifecycle.stop { _ in completion.call() }
    }

    public override func handleAppMessage(_ messageData: Data, completionHandler: ((Data?) -> Void)? = nil) {
        let completion = OptionalDataCompletionBox(completionHandler)
        do {
            let message = try ProviderMessageCodec.decode(messageData)
            switch message.command {
            case .status:
                completion.call(try ProviderMessageCodec.encode(lifecycle.statusResponse()))
            case .stop:
                lifecycle.stop { [weak self] error in
                    let state = self?.lifecycle.state ?? .disconnected
                    let response = ProviderMessageResponse(
                        success: error == nil && state == .disconnected,
                        state: state,
                        error: error.map(String.init(describing:))
                    )
                    completion.call(try? ProviderMessageCodec.encode(response))
                }
            }
        } catch {
            let response = ProviderMessageResponse(success: false, state: .error, error: String(describing: error))
            completion.call(try? ProviderMessageCodec.encode(response))
        }
    }

    private func loadConfiguration() throws -> PacketTunnelConfiguration {
        guard let providerConfiguration = (protocolConfiguration as? NETunnelProviderProtocol)?.providerConfiguration else {
            throw PacketTunnelProviderError.configurationMissing
        }
        do {
            let data = try JSONSerialization.data(withJSONObject: providerConfiguration, options: [.sortedKeys])
            return try PacketTunnelConfigurationCodec.decode(data).validated()
        } catch let error as PacketTunnelConfigurationError {
            throw error
        } catch {
            throw PacketTunnelProviderError.invalidConfiguration(String(describing: error))
        }
    }
}

private final class PacketTunnelNetworkSettingsDriver: PacketTunnelNetworkSettingsDriving, @unchecked Sendable {
    private weak var provider: NEPacketTunnelProvider?

    init(provider: NEPacketTunnelProvider) { self.provider = provider }

    func apply(configuration: PacketTunnelConfiguration, completion: @escaping @Sendable (Error?) -> Void) {
        guard let provider else {
            completion(PacketTunnelProviderError.networkSettingsFailed("provider deallocated"))
            return
        }
        do {
            let settings = try configuration.makeNetworkSettings()
            provider.setTunnelNetworkSettings(settings) { error in
                completion(error.map { PacketTunnelProviderError.networkSettingsFailed(String(describing: $0)) })
            }
        } catch {
            completion(error)
        }
    }

    func clear(completion: @escaping @Sendable (Error?) -> Void) {
        guard let provider else {
            completion(PacketTunnelProviderError.networkSettingsFailed("provider deallocated"))
            return
        }
        provider.setTunnelNetworkSettings(nil) { error in
            completion(error.map { PacketTunnelProviderError.networkSettingsFailed(String(describing: $0)) })
        }
    }
}

private final class ErrorCompletionBox: @unchecked Sendable {
    private let completion: (Error?) -> Void

    init(_ completion: @escaping (Error?) -> Void) { self.completion = completion }
    func call(_ error: Error?) { completion(error) }
}

private final class VoidCompletionBox: @unchecked Sendable {
    private let completion: () -> Void

    init(_ completion: @escaping () -> Void) { self.completion = completion }
    func call() { completion() }
}

private final class OptionalDataCompletionBox: @unchecked Sendable {
    private let completion: ((Data?) -> Void)?

    init(_ completion: ((Data?) -> Void)?) { self.completion = completion }
    func call(_ data: Data?) { completion?(data) }
}
#endif
