import Foundation
import XCTest
@testable import WhiteTransportMacOS

final class ConnectionContractTests: XCTestCase {
    func testWireEnumsAndFieldsUseExplicitSnakeCaseIncludingUnsupported() throws {
        XCTAssertEqual(ConnectionLifecycleState.permissionRequired.rawValue, "permission_required")
        XCTAssertEqual(ConnectionLifecycleState.disconnecting.rawValue, "disconnecting")
        XCTAssertEqual(ConnectionLifecycleState.unsupported.rawValue, "unsupported")
        XCTAssertEqual(SystemVPNState.unsupported.rawValue, "unsupported")
        XCTAssertEqual(PacketTunnelRouteMode.fullTunnel.rawValue, "full_tunnel")
        XCTAssertEqual(PacketTunnelRouteMode.destinationSplit.rawValue, "destination_split")

        let message = ProviderMessage(command: .status, requestID: "wire-1")
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: ProviderMessageCodec.encode(message)) as? [String: Any])
        XCTAssertEqual(object["request_id"] as? String, "wire-1")
        XCTAssertNil(object["requestID"])

        let status = ConnectionStatus(
            state: .unsupported,
            transport: .unsupported,
            systemVPN: .unsupported,
            providerState: .unsupported,
            profileIdentity: RuntimeProfileIdentity(
                daemonInstanceID: "daemon-wire",
                profileRevision: 7,
                profileHash: String(repeating: "a", count: 64),
                sessionID: "session-wire"
            ),
            profileValidUntil: Date(timeIntervalSince1970: 2_000_000_000),
            message: nil
        )
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        let statusData = try encoder.encode(status)
        let statusObject = try XCTUnwrap(JSONSerialization.jsonObject(with: statusData) as? [String: Any])
        XCTAssertEqual(statusObject["system_vpn"] as? String, "unsupported")
        XCTAssertEqual(statusObject["provider_state"] as? String, "unsupported")
        XCTAssertEqual(statusObject["daemon_instance_id"] as? String, "daemon-wire")
        XCTAssertEqual((statusObject["profile_revision"] as? NSNumber)?.uint64Value, 7)
        XCTAssertEqual(statusObject["profile_hash"] as? String, String(repeating: "a", count: 64))
        XCTAssertEqual(statusObject["session_id"] as? String, "session-wire")
        XCTAssertEqual(statusObject["profile_valid_until"] as? String, "2033-05-18T03:33:20Z")
        XCTAssertNil(statusObject["systemVPN"])
    }

    func testProviderConnectedRequiresExactExpectedProfileIdentity() throws {
        let expected = RuntimeProfileIdentity(
            daemonInstanceID: "daemon-current",
            profileRevision: 42,
            profileHash: String(repeating: "b", count: 64),
            sessionID: "session-current"
        )
        let validUntil = Date(timeIntervalSince1970: 2_000_000_000)
        let expectedLease = RuntimeProfileLease(identity: expected, validUntil: validUntil)
        let connected = ConnectionStatus(
            state: .connected,
            transport: .connected,
            systemVPN: .connected,
            providerState: .connected,
            profileIdentity: expected,
            profileValidUntil: validUntil
        )

        XCTAssertNoThrow(try RuntimeProviderStatusValidator.requireConnected(
            connected,
            expected: expectedLease,
            now: Date(timeIntervalSince1970: 1_900_000_000)
        ))
        XCTAssertThrowsError(try RuntimeProviderStatusValidator.requireConnected(
            ConnectionStatus(
                state: .connected,
                transport: .connected,
                systemVPN: .connected,
                providerState: .connected,
                profileIdentity: RuntimeProfileIdentity(
                    daemonInstanceID: "daemon-stale",
                    profileRevision: 42,
                    profileHash: String(repeating: "b", count: 64),
                    sessionID: "session-current"
                ),
                profileValidUntil: validUntil
            ),
            expected: expectedLease,
            now: Date(timeIntervalSince1970: 1_900_000_000)
        )) { error in
            XCTAssertEqual(error as? RuntimeProviderStatusValidationError, .profileMismatch)
        }
        XCTAssertThrowsError(try RuntimeProviderStatusValidator.requireConnected(
            ConnectionStatus(
                state: .connected,
                transport: .connected,
                systemVPN: .connected,
                providerState: .connecting,
                profileIdentity: expected,
                profileValidUntil: validUntil
            ),
            expected: expectedLease,
            now: Date(timeIntervalSince1970: 1_900_000_000)
        )) { error in
            XCTAssertEqual(error as? RuntimeProviderStatusValidationError, .providerNotConnected)
        }
        XCTAssertThrowsError(try RuntimeProviderStatusValidator.requireConnected(
            ConnectionStatus(
                state: .connected,
                transport: .connected,
                systemVPN: .connected,
                providerState: .connected
            ),
            expected: expectedLease,
            now: Date(timeIntervalSince1970: 1_900_000_000)
        )) { error in
            XCTAssertEqual(error as? RuntimeProviderStatusValidationError, .profileMissing)
        }
        XCTAssertThrowsError(try RuntimeProviderStatusValidator.requireConnected(
            connected,
            expected: RuntimeProfileLease(identity: expected, validUntil: validUntil.addingTimeInterval(60)),
            now: Date(timeIntervalSince1970: 1_900_000_000)
        )) { error in
            XCTAssertEqual(error as? RuntimeProviderStatusValidationError, .profileMismatch)
        }
        XCTAssertThrowsError(try RuntimeProviderStatusValidator.requireConnected(
            connected,
            expected: expectedLease,
            now: validUntil
        )) { error in
            XCTAssertEqual(error as? RuntimeProviderStatusValidationError, .profileExpired)
        }
    }

    func testConnectedRequiresTransportAndSystemVPN() {
        XCTAssertEqual(
            ConnectionStateReducer.reduce(transport: .connected, systemVPN: .connected),
            .connected
        )
        XCTAssertNotEqual(
            ConnectionStateReducer.reduce(transport: .connected, systemVPN: .disconnected),
            .connected
        )
        XCTAssertEqual(
            ConnectionStateReducer.reduce(transport: .connecting, systemVPN: .connected),
            .connecting
        )
    }

    func testProviderStatusAndStopMessagesRoundTrip() throws {
        let status = ProviderMessage(command: .status, requestID: "status-1")
        let statusData = try ProviderMessageCodec.encode(status)
        XCTAssertEqual(try ProviderMessageCodec.decode(statusData), status)

        let stop = ProviderMessage(command: .stop, requestID: "stop-1")
        XCTAssertEqual(try ProviderMessageCodec.decode(ProviderMessageCodec.encode(stop)), stop)
    }
}
