import Foundation
import WTCore

/// Starts the canonical Go client runtime with the session that the local
/// allowlisted WebView just captured. The session does not cross the
/// Capacitor boundary and is never persisted in a runtime config. The caller
/// keeps the local session in Keychain using device-only accessibility.
enum WTCoreRoomFirstRuntime {
    private struct LocalSession: Encodable {
        let platform = "wbstream"
        let accessToken: String
        let cookieHeader: String

        enum CodingKeys: String, CodingKey {
            case platform
            case accessToken = "access_token"
            case cookieHeader = "cookie_header"
        }
    }

    enum StartError: LocalizedError {
        case missingRuntimeConfiguration
        case invalidRuntimeConfiguration
        case failedToStart

        var errorDescription: String? {
            switch self {
            case .missingRuntimeConfiguration:
                return "WhiteTransport runtime is not provisioned on this device."
            case .invalidRuntimeConfiguration:
                return "WhiteTransport runtime configuration could not be read."
            case .failedToStart:
                return "WhiteTransport runtime could not start."
            }
        }
    }

    static func startIfProvisioned(session: WBStreamRoomSession) throws {
        guard let configURL = Bundle.main.url(forResource: "wt-runtime-config", withExtension: "json") else {
            throw StartError.missingRuntimeConfiguration
        }
        guard let configJSON = try? String(contentsOf: configURL, encoding: .utf8) else {
            throw StartError.invalidRuntimeConfiguration
        }

        let localSession = LocalSession(accessToken: session.accessToken, cookieHeader: session.cookieHeader)
        let encoded = try JSONEncoder().encode(localSession)
        guard let localSessionJSON = String(data: encoded, encoding: .utf8) else {
            throw StartError.invalidRuntimeConfiguration
        }

        var startError: NSError?
        guard MobileStartTransportWithLocalSession(configJSON, localSessionJSON, &startError) else {
            throw startError ?? StartError.failedToStart
        }
    }
}
