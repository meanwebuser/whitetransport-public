import Foundation
import NetworkExtension

final class SystemVPNManager {
    static let shared = SystemVPNManager()

    private let description = "Whitelist Bypass System VPN"
    private let providerBundleIdentifier = "bypass.whitelist.whitelist-bypass-proxy.PacketTunnel"

    var isPacketTunnelBundled: Bool {
        Bundle.main.url(forResource: "PacketTunnel", withExtension: "appex", subdirectory: "PlugIns") != nil
    }

    private init() {}

    func install(callURL: String, completion: @escaping (Result<String, Error>) -> Void) {
        guard isPacketTunnelBundled else {
            completion(.failure(SystemVPNError.packetTunnelExtensionMissing))
            return
        }
        loadOrCreateManager { result in
            switch result {
            case .failure(let error): completion(.failure(error))
            case .success(let manager):
                let proto = NETunnelProviderProtocol()
                proto.providerBundleIdentifier = self.providerBundleIdentifier
                proto.serverAddress = "whitelist-bypass"
                proto.providerConfiguration = [
                    "callURL": callURL,
                    "mode": "packet-tunnel-scaffold",
                    "tunnelAddress": "10.64.0.2",
                    "tunnelSubnetMask": "255.255.255.0",
                    "remoteAddress": "10.64.0.1",
                    "mtu": NSNumber(value: 1500),
                    "dnsServers": "1.1.1.1,8.8.8.8",
                    "socksHost": "127.0.0.1",
                    "socksPort": NSNumber(value: 1080),
                    "socksUser": "",
                    "socksPass": "",
                    "routeAllTraffic": NSNumber(value: false),
                    "forwardingEnabled": NSNumber(value: false)
                ]
                proto.disconnectOnSleep = false

                manager.localizedDescription = self.description
                manager.protocolConfiguration = proto
                manager.isEnabled = true
                manager.saveToPreferences { error in
                    if let error = error {
                        completion(.failure(error))
                    } else {
                        completion(.success("VPN profile installed"))
                    }
                }
            }
        }
    }

    func start(callURL: String, completion: @escaping (Result<String, Error>) -> Void) {
        install(callURL: callURL) { result in
            switch result {
            case .failure(let error): completion(.failure(error))
            case .success:
                self.loadOrCreateManager { loadResult in
                    switch loadResult {
                    case .failure(let error): completion(.failure(error))
                    case .success(let manager):
                        do {
                            try manager.connection.startVPNTunnel(options: [
                                "callURL": callURL as NSString,
                                "routeAllTraffic": NSNumber(value: false),
                                "forwardingEnabled": NSNumber(value: false)
                            ])
                            completion(.success("VPN start requested"))
                        } catch {
                            completion(.failure(error))
                        }
                    }
                }
            }
        }
    }

    func stop(completion: @escaping (Result<String, Error>) -> Void) {
        loadOrCreateManager { result in
            switch result {
            case .failure(let error): completion(.failure(error))
            case .success(let manager):
                manager.connection.stopVPNTunnel()
                completion(.success("VPN stop requested"))
            }
        }
    }

    func status(completion: @escaping (String) -> Void) {
        loadOrCreateManager { result in
            switch result {
            case .failure(let error): completion("VPN error: \(error.localizedDescription)")
            case .success(let manager): completion("VPN status: \(manager.connection.status.label)")
            }
        }
    }

    private func loadOrCreateManager(completion: @escaping (Result<NETunnelProviderManager, Error>) -> Void) {
        NETunnelProviderManager.loadAllFromPreferences { managers, error in
            if let error = error {
                completion(.failure(error))
                return
            }
            if let existing = managers?.first(where: { $0.localizedDescription == self.description }) {
                existing.loadFromPreferences { error in
                    if let error = error { completion(.failure(error)) }
                    else { completion(.success(existing)) }
                }
                return
            }
            completion(.success(NETunnelProviderManager()))
        }
    }
}

private extension NEVPNStatus {
    var label: String {
        switch self {
        case .invalid: return "invalid"
        case .disconnected: return "disconnected"
        case .connecting: return "connecting"
        case .connected: return "connected"
        case .reasserting: return "reasserting"
        case .disconnecting: return "disconnecting"
        @unknown default: return "unknown"
        }
    }
}


enum SystemVPNError: LocalizedError {
    case packetTunnelExtensionMissing

    var errorDescription: String? {
        switch self {
        case .packetTunnelExtensionMissing:
            return "PacketTunnel extension is not bundled in this build. Install the VPN build signed with NetworkExtension entitlement."
        }
    }
}
