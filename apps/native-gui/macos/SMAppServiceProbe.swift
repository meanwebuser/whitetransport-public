import Darwin
import Foundation
import Security
import ServiceManagement

/// The first privileged-helper milestone deliberately carries only a health
/// request. No route, utun, TokenStore, or executable path crosses this API.
@objc(WTMacAuthorizationProbeRequest)
public final class WTMacAuthorizationProbeRequest: NSObject, NSSecureCoding {
    public static var supportsSecureCoding: Bool { true }

    public let operation: String
    public let requestID: String
    public let nonce: String

    public init(operation: String, requestID: String, nonce: String) {
        self.operation = operation
        self.requestID = requestID
        self.nonce = nonce
    }

    public required init?(coder: NSCoder) {
        guard let operation = coder.decodeObject(of: NSString.self, forKey: "operation") as String?,
              let requestID = coder.decodeObject(of: NSString.self, forKey: "request_id") as String?,
              let nonce = coder.decodeObject(of: NSString.self, forKey: "nonce") as String? else {
            return nil
        }
        self.operation = operation
        self.requestID = requestID
        self.nonce = nonce
    }

    public func encode(with coder: NSCoder) {
        coder.encode(operation, forKey: "operation")
        coder.encode(requestID, forKey: "request_id")
        coder.encode(nonce, forKey: "nonce")
    }
}

@objc(WTMacAuthorizationProbeResponse)
public final class WTMacAuthorizationProbeResponse: NSObject, NSSecureCoding {
    public static var supportsSecureCoding: Bool { true }

    public let success: Bool
    public let operation: String
    public let helperVersion: String
    public let error: String?

    public init(success: Bool, operation: String, helperVersion: String, error: String? = nil) {
        self.success = success
        self.operation = operation
        self.helperVersion = helperVersion
        self.error = error
    }

    public required init?(coder: NSCoder) {
        guard let operation = coder.decodeObject(of: NSString.self, forKey: "operation") as String?,
              let helperVersion = coder.decodeObject(of: NSString.self, forKey: "helper_version") as String? else {
            return nil
        }
        self.success = coder.decodeBool(forKey: "success")
        self.operation = operation
        self.helperVersion = helperVersion
        self.error = coder.decodeObject(of: NSString.self, forKey: "error") as String?
    }

    public func encode(with coder: NSCoder) {
        coder.encode(success, forKey: "success")
        coder.encode(operation, forKey: "operation")
        coder.encode(helperVersion, forKey: "helper_version")
        coder.encode(error, forKey: "error")
    }
}

@objc public protocol WTMacAuthorizationProbeProtocol {
    func authorizeMutation(
        _ request: WTMacAuthorizationProbeRequest,
        withReply reply: @escaping (WTMacAuthorizationProbeResponse) -> Void
    )
}

public struct WTMacAuthorizationProbeResult: Codable, Equatable {
    public let supported: Bool
    public let registered: Bool
    public let authorized: Bool
    public let operation: String
    public let helperVersion: String
    public let error: String?

    public init(
        supported: Bool,
        registered: Bool,
        authorized: Bool,
        operation: String = "health",
        helperVersion: String = "",
        error: String? = nil
    ) {
        self.supported = supported
        self.registered = registered
        self.authorized = authorized
        self.operation = operation
        self.helperVersion = helperVersion
        self.error = error
    }
}

private enum WTMacAuthorizationProbeError: Error, LocalizedError {
    case unsupported
    case registration(String)
    case connection(String)
    case timeout

    var errorDescription: String? {
        switch self {
        case .unsupported:
            return "macOS 13 or newer is required for SMAppService"
        case let .registration(message):
            return "privileged helper registration failed: \(message)"
        case let .connection(message):
            return "privileged helper XPC connection failed: \(message)"
        case .timeout:
            return "privileged helper XPC health request timed out"
        }
    }
}

/// Registers the app-bundled daemon and performs one authenticated typed
/// health request. It is intentionally a probe and never mutates routes.
public enum WTMacAuthorizationProbe {
    public static let serviceName = "com.meanwebuser.whitetransport.net-helper"
    private static let plistName = "com.meanwebuser.whitetransport.net-helper.plist"

    public static func run(timeout: TimeInterval = 5) -> WTMacAuthorizationProbeResult {
        guard #available(macOS 13, *) else {
            return WTMacAuthorizationProbeResult(supported: false, registered: false, authorized: false, error: WTMacAuthorizationProbeError.unsupported.localizedDescription)
        }
        do {
            let service = SMAppService.daemon(plistName: plistName)
            do {
                try service.register()
            } catch {
                // register() is idempotent for an already enabled daemon.
                guard service.status == .enabled else {
                    throw WTMacAuthorizationProbeError.registration(error.localizedDescription)
                }
            }
            let response = try requestHealth(timeout: timeout)
            return WTMacAuthorizationProbeResult(
                supported: true,
                registered: true,
                authorized: response.success,
                operation: response.operation,
                helperVersion: response.helperVersion,
                error: response.error
            )
        } catch {
            return WTMacAuthorizationProbeResult(
                supported: true,
                registered: false,
                authorized: false,
                error: error.localizedDescription
            )
        }
    }

    @available(macOS 13, *)
    private static func requestHealth(timeout: TimeInterval) throws -> WTMacAuthorizationProbeResponse {
        let connection = NSXPCConnection(machServiceName: serviceName, options: .privileged)
        let interface = NSXPCInterface(with: WTMacAuthorizationProbeProtocol.self)
        let selector = #selector(WTMacAuthorizationProbeProtocol.authorizeMutation(_:withReply:))
        interface.setClasses(NSSet().adding(WTMacAuthorizationProbeRequest.self), for: selector, argumentIndex: 0, ofReply: false)
        interface.setClasses(NSSet().adding(WTMacAuthorizationProbeResponse.self), for: selector, argumentIndex: 0, ofReply: true)
        connection.remoteObjectInterface = interface
        let semaphore = DispatchSemaphore(value: 0)
        let result = WTProbeResponseBox()
        let failure = WTProbeErrorBox()
        let proxy = connection.remoteObjectProxyWithErrorHandler { error in
            failure.set(error)
            semaphore.signal()
        } as! WTMacAuthorizationProbeProtocol
        connection.resume()
        let request = WTMacAuthorizationProbeRequest(
            operation: "health",
            requestID: UUID().uuidString.lowercased(),
            nonce: UUID().uuidString.replacingOccurrences(of: "-", with: "") + UUID().uuidString.replacingOccurrences(of: "-", with: "")
        )
        proxy.authorizeMutation(request) { value in
            result.set(value)
            semaphore.signal()
        }
        guard semaphore.wait(timeout: .now() + timeout) == .success else {
            connection.invalidate()
            throw WTMacAuthorizationProbeError.timeout
        }
        connection.invalidate()
        if let connectionError = failure.get() { throw WTMacAuthorizationProbeError.connection(connectionError.localizedDescription) }
        guard let response = result.get() else { throw WTMacAuthorizationProbeError.connection("helper returned no response") }
        return response
    }
}

private final class WTProbeResponseBox: @unchecked Sendable {
    private let lock = NSLock()
    private var value: WTMacAuthorizationProbeResponse?

    func set(_ value: WTMacAuthorizationProbeResponse) {
        lock.lock()
        self.value = value
        lock.unlock()
    }

    func get() -> WTMacAuthorizationProbeResponse? {
        lock.lock()
        defer { lock.unlock() }
        return value
    }
}

private final class WTProbeErrorBox: @unchecked Sendable {
    private let lock = NSLock()
    private var value: Error?

    func set(_ value: Error) {
        lock.lock()
        self.value = value
        lock.unlock()
    }

    func get() -> Error? {
        lock.lock()
        defer { lock.unlock() }
        return value
    }
}

@_cdecl("WTMacAuthorizationProbeJSON")
public func WTMacAuthorizationProbeJSON() -> UnsafeMutablePointer<CChar>? {
    let result = WTMacAuthorizationProbe.run()
    guard let data = try? JSONEncoder().encode(result), let string = String(data: data, encoding: .utf8) else {
        return strdup("{\"supported\":true,\"registered\":false,\"authorized\":false,\"error\":\"probe encoding failed\"}")
    }
    return strdup(string)
}

@_cdecl("WTMacAuthorizationProbeFreeJSON")
public func WTMacAuthorizationProbeFreeJSON(_ pointer: UnsafeMutablePointer<CChar>?) {
    guard let pointer else { return }
    free(pointer)
}
