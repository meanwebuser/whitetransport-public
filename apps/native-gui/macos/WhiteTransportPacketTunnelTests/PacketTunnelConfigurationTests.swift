import Foundation
import XCTest
@testable import WhiteTransportMacOS
#if canImport(NetworkExtension)
@preconcurrency import NetworkExtension
#endif

final class PacketTunnelConfigurationTests: XCTestCase {
    func testGoWailsBridgeJSONDecodesExactIdentityAndUserBypassRoutes() throws {
        let profileHash = String(repeating: "ab", count: 32)
        let json = """
        {
          "remote_address": "127.0.0.1",
          "daemon_instance_id": "daemon-go-bridge",
          "profile_revision": 23,
          "profile_hash": "\(profileHash)",
          "session_id": "session-go-bridge",
          "profile_valid_until": "2030-01-02T03:04:05Z",
          "socks_endpoint": {"host": "127.0.0.1", "port": 41723},
          "route_mode": "full_tunnel",
          "destination_cidrs": [],
          "user_bypass_cidrs": ["10.0.0.0/8", "fd42::/16"],
          "bypass": {
            "authority": "carrier_control",
            "source_endpoints": ["https://control.example.test:443"],
            "required_hosts": ["control.example.test"],
            "resolved_cidrs": ["198.51.100.7/32", "2001:db8:ffff::7/128"],
            "resolved_cidrs_by_host": {
              "control.example.test": ["198.51.100.7/32", "2001:db8:ffff::7/128"]
            },
            "resolution_complete": true
          },
          "dns": {
            "servers": ["1.1.1.1", "2606:4700:4700::1111"],
            "matchDomains": [""],
            "searchDomains": []
          },
          "mtu": 1500,
          "tunnel_ipv4_address": "198.18.0.2",
          "tunnel_ipv4_subnet_mask": "255.255.255.0",
          "tunnel_ipv6_address": "fd00:5754:0001::2",
          "tunnel_ipv6_prefix_length": 64
        }
        """

        let configuration = try PacketTunnelConfigurationCodec.decode(Data(json.utf8))

        XCTAssertEqual(configuration.profileIdentity, RuntimeProfileIdentity(
            daemonInstanceID: "daemon-go-bridge",
            profileRevision: 23,
            profileHash: profileHash,
            sessionID: "session-go-bridge"
        ))
        XCTAssertEqual(configuration.profileValidUntil, try XCTUnwrap(ISO8601DateFormatter().date(from: "2030-01-02T03:04:05Z")))
        XCTAssertEqual(configuration.userBypassCIDRs, ["10.0.0.0/8", "fd42::/16"])
        XCTAssertNoThrow(try configuration.validated())
        let plan = try configuration.validatedRoutePlan()
        XCTAssertEqual(Set(plan.includedIPv4), Set([try IPRoute(cidr: "0.0.0.0/0")]))
        XCTAssertEqual(Set(plan.includedIPv6), Set([try IPRoute(cidr: "::/0")]))
        XCTAssertEqual(Set(plan.excluded), Set([
            try IPRoute(cidr: "198.51.100.7/32"),
            try IPRoute(cidr: "2001:db8:ffff::7/128"),
            try IPRoute(cidr: "10.0.0.0/8"),
            try IPRoute(cidr: "fd42::/16")
        ]))
        XCTAssertEqual(
            try PacketTunnelConfigurationCodec.decode(PacketTunnelConfigurationCodec.encode(configuration)),
            configuration
        )
        XCTAssertEqual(
            try PacketTunnelConfigurationCodec.dictionary(configuration)["profile_valid_until"] as? String,
            "2030-01-02T03:04:05Z"
        )

        let missingDeadline = json.replacingOccurrences(
            of: "\"profile_valid_until\": \"2030-01-02T03:04:05Z\",\n",
            with: ""
        )
        XCTAssertThrowsError(try PacketTunnelConfigurationCodec.decode(Data(missingDeadline.utf8)))

        let malformedDeadline = json.replacingOccurrences(of: "2030-01-02T03:04:05Z", with: "not-a-deadline")
        XCTAssertThrowsError(try PacketTunnelConfigurationCodec.decode(Data(malformedDeadline.utf8)))

        let expiredJSON = json.replacingOccurrences(of: "2030-01-02T03:04:05Z", with: "2020-01-02T03:04:05Z")
        let expired = try PacketTunnelConfigurationCodec.decode(Data(expiredJSON.utf8))
        XCTAssertThrowsError(try expired.validated(now: Date(timeIntervalSince1970: 1_700_000_000))) { error in
            XCTAssertEqual(error as? PacketTunnelConfigurationError, .expiredRuntimeProfile)
        }
        let equalNowJSON = json.replacingOccurrences(of: "2030-01-02T03:04:05Z", with: "2023-11-14T22:13:20Z")
        let equalNow = try PacketTunnelConfigurationCodec.decode(Data(equalNowJSON.utf8))
        XCTAssertThrowsError(try equalNow.validated(now: Date(timeIntervalSince1970: 1_700_000_000))) { error in
            XCTAssertEqual(error as? PacketTunnelConfigurationError, .expiredRuntimeProfile)
        }
    }

    func testUserBypassRejectsMalformedAndDefaultCIDRs() {
        for (cidr, expectedError) in [
            ("not-a-cidr", PacketTunnelConfigurationError.invalidUserBypassRoute("not-a-cidr")),
            ("0.0.0.0/0", PacketTunnelConfigurationError.userBypassRouteMustNotBeDefault("0.0.0.0/0")),
            ("::/0", PacketTunnelConfigurationError.userBypassRouteMustNotBeDefault("::/0"))
        ] {
            let configuration = PacketTunnelConfiguration(
                remoteAddress: "198.18.0.1",
                daemonInstanceID: "daemon-user-bypass",
                profileRevision: 2,
                profileHash: String(repeating: "7", count: 64),
                sessionID: "session-user-bypass",
                profileValidUntil: Date(timeIntervalSince1970: 4_102_444_800),
                socksEndpoint: testSocksEndpoint(),
                routeMode: .fullTunnel,
                destinationCIDRs: [],
                userBypassCIDRs: [cidr],
                bypass: strictBypass(),
                dns: DNSConfiguration(servers: ["1.1.1.1"])
            )
            XCTAssertThrowsError(try configuration.validated()) { error in
                XCTAssertEqual(error as? PacketTunnelConfigurationError, expectedError)
            }
        }
    }

    func testRuntimeProfileBuilderDerivesExactDualStackHostBypassesAndLoopbackSocks() throws {
        let runtime = RuntimeTunnelSnapshot(
            daemonInstanceID: "daemon-current",
            profileRevision: 17,
            profileHash: String(repeating: "a", count: 64),
            sessionID: "session-current",
            profileValidUntil: Date(timeIntervalSince1970: 4_102_444_800),
            confirmedSocksAddress: "127.0.0.1:1085",
            carrierControlEndpoints: [
                "https://control.example.test/session",
                "https://[2001:db8::44]/bootstrap"
            ],
            dnsSnapshot: [
                "control.example.test": ["198.51.100.7", "2001:db8:ffff::7"],
                "2001:db8::44": ["2001:db8::44"]
            ],
            dnsServers: ["1.1.1.1", "2606:4700:4700::1111"]
        )

        let configuration = try RuntimeTunnelProfileBuilder().build(
            runtime: runtime,
            routeMode: .destinationSplit,
            destinationCIDRs: ["203.0.113.0/24"]
        )

        XCTAssertEqual(configuration.socksEndpoint.port, 1085)
        XCTAssertEqual(configuration.daemonInstanceID, "daemon-current")
        XCTAssertEqual(configuration.profileRevision, 17)
        XCTAssertEqual(configuration.profileHash, String(repeating: "a", count: 64))
        XCTAssertEqual(configuration.sessionID, "session-current")
        XCTAssertEqual(configuration.userBypassCIDRs, [])
        let encoded = try JSONEncoder().encode(configuration)
        let wire = try XCTUnwrap(JSONSerialization.jsonObject(with: encoded) as? [String: Any])
        XCTAssertEqual(wire["daemon_instance_id"] as? String, "daemon-current")
        XCTAssertEqual((wire["profile_revision"] as? NSNumber)?.uint64Value, 17)
        XCTAssertEqual(wire["profile_hash"] as? String, String(repeating: "a", count: 64))
        XCTAssertEqual(wire["session_id"] as? String, "session-current")
        XCTAssertNil(wire["runtime_generation"])
        XCTAssertNil(wire["runtime_session_id"])
        XCTAssertEqual(configuration.bypass.requiredHosts, ["2001:db8::44", "control.example.test"])
        XCTAssertEqual(Set(configuration.bypass.resolvedCIDRs), [
            "198.51.100.7/32", "2001:db8:ffff::7/128", "2001:db8::44/128"
        ])
        XCTAssertNoThrow(try configuration.validated())
        let plan = try configuration.validatedRoutePlan()
        XCTAssertEqual(plan.includedIPv4, [try IPRoute(cidr: "203.0.113.0/24")])
        XCTAssertEqual(Set(plan.excluded), Set([
            try IPRoute(cidr: "198.51.100.7/32"),
            try IPRoute(cidr: "2001:db8:ffff::7/128"),
            try IPRoute(cidr: "2001:db8::44/128")
        ]))
    }

    func testRuntimeProfileFailsClosedForMissingIdentityAndIncompleteDNSSnapshot() {
        let missingIdentity = RuntimeTunnelSnapshot(
            daemonInstanceID: "",
            profileRevision: 0,
            profileHash: "",
            sessionID: "",
            profileValidUntil: Date(timeIntervalSince1970: 4_102_444_800),
            confirmedSocksAddress: "127.0.0.1:1085",
            carrierControlEndpoints: ["https://control.example.test/session"],
            dnsSnapshot: ["control.example.test": ["198.51.100.7"]],
            dnsServers: ["1.1.1.1"]
        )
        XCTAssertThrowsError(try RuntimeTunnelProfileBuilder().build(
            runtime: missingIdentity,
            routeMode: .fullTunnel,
            destinationCIDRs: []
        )) { error in
            XCTAssertEqual(error as? PacketTunnelConfigurationError, .invalidRuntimeProfileIdentity)
        }

        let invalidHash = RuntimeTunnelSnapshot(
            daemonInstanceID: "daemon-current",
            profileRevision: 17,
            profileHash: "not-a-sha256",
            sessionID: "session-current",
            profileValidUntil: Date(timeIntervalSince1970: 4_102_444_800),
            confirmedSocksAddress: "127.0.0.1:1085",
            carrierControlEndpoints: ["https://control.example.test/session"],
            dnsSnapshot: ["control.example.test": ["198.51.100.7"]],
            dnsServers: ["1.1.1.1"]
        )
        XCTAssertThrowsError(try RuntimeTunnelProfileBuilder().build(
            runtime: invalidHash,
            routeMode: .fullTunnel,
            destinationCIDRs: []
        )) { error in
            XCTAssertEqual(error as? PacketTunnelConfigurationError, .invalidRuntimeProfileIdentity)
        }

        let incompleteDNS = RuntimeTunnelSnapshot(
            daemonInstanceID: "daemon-current",
            profileRevision: 18,
            profileHash: String(repeating: "a", count: 64),
            sessionID: "session-current",
            profileValidUntil: Date(timeIntervalSince1970: 4_102_444_800),
            confirmedSocksAddress: "127.0.0.1:1085",
            carrierControlEndpoints: ["https://control.example.test/session"],
            dnsSnapshot: [:],
            dnsServers: ["1.1.1.1"]
        )
        XCTAssertThrowsError(try RuntimeTunnelProfileBuilder().build(
            runtime: incompleteDNS,
            routeMode: .fullTunnel,
            destinationCIDRs: []
        )) { error in
            XCTAssertEqual(error as? PacketTunnelConfigurationError, .bypassHostMappingMismatch)
        }
    }

    func testRuntimeGenerationGuardRejectsStaleGenerationAndSessionReplacement() throws {
        let guardState = RuntimeProfileGenerationGuard()
        let profileHash = String(repeating: "b", count: 64)

        XCTAssertNoThrow(try guardState.accept(RuntimeProfileIdentity(
            daemonInstanceID: "daemon-a", profileRevision: 41, profileHash: profileHash, sessionID: "session-a"
        )))
        XCTAssertNoThrow(try guardState.accept(RuntimeProfileIdentity(
            daemonInstanceID: "daemon-a", profileRevision: 41, profileHash: profileHash, sessionID: "session-a"
        )))
        XCTAssertThrowsError(try guardState.accept(RuntimeProfileIdentity(
            daemonInstanceID: "daemon-a", profileRevision: 40, profileHash: profileHash, sessionID: "session-a"
        ))) { error in
            XCTAssertEqual(error as? PacketTunnelConfigurationError, .staleRuntimeProfile)
        }
        XCTAssertThrowsError(try guardState.accept(RuntimeProfileIdentity(
            daemonInstanceID: "daemon-a", profileRevision: 41, profileHash: profileHash, sessionID: "session-b"
        ))) { error in
            XCTAssertEqual(error as? PacketTunnelConfigurationError, .staleRuntimeProfile)
        }
        XCTAssertNoThrow(try guardState.accept(RuntimeProfileIdentity(
            daemonInstanceID: "daemon-b", profileRevision: 42, profileHash: profileHash, sessionID: "session-b"
        )))
    }

    func testBothRouteModesRejectCallerSuppliedBypassWithoutRuntimeEndpoints() {
        let unprovenBypass = BypassSet(
            requiredHosts: ["control.example.test"],
            resolvedCIDRs: ["198.51.100.7/32"],
            resolvedCIDRsByHost: ["control.example.test": ["198.51.100.7/32"]],
            resolutionComplete: true
        )
        for mode in [PacketTunnelRouteMode.fullTunnel, .destinationSplit] {
            let configuration = PacketTunnelConfiguration(
                remoteAddress: "198.18.0.1",
                daemonInstanceID: "daemon-test",
                profileRevision: 1,
                profileHash: String(repeating: "e", count: 64),
                sessionID: "session-test",
                profileValidUntil: Date(timeIntervalSince1970: 4_102_444_800),
                socksEndpoint: testSocksEndpoint(),
                routeMode: mode,
                destinationCIDRs: mode == .destinationSplit ? ["203.0.113.0/24"] : [],
                bypass: unprovenBypass,
                dns: DNSConfiguration(servers: ["1.1.1.1"])
            )
            XCTAssertThrowsError(try configuration.validated()) { error in
                XCTAssertEqual(error as? PacketTunnelConfigurationError, .bypassSourceEndpointsMissing)
            }
        }
    }

    func testFullTunnelIncludesIPv4AndIPv6DefaultRoutes() throws {
        let configuration = PacketTunnelConfiguration(
            remoteAddress: "198.18.0.1",
            daemonInstanceID: "daemon-test",
            profileRevision: 1,
            profileHash: String(repeating: "e", count: 64),
            sessionID: "session-test",
            profileValidUntil: Date(timeIntervalSince1970: 4_102_444_800),
            socksEndpoint: testSocksEndpoint(),
            routeMode: .fullTunnel,
            destinationCIDRs: [],
            bypass: strictBypass(),
            dns: DNSConfiguration(servers: ["1.1.1.1", "2606:4700:4700::1111"])
        )

        let plan = try configuration.validatedRoutePlan()

        XCTAssertEqual(plan.includedIPv4, [try IPRoute(cidr: "0.0.0.0/0")])
        XCTAssertEqual(plan.includedIPv6, [try IPRoute(cidr: "::/0")])
        XCTAssertEqual(plan.excluded, [
            try IPRoute(cidr: "198.51.100.7/32"),
            try IPRoute(cidr: "2001:db8:ffff::7/128")
        ])
    }

    func testFullTunnelRejectsEmptyBypassSet() {
        let configuration = PacketTunnelConfiguration(
            remoteAddress: "198.18.0.1",
            daemonInstanceID: "daemon-test",
            profileRevision: 1,
            profileHash: String(repeating: "e", count: 64),
            sessionID: "session-test",
            profileValidUntil: Date(timeIntervalSince1970: 4_102_444_800),
            socksEndpoint: testSocksEndpoint(),
            routeMode: .fullTunnel,
            destinationCIDRs: [],
            bypass: .empty,
            dns: DNSConfiguration(servers: ["1.1.1.1"])
        )

        XCTAssertThrowsError(try configuration.validatedRoutePlan()) { error in
            XCTAssertEqual(error as? PacketTunnelConfigurationError, .fullTunnelBypassRequired)
        }
    }

    func testBypassRequiresExactHostMappingAndHostRoutes() {
        let extraMapping = BypassSet(
            requiredHosts: ["control.example.test"],
            resolvedCIDRs: ["198.51.100.7/32", "203.0.113.8/32"],
            resolvedCIDRsByHost: [
                "control.example.test": ["198.51.100.7/32"],
                "unexpected.example.test": ["203.0.113.8/32"]
            ],
            sourceEndpoints: ["https://control.example.test/api"],
            resolutionComplete: true
        )
        XCTAssertThrowsError(try extraMapping.validatedRoutes()) { error in
            XCTAssertEqual(error as? PacketTunnelConfigurationError, .bypassHostMappingMismatch)
        }

        let networkRoute = BypassSet(
            requiredHosts: ["control.example.test"],
            resolvedCIDRs: ["198.51.100.0/24"],
            resolvedCIDRsByHost: ["control.example.test": ["198.51.100.0/24"]],
            sourceEndpoints: ["https://control.example.test/api"],
            resolutionComplete: true
        )
        XCTAssertThrowsError(try networkRoute.validatedRoutes()) { error in
            XCTAssertEqual(error as? PacketTunnelConfigurationError, .bypassRouteMustBeHost("198.51.100.0/24"))
        }
    }

    func testDestinationSplitPreservesBothFamiliesAndResolvedBypasses() throws {
        let bypass = BypassSet(
            requiredHosts: ["control.example.test"],
            resolvedCIDRs: ["198.51.100.7/32", "2001:db8:ffff::7/128"],
            resolvedCIDRsByHost: [
                "control.example.test": ["198.51.100.7/32", "2001:db8:ffff::7/128"]
            ],
            sourceEndpoints: ["https://control.example.test/api"],
            resolutionComplete: true
        )
        let configuration = PacketTunnelConfiguration(
            remoteAddress: "198.18.0.1",
            daemonInstanceID: "daemon-test",
            profileRevision: 1,
            profileHash: String(repeating: "e", count: 64),
            sessionID: "session-test",
            profileValidUntil: Date(timeIntervalSince1970: 4_102_444_800),
            socksEndpoint: testSocksEndpoint(),
            routeMode: .destinationSplit,
            destinationCIDRs: ["203.0.113.0/24", "2001:db8:1234::/48"],
            bypass: bypass,
            dns: DNSConfiguration(servers: ["9.9.9.9"])
        )

        let plan = try configuration.validatedRoutePlan()

        XCTAssertEqual(plan.includedIPv4, [try IPRoute(cidr: "203.0.113.0/24")])
        XCTAssertEqual(plan.includedIPv6, [try IPRoute(cidr: "2001:db8:1234::/48")])
        XCTAssertEqual(plan.excluded, [
            try IPRoute(cidr: "198.51.100.7/32"),
            try IPRoute(cidr: "2001:db8:ffff::7/128")
        ])
    }

    func testIncompleteBypassSetFailsClosed() {
        let bypass = BypassSet(
            requiredHosts: ["control.example.test"],
            resolvedCIDRs: [],
            resolutionComplete: false
        )
        let configuration = PacketTunnelConfiguration(
            remoteAddress: "198.18.0.1",
            daemonInstanceID: "daemon-test",
            profileRevision: 1,
            profileHash: String(repeating: "e", count: 64),
            sessionID: "session-test",
            profileValidUntil: Date(timeIntervalSince1970: 4_102_444_800),
            socksEndpoint: testSocksEndpoint(),
            routeMode: .fullTunnel,
            destinationCIDRs: [],
            bypass: bypass,
            dns: DNSConfiguration(servers: ["1.1.1.1"])
        )

        XCTAssertThrowsError(try configuration.validatedRoutePlan()) { error in
            XCTAssertEqual(error as? PacketTunnelConfigurationError, .bypassResolutionIncomplete)
        }
    }

    func testBypassAggregateMustMatchPerHostResolution() {
        let bypass = BypassSet(
            requiredHosts: ["control.example.test"],
            resolvedCIDRs: ["198.51.100.7/32"],
            resolvedCIDRsByHost: ["control.example.test": ["203.0.113.7/32"]],
            sourceEndpoints: ["https://control.example.test/api"],
            resolutionComplete: true
        )
        let configuration = PacketTunnelConfiguration(
            remoteAddress: "198.18.0.1",
            daemonInstanceID: "daemon-test",
            profileRevision: 1,
            profileHash: String(repeating: "e", count: 64),
            sessionID: "session-test",
            profileValidUntil: Date(timeIntervalSince1970: 4_102_444_800),
            socksEndpoint: testSocksEndpoint(),
            routeMode: .fullTunnel,
            destinationCIDRs: [],
            bypass: bypass,
            dns: DNSConfiguration(servers: ["1.1.1.1"])
        )

        XCTAssertThrowsError(try configuration.validatedRoutePlan()) { error in
            XCTAssertEqual(error as? PacketTunnelConfigurationError, .bypassRouteMappingMismatch)
        }
    }

    func testInvalidDNSAndMTUAreTypedErrors() {
        let configuration = PacketTunnelConfiguration(
            remoteAddress: "198.18.0.1",
            daemonInstanceID: "daemon-test",
            profileRevision: 1,
            profileHash: String(repeating: "e", count: 64),
            sessionID: "session-test",
            profileValidUntil: Date(timeIntervalSince1970: 4_102_444_800),
            socksEndpoint: testSocksEndpoint(),
            routeMode: .fullTunnel,
            destinationCIDRs: [],
            bypass: .empty,
            dns: DNSConfiguration(servers: ["not-an-ip"])
        )

        XCTAssertThrowsError(try configuration.validated()) { error in
            XCTAssertEqual(error as? PacketTunnelConfigurationError, .invalidDNSAddress("not-an-ip"))
        }

        let invalidMTU = PacketTunnelConfiguration(
            remoteAddress: "198.18.0.1",
            daemonInstanceID: "daemon-test",
            profileRevision: 1,
            profileHash: String(repeating: "e", count: 64),
            sessionID: "session-test",
            profileValidUntil: Date(timeIntervalSince1970: 4_102_444_800),
            socksEndpoint: testSocksEndpoint(),
            routeMode: .fullTunnel,
            destinationCIDRs: [],
            bypass: .empty,
            dns: DNSConfiguration(servers: ["1.1.1.1"]),
            mtu: 100
        )
        XCTAssertThrowsError(try invalidMTU.validated()) { error in
            XCTAssertEqual(error as? PacketTunnelConfigurationError, .invalidMTU(100))
        }

        let invalidMask = PacketTunnelConfiguration(
            remoteAddress: "198.18.0.1",
            daemonInstanceID: "daemon-test",
            profileRevision: 1,
            profileHash: String(repeating: "e", count: 64),
            sessionID: "session-test",
            profileValidUntil: Date(timeIntervalSince1970: 4_102_444_800),
            socksEndpoint: testSocksEndpoint(),
            routeMode: .fullTunnel,
            destinationCIDRs: [],
            bypass: .empty,
            dns: DNSConfiguration(servers: ["1.1.1.1"]),
            tunnelIPv4SubnetMask: "255.0.255.0"
        )
        XCTAssertThrowsError(try invalidMask.validated()) { error in
            XCTAssertEqual(error as? PacketTunnelConfigurationError, .invalidIPv4SubnetMask("255.0.255.0"))
        }
    }

    #if canImport(NetworkExtension)
    func testNetworkSettingsContainDualStackRoutesAndDNS() throws {
        let configuration = PacketTunnelConfiguration(
            remoteAddress: "198.18.0.1",
            daemonInstanceID: "daemon-test",
            profileRevision: 1,
            profileHash: String(repeating: "e", count: 64),
            sessionID: "session-test",
            profileValidUntil: Date(timeIntervalSince1970: 4_102_444_800),
            socksEndpoint: testSocksEndpoint(),
            routeMode: .fullTunnel,
            destinationCIDRs: [],
            bypass: BypassSet(
                requiredHosts: ["control.example.test"],
                resolvedCIDRs: ["198.51.100.7/32", "2001:db8:ffff::7/128"],
                resolvedCIDRsByHost: ["control.example.test": ["198.51.100.7/32", "2001:db8:ffff::7/128"]],
                sourceEndpoints: ["https://control.example.test/api"],
                resolutionComplete: true
            ),
            dns: DNSConfiguration(servers: ["1.1.1.1", "2606:4700:4700::1111"])
        )

        let settings = try configuration.makeNetworkSettings()

        XCTAssertEqual(settings.ipv4Settings?.includedRoutes?.count, 1)
        XCTAssertEqual(settings.ipv4Settings?.excludedRoutes?.count, 1)
        XCTAssertEqual(settings.ipv6Settings?.includedRoutes?.count, 1)
        XCTAssertEqual(settings.ipv6Settings?.excludedRoutes?.count, 1)
        XCTAssertEqual(settings.dnsSettings?.servers, ["1.1.1.1", "2606:4700:4700::1111"])
        XCTAssertEqual(settings.dnsSettings?.matchDomains, [""])
    }
    #endif
}

private func testSocksEndpoint() -> RuntimeLoopbackSocksEndpoint {
    try! RuntimeLoopbackSocksEndpoint(confirmedAddress: "127.0.0.1:1080")
}

private func strictBypass() -> BypassSet {
    BypassSet(
        requiredHosts: ["control.example.test"],
        resolvedCIDRs: ["198.51.100.7/32", "2001:db8:ffff::7/128"],
        resolvedCIDRsByHost: [
            "control.example.test": ["198.51.100.7/32", "2001:db8:ffff::7/128"]
        ],
        sourceEndpoints: ["https://control.example.test/api"],
        resolutionComplete: true
    )
}
