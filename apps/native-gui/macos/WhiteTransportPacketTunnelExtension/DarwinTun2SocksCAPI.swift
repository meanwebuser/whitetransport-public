import Foundation
import WhiteTransportMacOS
import WhiteTransportTun2SocksC

private struct DarwinTun2SocksCAPIError: Error, LocalizedError {
    let operation: String
    let detail: String

    var errorDescription: String? { "tun2socks \(operation) failed: \(detail)" }
}

/// Extension-only implementation backed by the statically linked Go C archive.
final class DarwinTun2SocksCAPI: DarwinTun2SocksCalling, @unchecked Sendable {
    func start(fileDescriptor: Int32, mtu: Int32, socksPort: Int32) throws {
        guard WTStartTun2Socks(fileDescriptor, mtu, socksPort) == 0 else {
            throw lastError(operation: "start")
        }
    }

    func stop() throws {
        guard WTStopTun2Socks() == 0 else { throw lastError(operation: "stop") }
    }

    private func lastError(operation: String) -> Error {
        guard let pointer = WTLastError() else {
            return DarwinTun2SocksCAPIError(operation: operation, detail: "unknown engine error")
        }
        defer { WTFreeCString(pointer) }
        let detail = String(cString: pointer)
        return DarwinTun2SocksCAPIError(operation: operation, detail: detail.isEmpty ? "unknown engine error" : detail)
    }
}

final class DarwinPacketFlowEngineBuilder: PacketFlowEngineBuilding {
    private let api: DarwinTun2SocksCalling

    init(api: DarwinTun2SocksCalling = DarwinTun2SocksCAPI()) { self.api = api }

    func makeEngine(configuration: PacketTunnelConfiguration) throws -> PacketFlowBridgeEngine {
        let validated = try configuration.validated()
        return DarwinTun2SocksEngine(mtu: validated.mtu, endpoint: validated.socksEndpoint, api: api)
    }
}
