import Foundation
import NetworkExtension

private struct PacketTunnelRuntimeConfig {
    let tunnelAddress: String
    let tunnelSubnetMask: String
    let remoteAddress: String
    let mtu: Int
    let dnsServers: [String]
    let routeAllTraffic: Bool
    let forwardingEnabled: Bool
    let socksHost: String
    let socksPort: Int
    let socksUser: String
    let socksPass: String

    static func load(from providerConfig: [String: Any]?, options: [String: NSObject]?) -> PacketTunnelRuntimeConfig {
        func string(_ key: String, _ fallback: String) -> String {
            if let value = options?[key] as? String, !value.isEmpty { return value }
            if let value = providerConfig?[key] as? String, !value.isEmpty { return value }
            return fallback
        }
        func bool(_ key: String, _ fallback: Bool) -> Bool {
            if let value = options?[key] as? NSNumber { return value.boolValue }
            if let value = providerConfig?[key] as? NSNumber { return value.boolValue }
            if let value = providerConfig?[key] as? Bool { return value }
            return fallback
        }
        func int(_ key: String, _ fallback: Int) -> Int {
            if let value = options?[key] as? NSNumber { return value.intValue }
            if let value = providerConfig?[key] as? NSNumber { return value.intValue }
            if let value = providerConfig?[key] as? Int { return value }
            return fallback
        }
        let dnsRaw = string("dnsServers", "1.1.1.1,8.8.8.8")
        let dns = dnsRaw
            .split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty }

        return PacketTunnelRuntimeConfig(
            tunnelAddress: string("tunnelAddress", "10.64.0.2"),
            tunnelSubnetMask: string("tunnelSubnetMask", "255.255.255.0"),
            remoteAddress: string("remoteAddress", "10.64.0.1"),
            mtu: int("mtu", 1500),
            dnsServers: dns.isEmpty ? ["1.1.1.1", "8.8.8.8"] : dns,
            routeAllTraffic: bool("routeAllTraffic", false),
            forwardingEnabled: bool("forwardingEnabled", false),
            socksHost: string("socksHost", "127.0.0.1"),
            socksPort: int("socksPort", 1080),
            socksUser: string("socksUser", ""),
            socksPass: string("socksPass", "")
        )
    }
}

final class PacketTunnelProvider: NEPacketTunnelProvider {
    private var isReadingPackets = false
    private var runtimeConfig: PacketTunnelRuntimeConfig?

    override func startTunnel(options: [String : NSObject]?, completionHandler: @escaping (Error?) -> Void) {
        let providerConfig = (protocolConfiguration as? NETunnelProviderProtocol)?.providerConfiguration
        let config = PacketTunnelRuntimeConfig.load(from: providerConfig, options: options)
        runtimeConfig = config
        NSLog("[PacketTunnel] startTunnel routeAllTraffic=\(config.routeAllTraffic) forwardingEnabled=\(config.forwardingEnabled) socks=\(config.socksHost):\(config.socksPort) mtu=\(config.mtu)")

        let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: config.remoteAddress)
        settings.mtu = NSNumber(value: config.mtu)

        let ipv4 = NEIPv4Settings(addresses: [config.tunnelAddress], subnetMasks: [config.tunnelSubnetMask])
        if config.routeAllTraffic && config.forwardingEnabled {
            ipv4.includedRoutes = [NEIPv4Route.default()]
            NSLog("[PacketTunnel] default IPv4 route enabled")
        } else {
            // Safety: do not claim the default route until packetFlow -> tun2socks is wired.
            // Otherwise iOS will send traffic into this extension and it will be dropped.
            ipv4.includedRoutes = []
            NSLog("[PacketTunnel] default IPv4 route disabled; scaffold/proxy-safe mode")
        }
        settings.ipv4Settings = ipv4

        let dns = NEDNSSettings(servers: config.dnsServers)
        dns.matchDomains = config.routeAllTraffic && config.forwardingEnabled ? [""] : []
        settings.dnsSettings = dns

        setTunnelNetworkSettings(settings) { [weak self] error in
            if let error = error {
                NSLog("[PacketTunnel] setTunnelNetworkSettings error: \(error)")
                completionHandler(error)
                return
            }
            self?.isReadingPackets = true
            self?.readPacketsLoop()
            NSLog("[PacketTunnel] started; forwardingEnabled=\(config.forwardingEnabled)")
            completionHandler(nil)
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        NSLog("[PacketTunnel] stopTunnel reason=\(reason.rawValue)")
        isReadingPackets = false
        runtimeConfig = nil
        completionHandler()
    }

    override func handleAppMessage(_ messageData: Data, completionHandler: ((Data?) -> Void)?) {
        let text = String(data: messageData, encoding: .utf8) ?? ""
        NSLog("[PacketTunnel] app message: \(text)")
        let payload: [String: Any] = [
            "ok": true,
            "forwardingEnabled": runtimeConfig?.forwardingEnabled ?? false,
            "routeAllTraffic": runtimeConfig?.routeAllTraffic ?? false,
            "socksHost": runtimeConfig?.socksHost ?? "",
            "socksPort": runtimeConfig?.socksPort ?? 0
        ]
        let data = try? JSONSerialization.data(withJSONObject: payload)
        completionHandler?(data ?? "ok".data(using: .utf8))
    }

    private func readPacketsLoop() {
        guard isReadingPackets else { return }
        packetFlow.readPackets { [weak self] packets, protocols in
            guard let self else { return }
            if !packets.isEmpty {
                if self.runtimeConfig?.forwardingEnabled == true {
                    NSLog("[PacketTunnel] forwarding not implemented yet; dropping \(packets.count) packets")
                } else {
                    NSLog("[PacketTunnel] scaffold received \(packets.count) packets; dropped by design")
                }
            }
            self.readPacketsLoop()
        }
    }
}
