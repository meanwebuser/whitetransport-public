import Foundation
import XCTest
@testable import WhiteTransportMacOS

final class DarwinTun2SocksEngineTests: XCTestCase {
    func testRuntimeEndpointAcceptsOnlyCredentialFreeLoopback() throws {
        XCTAssertEqual(
            try RuntimeLoopbackSocksEndpoint(confirmedAddress: "127.0.0.1:1085"),
            RuntimeLoopbackSocksEndpoint(host: "127.0.0.1", port: 1085)
        )
        XCTAssertEqual(
            try RuntimeLoopbackSocksEndpoint(confirmedAddress: "[::1]:2085"),
            RuntimeLoopbackSocksEndpoint(host: "::1", port: 2085)
        )
        XCTAssertThrowsError(try RuntimeLoopbackSocksEndpoint(confirmedAddress: "192.0.2.10:1085"))
        XCTAssertThrowsError(try RuntimeLoopbackSocksEndpoint(confirmedAddress: "user:pass@127.0.0.1:1085"))
        XCTAssertThrowsError(try RuntimeLoopbackSocksEndpoint(confirmedAddress: "127.0.0.1:0"))
    }

    func testEnginePassesValidatedFDMTUAndRuntimePortThenStopsExactlyOnce() throws {
        let api = Tun2SocksAPISpy()
        let engine = DarwinTun2SocksEngine(
            mtu: 1_420,
            endpoint: try RuntimeLoopbackSocksEndpoint(confirmedAddress: "127.0.0.1:1085"),
            api: api
        )

        try engine.start(fileDescriptor: 41, offset: 0)
        engine.stop()
        engine.stop()

        XCTAssertEqual(api.startCalls, [.init(fileDescriptor: 41, mtu: 1_420, socksPort: 1085)])
        XCTAssertEqual(api.stopCount, 1)
    }

    func testEngineStartFailureDoesNotCallStopOrTakeDescriptorOwnership() throws {
        let api = Tun2SocksAPISpy(startError: DarwinEngineTestError.startFailed)
        let engine = DarwinTun2SocksEngine(
            mtu: 1_500,
            endpoint: try RuntimeLoopbackSocksEndpoint(confirmedAddress: "[::1]:1085"),
            api: api
        )

        XCTAssertThrowsError(try engine.start(fileDescriptor: 51, offset: 0))
        engine.stop()

        XCTAssertEqual(api.stopCount, 0)
    }

    func testEngineRejectsNonzeroPacketOffset() throws {
        let api = Tun2SocksAPISpy()
        let engine = DarwinTun2SocksEngine(
            mtu: 1_500,
            endpoint: try RuntimeLoopbackSocksEndpoint(confirmedAddress: "127.0.0.1:1085"),
            api: api
        )

        XCTAssertThrowsError(try engine.start(fileDescriptor: 61, offset: 4))
        XCTAssertTrue(api.startCalls.isEmpty)
    }
}

private enum DarwinEngineTestError: Error { case startFailed }

private final class Tun2SocksAPISpy: DarwinTun2SocksCalling, @unchecked Sendable {
    struct StartCall: Equatable {
        let fileDescriptor: Int32
        let mtu: Int32
        let socksPort: Int32
    }

    let startError: Error?
    private(set) var startCalls: [StartCall] = []
    private(set) var stopCount = 0

    init(startError: Error? = nil) { self.startError = startError }

    func start(fileDescriptor: Int32, mtu: Int32, socksPort: Int32) throws {
        startCalls.append(.init(fileDescriptor: fileDescriptor, mtu: mtu, socksPort: socksPort))
        if let startError { throw startError }
    }

    func stop() throws { stopCount += 1 }
}
