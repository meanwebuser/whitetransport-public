import Foundation
import os

/// A deliberately narrow XCTest launch contract for the iOS application.
///
/// Production launches cannot opt in accidentally: both the XCTest environment
/// marker and the `-wt-ui-test-connect` argument are required. The native iOS
/// client connects to a room URL, not a WhiteTransport node ID, so this contract
/// intentionally does not pretend to select a node or prove an egress payload.
enum LaunchTestContract {
    static let testEnvironmentKey = "WT_UI_TEST_MODE"
    static let connectArgument = "-wt-ui-test-connect"
    static let callURLArgument = "-wt-ui-test-call-url"
    static let resultFileName = "wt-ui-launch-result.json"

    struct Request: Equatable {
        let callURL: String
    }

    struct Result: Encodable {
        let schemaVersion: Int
        let event: String
        let timestamp: Date
        let status: String
        let isRunning: Bool
        let hasCallURL: Bool
        let errorMessage: String
    }

    /// Returns a request only for an explicit XCTest-only auto-connect launch.
    static func parse(arguments: [String], environment: [String: String]) -> Request? {
        guard environment[testEnvironmentKey] == "1",
              let connectIndex = arguments.firstIndex(of: connectArgument),
              connectIndex >= 0,
              let callURLIndex = arguments.firstIndex(of: callURLArgument),
              callURLIndex + 1 < arguments.count else {
            return nil
        }

        let callURL = arguments[callURLIndex + 1].trimmingCharacters(in: .whitespacesAndNewlines)
        guard let url = URL(string: callURL), url.scheme != nil, !callURL.isEmpty else {
            return nil
        }
        return Request(callURL: callURL)
    }

    /// Starts a room-link connection and records observable native state for XCTest.
    @MainActor
    static func runIfRequested(proxyManager: ProxyManager) async {
        guard let request = parse(
            arguments: ProcessInfo.processInfo.arguments,
            environment: ProcessInfo.processInfo.environment
        ) else {
            return
        }

        writeResult(event: "connect_requested", proxyManager: proxyManager, hasCallURL: !request.callURL.isEmpty)
        proxyManager.callUrl = request.callURL
        proxyManager.connect()

        // A launch test observes the app's real native state after the request.
        // It intentionally does not claim tunnel readiness or payload success;
        // those require a separately provisioned room and provider test.
        try? await Task.sleep(for: .seconds(3))
        writeResult(event: "connect_observed", proxyManager: proxyManager, hasCallURL: true)
    }

    @MainActor
    private static func writeResult(event: String, proxyManager: ProxyManager, hasCallURL: Bool) {
        let result = Result(
            schemaVersion: 1,
            event: event,
            timestamp: Date(),
            status: proxyManager.status.rawValue,
            isRunning: proxyManager.isRunning,
            hasCallURL: hasCallURL,
            errorMessage: proxyManager.errorMessage
        )
        guard let data = try? JSONEncoder.launchTestEncoder.encode(result),
              let url = resultURL() else {
            Logger(subsystem: "bypass.whitelist", category: "ui-launch-test")
                .error("could not encode XCTest launch result")
            return
        }

        do {
            try data.write(to: url, options: .atomic)
            Logger(subsystem: "bypass.whitelist", category: "ui-launch-test")
                .info("XCTest launch result event=\(event, privacy: .public) status=\(result.status, privacy: .public)")
        } catch {
            Logger(subsystem: "bypass.whitelist", category: "ui-launch-test")
                .error("could not write XCTest launch result: \(error.localizedDescription, privacy: .public)")
        }
    }

    private static func resultURL() -> URL? {
        let manager = FileManager.default
        guard let applicationSupport = try? manager.url(
            for: .applicationSupportDirectory,
            in: .userDomainMask,
            appropriateFor: nil,
            create: true
        ) else {
            return nil
        }
        return applicationSupport.appendingPathComponent(resultFileName, isDirectory: false)
    }
}

private extension JSONEncoder {
    static var launchTestEncoder: JSONEncoder {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        encoder.outputFormatting = [.sortedKeys]
        return encoder
    }
}
