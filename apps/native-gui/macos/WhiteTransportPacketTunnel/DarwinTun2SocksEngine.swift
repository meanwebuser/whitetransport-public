import Foundation

public enum RuntimeLoopbackSocksEndpointError: Error, Equatable, LocalizedError, Sendable {
    case malformedAddress(String)
    case credentialsNotAllowed
    case nonLoopbackHost(String)
    case invalidPort(Int?)

    public var errorDescription: String? {
        switch self {
        case .malformedAddress(let value): return "invalid confirmed SOCKS listen address: \(value)"
        case .credentialsNotAllowed: return "confirmed SOCKS listen address must not contain credentials"
        case .nonLoopbackHost(let value): return "confirmed SOCKS listen host is not loopback: \(value)"
        case .invalidPort(let value): return "confirmed SOCKS listen port is invalid: \(value.map(String.init) ?? "missing")"
        }
    }
}

/// A credential-free SOCKS endpoint copied from the daemon's confirmed runtime status.
public struct RuntimeLoopbackSocksEndpoint: Codable, Equatable, Sendable {
    public let host: String
    public let port: Int

    init(host: String, port: Int) {
        precondition(host == "127.0.0.1" || host == "::1")
        precondition((1...65_535).contains(port))
        self.host = host
        self.port = port
    }

    public init(confirmedAddress: String) throws {
        let value = confirmedAddress.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !value.isEmpty,
              let components = URLComponents(string: "socks5://\(value)"),
              components.scheme == "socks5",
              components.path.isEmpty,
              components.query == nil,
              components.fragment == nil,
              let parsedHost = components.host else {
            throw RuntimeLoopbackSocksEndpointError.malformedAddress(confirmedAddress)
        }
        guard components.user == nil, components.password == nil else {
            throw RuntimeLoopbackSocksEndpointError.credentialsNotAllowed
        }
        let host = parsedHost.hasPrefix("[") && parsedHost.hasSuffix("]") ? String(parsedHost.dropFirst().dropLast()) : parsedHost
        guard host == "127.0.0.1" || host == "::1" else {
            throw RuntimeLoopbackSocksEndpointError.nonLoopbackHost(host)
        }
        guard let port = components.port, (1...65_535).contains(port) else {
            throw RuntimeLoopbackSocksEndpointError.invalidPort(components.port)
        }
        self.host = host
        self.port = port
    }
}

/// Narrow C-ABI surface implemented only by the packet-tunnel executable target.
public protocol DarwinTun2SocksCalling: AnyObject, Sendable {
    func start(fileDescriptor: Int32, mtu: Int32, socksPort: Int32) throws
    func stop() throws
}

public enum DarwinTun2SocksEngineError: Error, Equatable, LocalizedError, Sendable {
    case invalidFileDescriptor(Int32)
    case unsupportedPacketOffset(Int32)
    case invalidMTU(Int)

    public var errorDescription: String? {
        switch self {
        case .invalidFileDescriptor(let value): return "invalid packet engine file descriptor: \(value)"
        case .unsupportedPacketOffset(let value): return "packet engine does not support a nonzero offset: \(value)"
        case .invalidMTU(let value): return "invalid packet engine MTU: \(value)"
        }
    }
}

/// Extension-local adapter that transfers the bridge descriptor to the Go engine after a successful start.
public final class DarwinTun2SocksEngine: PacketFlowBridgeEngine, @unchecked Sendable {
    public var lastStopError: Error? { lock.withLock { stopError } }

    private let mtu: Int
    private let endpoint: RuntimeLoopbackSocksEndpoint
    private let api: DarwinTun2SocksCalling
    private let lock = NSLock()
    private var ownsDescriptor = false
    private var stopError: Error?

    public init(mtu: Int, endpoint: RuntimeLoopbackSocksEndpoint, api: DarwinTun2SocksCalling) {
        self.mtu = mtu
        self.endpoint = endpoint
        self.api = api
    }

    public func start(fileDescriptor: Int32, offset: Int32) throws {
        guard fileDescriptor >= 0 else { throw DarwinTun2SocksEngineError.invalidFileDescriptor(fileDescriptor) }
        guard offset == 0 else { throw DarwinTun2SocksEngineError.unsupportedPacketOffset(offset) }
        guard (576...65_535).contains(mtu) else { throw DarwinTun2SocksEngineError.invalidMTU(mtu) }

        try lock.withLock {
            guard !ownsDescriptor else { throw PacketFlowBridgeError.alreadyStarted }
            try api.start(fileDescriptor: fileDescriptor, mtu: Int32(mtu), socksPort: Int32(endpoint.port))
            ownsDescriptor = true
            stopError = nil
        }
    }

    public func stop() {
        lock.withLock {
            guard ownsDescriptor else { return }
            ownsDescriptor = false
            do { try api.stop() }
            catch { stopError = error }
        }
    }
}

private extension NSLock {
    func withLock<T>(_ body: () throws -> T) rethrows -> T {
        lock()
        defer { unlock() }
        return try body()
    }
}
