import Foundation

#if canImport(Darwin)
import Darwin
#elseif canImport(Glibc)
import Glibc
#endif

public enum IPFamily: String, Codable, Equatable, Sendable {
    case ipv4
    case ipv6
}

public enum IPRouteError: Error, Equatable {
    case invalidCIDR(String)
    case invalidAddress(String)
    case invalidPrefixLength(String)
}

/// An explicit, normalized destination route. Parsing is strict so malformed routes cannot silently become defaults.
public struct IPRoute: Codable, Equatable, Hashable, Sendable {
    public let address: String
    public let prefixLength: Int
    public let family: IPFamily

    public init(cidr: String) throws {
        let pieces = cidr.split(separator: "/", omittingEmptySubsequences: false)
        guard pieces.count == 2 else { throw IPRouteError.invalidCIDR(cidr) }
        let address = String(pieces[0]).trimmingCharacters(in: .whitespacesAndNewlines)
        guard !address.isEmpty else { throw IPRouteError.invalidAddress(cidr) }
        guard let prefix = Int(pieces[1]), prefix >= 0 else {
            throw IPRouteError.invalidPrefixLength(cidr)
        }

        if Self.isIPv4(address) {
            guard prefix <= 32 else { throw IPRouteError.invalidPrefixLength(cidr) }
            self.family = .ipv4
            self.address = address
            self.prefixLength = prefix
            return
        }
        if Self.isIPv6(address) {
            guard prefix <= 128 else { throw IPRouteError.invalidPrefixLength(cidr) }
            self.family = .ipv6
            self.address = address.lowercased()
            self.prefixLength = prefix
            return
        }
        throw IPRouteError.invalidAddress(cidr)
    }

    public var cidr: String { "\(address)/\(prefixLength)" }

    #if canImport(Darwin) || canImport(Glibc)
    private static func isIPv4(_ value: String) -> Bool {
        var storage = in_addr()
        return value.withCString { inet_pton(AF_INET, $0, &storage) == 1 }
    }

    private static func isIPv6(_ value: String) -> Bool {
        var storage = in6_addr()
        return value.withCString { inet_pton(AF_INET6, $0, &storage) == 1 }
    }
    #else
    private static func isIPv4(_ value: String) -> Bool { value.split(separator: ".").count == 4 }
    private static func isIPv6(_ value: String) -> Bool { value.contains(":") }
    #endif
}

/// The set of routes required to keep discovery/control traffic outside the tunnel.
public enum BypassAuthority: String, Codable, Equatable, Sendable {
    case carrierControl = "carrier_control"
}

public struct BypassSet: Codable, Equatable, Sendable {
    public let authority: BypassAuthority
    public let sourceEndpoints: [String]
    public let requiredHosts: [String]
    public let resolvedCIDRs: [String]
    public let resolvedCIDRsByHost: [String: [String]]
    public let resolutionComplete: Bool

    public static let empty = BypassSet(
        requiredHosts: [],
        resolvedCIDRs: [],
        resolvedCIDRsByHost: [:],
        sourceEndpoints: [],
        authority: .carrierControl,
        resolutionComplete: true
    )

    public init(
        requiredHosts: [String],
        resolvedCIDRs: [String],
        resolvedCIDRsByHost: [String: [String]] = [:],
        sourceEndpoints: [String] = [],
        authority: BypassAuthority = .carrierControl,
        resolutionComplete: Bool
    ) {
        self.requiredHosts = requiredHosts
        self.resolvedCIDRs = resolvedCIDRs
        self.resolvedCIDRsByHost = resolvedCIDRsByHost
        self.sourceEndpoints = sourceEndpoints
        self.authority = authority
        self.resolutionComplete = resolutionComplete
    }

    public func validatedRoutes() throws -> [IPRoute] {
        guard resolutionComplete else { throw PacketTunnelConfigurationError.bypassResolutionIncomplete }
        guard requiredHosts.allSatisfy({ !$0.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }) else {
            throw PacketTunnelConfigurationError.invalidBypassHost
        }
        guard requiredHosts.isEmpty || !resolvedCIDRs.isEmpty else {
            throw PacketTunnelConfigurationError.bypassAddressesMissing
        }
        if !requiredHosts.isEmpty {
            guard !sourceEndpoints.isEmpty else { throw PacketTunnelConfigurationError.bypassSourceEndpointsMissing }
            let endpointHosts = try Set(sourceEndpoints.map(Self.endpointHost))
            let required = Set(requiredHosts)
            guard endpointHosts == required else { throw PacketTunnelConfigurationError.bypassEndpointHostMismatch }
            guard Set(resolvedCIDRsByHost.keys) == required else {
                throw PacketTunnelConfigurationError.bypassHostMappingMismatch
            }
            for host in requiredHosts {
                guard let routes = resolvedCIDRsByHost[host], !routes.isEmpty else { throw PacketTunnelConfigurationError.bypassAddressesMissing }
                for cidr in routes {
                    let route: IPRoute
                    do { route = try IPRoute(cidr: cidr) }
                    catch { throw PacketTunnelConfigurationError.invalidBypassRoute(cidr) }
                    let requiredPrefix = route.family == .ipv4 ? 32 : 128
                    guard route.prefixLength == requiredPrefix else {
                        throw PacketTunnelConfigurationError.bypassRouteMustBeHost(cidr)
                    }
                }
            }
        }
        do {
            let aggregate = resolvedCIDRs
            let mapped = resolvedCIDRsByHost.values.flatMap { $0 }
            if !aggregate.isEmpty, !mapped.isEmpty, Set(aggregate) != Set(mapped) {
                throw PacketTunnelConfigurationError.bypassRouteMappingMismatch
            }
            let source = aggregate.isEmpty ? mapped : aggregate
            return try source.map(IPRoute.init(cidr:))
        } catch let error as PacketTunnelConfigurationError {
            throw error
        } catch {
            throw PacketTunnelConfigurationError.invalidBypassRoute(String(describing: error))
        }
    }

    private enum CodingKeys: String, CodingKey {
        case authority
        case sourceEndpoints = "source_endpoints"
        case requiredHosts = "required_hosts"
        case resolvedCIDRs = "resolved_cidrs"
        case resolvedCIDRsByHost = "resolved_cidrs_by_host"
        case resolutionComplete = "resolution_complete"
    }

    static func endpointHost(_ endpoint: String) throws -> String {
        guard let components = URLComponents(string: endpoint),
              components.scheme != nil,
              components.user == nil,
              components.password == nil,
              let parsedHost = components.host else {
            throw PacketTunnelConfigurationError.invalidBypassEndpoint(endpoint)
        }
        let host = parsedHost.hasPrefix("[") && parsedHost.hasSuffix("]") ? String(parsedHost.dropFirst().dropLast()) : parsedHost
        guard !host.isEmpty else { throw PacketTunnelConfigurationError.invalidBypassEndpoint(endpoint) }
        return host.lowercased()
    }
}

public struct DNSConfiguration: Codable, Equatable, Sendable {
    public let servers: [String]
    public let matchDomains: [String]
    public let searchDomains: [String]

    public init(servers: [String], matchDomains: [String] = [""], searchDomains: [String] = []) {
        self.servers = servers
        self.matchDomains = matchDomains
        self.searchDomains = searchDomains
    }

    fileprivate func validate() throws {
        guard !servers.isEmpty else { throw PacketTunnelConfigurationError.dnsServersMissing }
        for server in servers {
            let value = server.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !value.isEmpty, (try? IPRoute(cidr: "\(value)/\(value.contains(":") ? 128 : 32)")) != nil else {
                throw PacketTunnelConfigurationError.invalidDNSAddress(server)
            }
        }
        guard matchDomains.allSatisfy({ !$0.contains(" ") }) else {
            throw PacketTunnelConfigurationError.invalidDNSMatchDomain
        }
    }
}

public enum PacketTunnelRouteMode: String, Codable, Equatable, Sendable {
    case fullTunnel = "full_tunnel"
    case destinationSplit = "destination_split"
}

public enum PacketTunnelConfigurationError: Error, Equatable, LocalizedError, Sendable {
    case invalidRemoteAddress(String)
    case invalidMTU(Int)
    case invalidIPv6PrefixLength(Int)
    case invalidIPv4SubnetMask(String)
    case invalidDNSAddress(String)
    case dnsServersMissing
    case invalidDNSMatchDomain
    case bypassResolutionIncomplete
    case bypassAddressesMissing
    case bypassSourceEndpointsMissing
    case bypassEndpointHostMismatch
    case bypassRouteMappingMismatch
    case bypassHostMappingMismatch
    case bypassRouteMustBeHost(String)
    case fullTunnelBypassRequired
    case invalidBypassHost
    case invalidBypassEndpoint(String)
    case invalidBypassRoute(String)
    case invalidUserBypassRoute(String)
    case userBypassRouteMustNotBeDefault(String)
    case invalidDestinationRoute(String)
    case destinationRoutesRequired
    case destinationsNotAllowedInFullTunnel
    case invalidRuntimeProfileIdentity
    case expiredRuntimeProfile
    case staleRuntimeProfile

    public var errorDescription: String? {
        switch self {
        case .invalidRemoteAddress(let value): return "invalid tunnel remote address: \(value)"
        case .invalidMTU(let value): return "invalid tunnel MTU: \(value)"
        case .invalidIPv6PrefixLength(let value): return "invalid IPv6 prefix length: \(value)"
        case .invalidIPv4SubnetMask(let value): return "invalid IPv4 subnet mask: \(value)"
        case .invalidDNSAddress(let value): return "invalid DNS address: \(value)"
        case .dnsServersMissing: return "at least one DNS server is required"
        case .invalidDNSMatchDomain: return "DNS match domains contain whitespace"
        case .bypassResolutionIncomplete: return "bypass host resolution is incomplete"
        case .bypassAddressesMissing: return "bypass hosts have no resolved addresses"
        case .bypassSourceEndpointsMissing: return "bypass routes lack confirmed runtime carrier/control endpoints"
        case .bypassEndpointHostMismatch: return "bypass hosts do not exactly match confirmed runtime endpoints"
        case .bypassRouteMappingMismatch: return "bypass route mapping does not match the aggregate route set"
        case .bypassHostMappingMismatch: return "bypass host keys do not exactly match required carrier/control hosts"
        case .bypassRouteMustBeHost(let value): return "bypass route must be /32 or /128: \(value)"
        case .fullTunnelBypassRequired: return "full tunnel requires authoritative carrier/control bypass hosts"
        case .invalidBypassHost: return "bypass host is empty"
        case .invalidBypassEndpoint(let value): return "invalid carrier/control endpoint: \(value)"
        case .invalidBypassRoute(let value): return "invalid bypass route: \(value)"
        case .invalidUserBypassRoute(let value): return "invalid user bypass route: \(value)"
        case .userBypassRouteMustNotBeDefault(let value): return "user bypass route must not be a default route: \(value)"
        case .invalidDestinationRoute(let value): return "invalid destination route: \(value)"
        case .destinationRoutesRequired: return "destination split requires at least one destination route"
        case .destinationsNotAllowedInFullTunnel: return "full tunnel cannot include destination-only routes"
        case .invalidRuntimeProfileIdentity: return "runtime VPN profile identity is incomplete or invalid"
        case .expiredRuntimeProfile: return "runtime VPN profile validity deadline has expired"
        case .staleRuntimeProfile: return "runtime VPN profile revision or identity is stale"
        }
    }
}

/// Exact daemon status fields required to derive a Network Extension profile.
public struct RuntimeTunnelSnapshot: Equatable, Sendable {
    public let daemonInstanceID: String
    public let profileRevision: UInt64
    public let profileHash: String
    public let sessionID: String
    public let profileValidUntil: Date
    public let confirmedSocksAddress: String
    public let carrierControlEndpoints: [String]
    public let dnsSnapshot: [String: [String]]
    public let dnsServers: [String]

    public init(
        daemonInstanceID: String,
        profileRevision: UInt64,
        profileHash: String,
        sessionID: String,
        profileValidUntil: Date,
        confirmedSocksAddress: String,
        carrierControlEndpoints: [String],
        dnsSnapshot: [String: [String]],
        dnsServers: [String]
    ) {
        self.daemonInstanceID = daemonInstanceID
        self.profileRevision = profileRevision
        self.profileHash = profileHash
        self.sessionID = sessionID
        self.profileValidUntil = profileValidUntil
        self.confirmedSocksAddress = confirmedSocksAddress
        self.carrierControlEndpoints = carrierControlEndpoints
        self.dnsSnapshot = dnsSnapshot
        self.dnsServers = dnsServers
    }

    public var profileIdentity: RuntimeProfileIdentity {
        RuntimeProfileIdentity(
            daemonInstanceID: daemonInstanceID,
            profileRevision: profileRevision,
            profileHash: profileHash,
            sessionID: sessionID
        )
    }
}

/// Rejects asynchronous profile delivery that moves backwards or replaces a session at one generation.
public final class RuntimeProfileGenerationGuard: @unchecked Sendable {
    private let lock = NSLock()
    private var latestIdentity: RuntimeProfileIdentity?

    public init() {}

    public func accept(_ identity: RuntimeProfileIdentity) throws {
        do { try identity.validated() }
        catch { throw PacketTunnelConfigurationError.invalidRuntimeProfileIdentity }
        lock.lock()
        defer { lock.unlock() }
        if let current = latestIdentity {
            if identity.profileRevision < current.profileRevision ||
               (identity.profileRevision == current.profileRevision && identity != current) {
                throw PacketTunnelConfigurationError.staleRuntimeProfile
            }
            if identity.profileRevision > current.profileRevision { latestIdentity = identity }
        } else {
            latestIdentity = identity
        }
    }

    public func reset() {
        lock.lock()
        latestIdentity = nil
        lock.unlock()
    }
}

/// Builds the profile from one confirmed runtime snapshot; arbitrary bypass labels are not trusted.
public struct RuntimeTunnelProfileBuilder: Sendable {
    public init() {}

    public func build(
        runtime: RuntimeTunnelSnapshot,
        routeMode: PacketTunnelRouteMode,
        destinationCIDRs: [String],
        mtu: Int = 1_500
    ) throws -> PacketTunnelConfiguration {
        let profileIdentity: RuntimeProfileIdentity
        do { profileIdentity = try runtime.profileIdentity.validated() }
        catch { throw PacketTunnelConfigurationError.invalidRuntimeProfileIdentity }
        let endpointHosts = try Set(runtime.carrierControlEndpoints.map(endpointHost)).sorted()
        guard !endpointHosts.isEmpty else { throw PacketTunnelConfigurationError.bypassSourceEndpointsMissing }
        guard Set(runtime.dnsSnapshot.keys.map { $0.lowercased() }) == Set(endpointHosts) else {
            throw PacketTunnelConfigurationError.bypassHostMappingMismatch
        }

        var routesByHost: [String: [String]] = [:]
        for host in endpointHosts {
            guard let addresses = runtime.dnsSnapshot.first(where: { $0.key.lowercased() == host })?.value,
                  !addresses.isEmpty else {
                throw PacketTunnelConfigurationError.bypassAddressesMissing
            }
            routesByHost[host] = try addresses.map(hostRoute)
        }
        let aggregateRoutes = routesByHost.values.flatMap { $0 }.sorted()
        let configuration = PacketTunnelConfiguration(
            remoteAddress: "127.0.0.1",
            daemonInstanceID: profileIdentity.daemonInstanceID,
            profileRevision: profileIdentity.profileRevision,
            profileHash: profileIdentity.profileHash,
            sessionID: profileIdentity.sessionID,
            profileValidUntil: runtime.profileValidUntil,
            socksEndpoint: try RuntimeLoopbackSocksEndpoint(confirmedAddress: runtime.confirmedSocksAddress),
            routeMode: routeMode,
            destinationCIDRs: destinationCIDRs,
            bypass: BypassSet(
                requiredHosts: endpointHosts,
                resolvedCIDRs: aggregateRoutes,
                resolvedCIDRsByHost: routesByHost,
                sourceEndpoints: runtime.carrierControlEndpoints,
                resolutionComplete: true
            ),
            dns: DNSConfiguration(servers: runtime.dnsServers),
            mtu: mtu
        )
        return try configuration.validated()
    }

    private func endpointHost(_ endpoint: String) throws -> String {
        try BypassSet.endpointHost(endpoint)
    }

    private func hostRoute(_ address: String) throws -> String {
        if (try? IPRoute(cidr: "\(address)/32"))?.family == .ipv4 { return "\(address)/32" }
        if (try? IPRoute(cidr: "\(address)/128"))?.family == .ipv6 { return "\(address.lowercased())/128" }
        throw PacketTunnelConfigurationError.invalidBypassRoute(address)
    }
}

public struct PacketTunnelRoutePlan: Equatable, Sendable {
    public let includedIPv4: [IPRoute]
    public let includedIPv6: [IPRoute]
    public let excluded: [IPRoute]

    public init(includedIPv4: [IPRoute], includedIPv6: [IPRoute], excluded: [IPRoute]) {
        self.includedIPv4 = includedIPv4
        self.includedIPv6 = includedIPv6
        self.excluded = excluded
    }
}

public struct PacketTunnelConfiguration: Codable, Equatable, Sendable {
    public let remoteAddress: String
    public let daemonInstanceID: String
    public let profileRevision: UInt64
    public let profileHash: String
    public let sessionID: String
    public let profileValidUntil: Date
    public let socksEndpoint: RuntimeLoopbackSocksEndpoint
    public let routeMode: PacketTunnelRouteMode
    public let destinationCIDRs: [String]
    public let userBypassCIDRs: [String]
    public let bypass: BypassSet
    public let dns: DNSConfiguration
    public let mtu: Int
    public let tunnelIPv4Address: String
    public let tunnelIPv4SubnetMask: String
    public let tunnelIPv6Address: String
    public let tunnelIPv6PrefixLength: Int

    public init(
        remoteAddress: String,
        daemonInstanceID: String,
        profileRevision: UInt64,
        profileHash: String,
        sessionID: String,
        profileValidUntil: Date,
        socksEndpoint: RuntimeLoopbackSocksEndpoint,
        routeMode: PacketTunnelRouteMode,
        destinationCIDRs: [String],
        userBypassCIDRs: [String] = [],
        bypass: BypassSet,
        dns: DNSConfiguration,
        mtu: Int = 1500,
        tunnelIPv4Address: String = "198.18.0.2",
        tunnelIPv4SubnetMask: String = "255.255.255.0",
        tunnelIPv6Address: String = "fd00:5754:0001::2",
        tunnelIPv6PrefixLength: Int = 64
    ) {
        self.remoteAddress = remoteAddress
        self.daemonInstanceID = daemonInstanceID
        self.profileRevision = profileRevision
        self.profileHash = profileHash
        self.sessionID = sessionID
        self.profileValidUntil = profileValidUntil
        self.socksEndpoint = socksEndpoint
        self.routeMode = routeMode
        self.destinationCIDRs = destinationCIDRs
        self.userBypassCIDRs = userBypassCIDRs
        self.bypass = bypass
        self.dns = dns
        self.mtu = mtu
        self.tunnelIPv4Address = tunnelIPv4Address
        self.tunnelIPv4SubnetMask = tunnelIPv4SubnetMask
        self.tunnelIPv6Address = tunnelIPv6Address
        self.tunnelIPv6PrefixLength = tunnelIPv6PrefixLength
    }

    private enum CodingKeys: String, CodingKey {
        case remoteAddress = "remote_address"
        case daemonInstanceID = "daemon_instance_id"
        case profileRevision = "profile_revision"
        case profileHash = "profile_hash"
        case sessionID = "session_id"
        case profileValidUntil = "profile_valid_until"
        case socksEndpoint = "socks_endpoint"
        case routeMode = "route_mode"
        case destinationCIDRs = "destination_cidrs"
        case userBypassCIDRs = "user_bypass_cidrs"
        case bypass
        case dns
        case mtu
        case tunnelIPv4Address = "tunnel_ipv4_address"
        case tunnelIPv4SubnetMask = "tunnel_ipv4_subnet_mask"
        case tunnelIPv6Address = "tunnel_ipv6_address"
        case tunnelIPv6PrefixLength = "tunnel_ipv6_prefix_length"
    }

    @discardableResult
    public func validated(now: Date = Date()) throws -> PacketTunnelConfiguration {
        do { try profileIdentity.validated() }
        catch { throw PacketTunnelConfigurationError.invalidRuntimeProfileIdentity }
        guard profileValidUntil > now else { throw PacketTunnelConfigurationError.expiredRuntimeProfile }
        let remote = remoteAddress.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !remote.isEmpty, !remote.contains("\n"), !remote.contains("\r") else {
            throw PacketTunnelConfigurationError.invalidRemoteAddress(remoteAddress)
        }
        guard (576...65535).contains(mtu) else { throw PacketTunnelConfigurationError.invalidMTU(mtu) }
        guard (0...128).contains(tunnelIPv6PrefixLength) else {
            throw PacketTunnelConfigurationError.invalidIPv6PrefixLength(tunnelIPv6PrefixLength)
        }
        guard (try? IPRoute(cidr: "\(tunnelIPv4Address)/32"))?.family == .ipv4,
              Self.isContiguousIPv4Mask(tunnelIPv4SubnetMask),
              (try? IPRoute(cidr: "\(tunnelIPv6Address)/128"))?.family == .ipv6 else {
            if !Self.isContiguousIPv4Mask(tunnelIPv4SubnetMask) {
                throw PacketTunnelConfigurationError.invalidIPv4SubnetMask(tunnelIPv4SubnetMask)
            }
            throw PacketTunnelConfigurationError.invalidRemoteAddress("invalid tunnel interface address")
        }
        try dns.validate()
        let bypassRoutes = try bypass.validatedRoutes()

        if routeMode == .fullTunnel, bypass.requiredHosts.isEmpty || bypassRoutes.isEmpty {
            throw PacketTunnelConfigurationError.fullTunnelBypassRequired
        }
        if routeMode == .destinationSplit, bypass.requiredHosts.isEmpty || bypassRoutes.isEmpty {
            throw PacketTunnelConfigurationError.bypassSourceEndpointsMissing
        }

        if routeMode == .fullTunnel, !destinationCIDRs.isEmpty {
            throw PacketTunnelConfigurationError.destinationsNotAllowedInFullTunnel
        }
        if routeMode == .destinationSplit, destinationCIDRs.isEmpty {
            throw PacketTunnelConfigurationError.destinationRoutesRequired
        }
        for destination in destinationCIDRs {
            do { _ = try IPRoute(cidr: destination) }
            catch { throw PacketTunnelConfigurationError.invalidDestinationRoute(destination) }
        }
        _ = try validatedUserBypassRoutes()
        return self
    }

    public var profileIdentity: RuntimeProfileIdentity {
        RuntimeProfileIdentity(
            daemonInstanceID: daemonInstanceID,
            profileRevision: profileRevision,
            profileHash: profileHash,
            sessionID: sessionID
        )
    }

    private static func isContiguousIPv4Mask(_ value: String) -> Bool {
        let octets = value.split(separator: ".", omittingEmptySubsequences: false).compactMap { Int($0) }
        guard octets.count == 4 else { return false }
        var sawZero = false
        for octet in octets {
            guard (0...255).contains(octet) else { return false }
            for bit in stride(from: 7, through: 0, by: -1) {
                let one = (octet & (1 << bit)) != 0
                if one, sawZero { return false }
                if !one { sawZero = true }
            }
        }
        return true
    }

    public func validatedRoutePlan() throws -> PacketTunnelRoutePlan {
        try validated()
        let destinations = try destinationCIDRs.map { try IPRoute(cidr: $0) }
        let carrierBypassRoutes = try bypass.validatedRoutes()
        let userBypassRoutes = try validatedUserBypassRoutes()
        var seenExcludedRoutes = Set<IPRoute>()
        let excludedRoutes = (carrierBypassRoutes + userBypassRoutes).filter {
            seenExcludedRoutes.insert($0).inserted
        }
        let included: [IPRoute]
        if routeMode == .fullTunnel {
            included = [try IPRoute(cidr: "0.0.0.0/0"), try IPRoute(cidr: "::/0")]
        } else {
            included = destinations
        }
        return PacketTunnelRoutePlan(
            includedIPv4: included.filter { $0.family == .ipv4 },
            includedIPv6: included.filter { $0.family == .ipv6 },
            excluded: excludedRoutes
        )
    }

    private func validatedUserBypassRoutes() throws -> [IPRoute] {
        try userBypassCIDRs.map { cidr in
            let route: IPRoute
            do { route = try IPRoute(cidr: cidr) }
            catch { throw PacketTunnelConfigurationError.invalidUserBypassRoute(cidr) }
            guard route.prefixLength > 0 else {
                throw PacketTunnelConfigurationError.userBypassRouteMustNotBeDefault(cidr)
            }
            return route
        }
    }
}

public enum PacketTunnelConfigurationCodecError: Error, Equatable, LocalizedError, Sendable {
    case invalidJSONObject

    public var errorDescription: String? { "packet-tunnel configuration is not a JSON object" }
}

/// Canonical whole-second ISO-8601 codec shared by Go input, preferences, and the provider.
public enum PacketTunnelConfigurationCodec {
    public static func encode(_ configuration: PacketTunnelConfiguration) throws -> Data {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        encoder.outputFormatting = [.sortedKeys]
        return try encoder.encode(configuration)
    }

    public static func decode(_ data: Data) throws -> PacketTunnelConfiguration {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        return try decoder.decode(PacketTunnelConfiguration.self, from: data)
    }

    public static func dictionary(_ configuration: PacketTunnelConfiguration) throws -> [String: Any] {
        guard let object = try JSONSerialization.jsonObject(with: encode(configuration)) as? [String: Any] else {
            throw PacketTunnelConfigurationCodecError.invalidJSONObject
        }
        return object
    }
}

#if canImport(NetworkExtension)
import NetworkExtension

public extension PacketTunnelConfiguration {
    /// Builds only public Network Extension settings; no packet-flow private API is used.
    func makeNetworkSettings() throws -> NEPacketTunnelNetworkSettings {
        let plan = try validatedRoutePlan()
        let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: remoteAddress)
        settings.mtu = NSNumber(value: mtu)

        let ipv4 = NEIPv4Settings(addresses: [tunnelIPv4Address], subnetMasks: [tunnelIPv4SubnetMask])
        ipv4.includedRoutes = plan.includedIPv4.map {
            NEIPv4Route(destinationAddress: $0.address, subnetMask: Self.ipv4Mask(prefixLength: $0.prefixLength))
        }
        ipv4.excludedRoutes = plan.excluded.filter { $0.family == .ipv4 }.map {
            NEIPv4Route(destinationAddress: $0.address, subnetMask: Self.ipv4Mask(prefixLength: $0.prefixLength))
        }
        settings.ipv4Settings = ipv4

        let ipv6 = NEIPv6Settings(
            addresses: [tunnelIPv6Address],
            networkPrefixLengths: [NSNumber(value: tunnelIPv6PrefixLength)]
        )
        ipv6.includedRoutes = plan.includedIPv6.map {
            NEIPv6Route(destinationAddress: $0.address, networkPrefixLength: NSNumber(value: $0.prefixLength))
        }
        ipv6.excludedRoutes = plan.excluded.filter { $0.family == .ipv6 }.map {
            NEIPv6Route(destinationAddress: $0.address, networkPrefixLength: NSNumber(value: $0.prefixLength))
        }
        settings.ipv6Settings = ipv6

        let dnsSettings = NEDNSSettings(servers: dns.servers)
        dnsSettings.matchDomains = dns.matchDomains
        dnsSettings.searchDomains = dns.searchDomains
        settings.dnsSettings = dnsSettings
        return settings
    }

    private static func ipv4Mask(prefixLength: Int) -> String {
        guard prefixLength > 0 else { return "0.0.0.0" }
        let value = UInt32.max << UInt32(32 - prefixLength)
        return "\((value >> 24) & 255).\((value >> 16) & 255).\((value >> 8) & 255).\(value & 255)"
    }
}
#endif
