import Foundation

// Copy to WtBusSecrets.swift or generate it during local build/deploy.
// Do not commit real keys, bot tokens, peer IDs, or telemetry IDs.
enum WtBusSecrets {
    static let keyB64 = ""
    static let keyID = "k1"
    static let vkBotToken = ""
    static let vkBotPeerID = ""
    static let vkTelemetryPeerID = ""
    static let vkBusPeerIDs = [vkBotPeerID].filter { !$0.isEmpty }
    static let vkLogPeerIDs = [vkTelemetryPeerID].filter { !$0.isEmpty }
}
