import Darwin
import Foundation
import Security

private let serviceName = "com.meanwebuser.whitetransport.net-helper"
private let probeHelperVersion = "smappservice-probe.v1"
private let expectedBundleIdentifier = "com.meanwebuser.whitetransport"

@objc(WTMacAuthorizationProbeRequest)
final class WTMacAuthorizationProbeRequest: NSObject, NSSecureCoding {
    static var supportsSecureCoding: Bool { true }
    let operation: String
    let requestID: String
    let nonce: String

    required init?(coder: NSCoder) {
        guard let operation = coder.decodeObject(of: NSString.self, forKey: "operation") as String?,
              let requestID = coder.decodeObject(of: NSString.self, forKey: "request_id") as String?,
              let nonce = coder.decodeObject(of: NSString.self, forKey: "nonce") as String? else { return nil }
        self.operation = operation
        self.requestID = requestID
        self.nonce = nonce
    }

    func encode(with coder: NSCoder) {
        coder.encode(operation, forKey: "operation")
        coder.encode(requestID, forKey: "request_id")
        coder.encode(nonce, forKey: "nonce")
    }
}

@objc(WTMacAuthorizationProbeResponse)
final class WTMacAuthorizationProbeResponse: NSObject, NSSecureCoding {
    static var supportsSecureCoding: Bool { true }
    let success: Bool
    let operation: String
    let helperVersion: String
    let error: String?

    init(success: Bool, operation: String, helperVersion: String = probeHelperVersion, error: String? = nil) {
        self.success = success
        self.operation = operation
        self.helperVersion = helperVersion
        self.error = error
    }

    required init?(coder: NSCoder) {
        guard let operation = coder.decodeObject(of: NSString.self, forKey: "operation") as String?,
              let helperVersion = coder.decodeObject(of: NSString.self, forKey: "helper_version") as String? else { return nil }
        self.success = coder.decodeBool(forKey: "success")
        self.operation = operation
        self.helperVersion = helperVersion
        self.error = coder.decodeObject(of: NSString.self, forKey: "error") as String?
    }

    func encode(with coder: NSCoder) {
        coder.encode(success, forKey: "success")
        coder.encode(operation, forKey: "operation")
        coder.encode(helperVersion, forKey: "helper_version")
        coder.encode(error, forKey: "error")
    }
}

@objc protocol WTMacAuthorizationProbeProtocol {
    func authorizeMutation(
        _ request: WTMacAuthorizationProbeRequest,
        withReply reply: @escaping (WTMacAuthorizationProbeResponse) -> Void
    )
}

private final class ProbeRequestHandler: NSObject, WTMacAuthorizationProbeProtocol {
    private let connection: NSXPCConnection

    init(connection: NSXPCConnection) {
        self.connection = connection
    }

    func authorizeMutation(
        _ request: WTMacAuthorizationProbeRequest,
        withReply reply: @escaping (WTMacAuthorizationProbeResponse) -> Void
    ) {
        guard request.operation == "health" else {
            reply(WTMacAuthorizationProbeResponse(success: false, operation: request.operation, error: "unsupported probe operation"))
            return
        }
        guard request.requestID.count <= 128,
              request.requestID.range(of: "^[a-f0-9-]+$", options: .regularExpression) != nil,
              request.nonce.count == 64,
              request.nonce.range(of: "^[a-f0-9]+$", options: .regularExpression) != nil else {
            reply(WTMacAuthorizationProbeResponse(success: false, operation: request.operation, error: "malformed typed probe request"))
            return
        }
        guard authenticatedClient() else {
            reply(WTMacAuthorizationProbeResponse(success: false, operation: request.operation, error: "XPC caller code identity is not the installed WhiteTransport app"))
            return
        }
        // This is intentionally a no-op. It proves the root launchd/XPC and
        // code-signing boundary without accepting route, path, or process data.
        reply(WTMacAuthorizationProbeResponse(success: true, operation: request.operation))
    }

    private func authenticatedClient() -> Bool {
        // NSXPCConnection exposes the peer PID (not an auditToken property in
        // Swift). Resolve that PID to a signed SecCode and enforce the full
        // designated requirement below before accepting any request.
        let pid = connection.processIdentifier
        guard pid > 0 else { return false }
        var guestCode: SecCode?
        let attributes: [CFString: Any] = [kSecGuestAttributePid: NSNumber(value: pid)]
        guard SecCodeCopyGuestWithAttributes(nil, attributes as CFDictionary, [], &guestCode) == errSecSuccess,
              let guestCode else { return false }

        var staticCode: SecStaticCode?
        guard SecCodeCopyStaticCode(guestCode, [], &staticCode) == errSecSuccess,
              let staticCode,
              let callerTeam = signingTeamIdentifier(staticCode),
              let helperTeam = helperTeamIdentifier() else { return false }

        // A bundle identifier by itself is not an authentication boundary.
        // Require Apple's generic validation anchor plus the TeamIdentifier
        // (certificate OU) and then inspect the signed identifier as well.
        let designatedRequirement = "anchor apple generic and identifier \"\(expectedBundleIdentifier)\" and certificate leaf[subject.OU] = \"\(helperTeam)\""
        var requirement: SecRequirement?
        guard SecRequirementCreateWithString(designatedRequirement as CFString, [], &requirement) == errSecSuccess,
              let requirement,
              SecStaticCodeCheckValidity(staticCode, [], requirement) == errSecSuccess,
              let callerIdentifier = signingIdentifier(staticCode),
              callerIdentifier == expectedBundleIdentifier,
              callerTeam == helperTeam else { return false }
        return true
    }

    private func signingInformation(_ code: SecStaticCode) -> [String: Any]? {
        var information: CFDictionary?
        guard SecCodeCopySigningInformation(code, SecCSFlags(), &information) == errSecSuccess else { return nil }
        return information as? [String: Any]
    }

    private func signingIdentifier(_ code: SecStaticCode) -> String? {
        signingInformation(code)?[kSecCodeInfoIdentifier as String] as? String
    }

    private func signingTeamIdentifier(_ code: SecStaticCode) -> String? {
        signingInformation(code)?[kSecCodeInfoTeamIdentifier as String] as? String
    }

    private func helperTeamIdentifier() -> String? {
        var selfCode: SecCode?
        guard SecCodeCopySelf([], &selfCode) == errSecSuccess, let selfCode else { return nil }
        var staticCode: SecStaticCode?
        guard SecCodeCopyStaticCode(selfCode, [], &staticCode) == errSecSuccess, let staticCode else { return nil }
        return signingTeamIdentifier(staticCode)
    }
}

final class ProbeListenerDelegate: NSObject, NSXPCListenerDelegate {
    func listener(_ listener: NSXPCListener, shouldAcceptNewConnection newConnection: NSXPCConnection) -> Bool {
        let interface = NSXPCInterface(with: WTMacAuthorizationProbeProtocol.self)
        let selector = #selector(WTMacAuthorizationProbeProtocol.authorizeMutation(_:withReply:))
        interface.setClasses(NSSet().adding(WTMacAuthorizationProbeRequest.self), for: selector, argumentIndex: 0, ofReply: false)
        interface.setClasses(NSSet().adding(WTMacAuthorizationProbeResponse.self), for: selector, argumentIndex: 0, ofReply: true)
        newConnection.exportedInterface = interface
        newConnection.exportedObject = ProbeRequestHandler(connection: newConnection)
        newConnection.invalidationHandler = { }
        newConnection.interruptionHandler = { }
        newConnection.resume()
        return true
    }
}

let listener = NSXPCListener(machServiceName: serviceName)
let delegate = ProbeListenerDelegate()
listener.delegate = delegate
listener.resume()
RunLoop.main.run()
