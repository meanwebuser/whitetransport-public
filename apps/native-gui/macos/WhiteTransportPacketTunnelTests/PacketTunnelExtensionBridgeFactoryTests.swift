import Foundation
import XCTest
@testable import WhiteTransportMacOS

final class PacketTunnelExtensionBridgeFactoryTests: XCTestCase {
    func testProcessLocalEngineBuilderReceivesValidatedRuntimeConfiguration() throws {
        let engine = ExtensionFactoryTestEngine()
        let builder = ExtensionFactoryTestEngineBuilder(engine: engine)
        let factory = PacketTunnelExtensionBridgeFactory(flow: ExtensionFactoryTestFlow(), engineBuilder: builder)
        let configuration = extensionFactoryConfiguration()

        let bridge = try factory.makeBridge(configuration: configuration, failureHandler: { _ in })
        try bridge.start()
        bridge.stop()

        XCTAssertEqual(builder.makeCount, 1)
        XCTAssertEqual(builder.configuration, configuration)
        XCTAssertEqual(engine.startCount, 1)
        XCTAssertEqual(engine.stopCount, 1)
    }
}

private final class ExtensionFactoryTestFlow: PacketFlowBridgePacketFlow, @unchecked Sendable {
    private var completion: (@Sendable ([Data], [NSNumber]) -> Void)?

    func readPackets(completionHandler: @Sendable @escaping ([Data], [NSNumber]) -> Void) {
        completion = completionHandler
    }

    func writePackets(_ packets: [Data], withProtocols protocols: [NSNumber]) -> Bool { true }
}

private final class ExtensionFactoryTestEngine: PacketFlowBridgeEngine, @unchecked Sendable {
    private(set) var startCount = 0
    private(set) var stopCount = 0

    func start(fileDescriptor: Int32, offset: Int32) throws {
        startCount += 1
        close(fileDescriptor)
    }

    func stop() { stopCount += 1 }
}

private final class ExtensionFactoryTestEngineBuilder: PacketFlowEngineBuilding, @unchecked Sendable {
    let engine: ExtensionFactoryTestEngine
    private(set) var makeCount = 0
    private(set) var configuration: PacketTunnelConfiguration?

    init(engine: ExtensionFactoryTestEngine) { self.engine = engine }

    func makeEngine(configuration: PacketTunnelConfiguration) throws -> PacketFlowBridgeEngine {
        makeCount += 1
        self.configuration = configuration
        return engine
    }
}

private func extensionFactoryConfiguration() -> PacketTunnelConfiguration {
    PacketTunnelConfiguration(
        remoteAddress: "198.18.0.1",
        daemonInstanceID: "daemon-extension",
        profileRevision: 5,
        profileHash: String(repeating: "1", count: 64),
        sessionID: "session-extension",
        profileValidUntil: Date(timeIntervalSince1970: 4_102_444_800),
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
