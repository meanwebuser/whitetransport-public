import Foundation

/// Product-level lifecycle state shared by the Wails host and the packet-tunnel extension.
public enum ConnectionLifecycleState: String, Codable, Equatable, Sendable {
    case disconnected
    case permissionRequired = "permission_required"
    case connecting
    case connected
    case degraded
    case disconnecting
    case unsupported
    case error
}

/// State reported by the transport/session layer.
public enum TransportState: String, Codable, Equatable, Sendable {
    case disconnected
    case connecting
    case connected
    case degraded
    case disconnecting
    case unsupported
    case error
}

/// State reported by the operating-system VPN manager.
public enum SystemVPNState: String, Codable, Equatable, Sendable {
    case disconnected
    case permissionRequired = "permission_required"
    case connecting
    case connected
    case degraded
    case disconnecting
    case unsupported
    case error
}

public enum RuntimeProfileIdentityError: Error, Equatable, LocalizedError, Sendable {
    case invalidDaemonInstanceID
    case invalidProfileRevision
    case invalidProfileHash
    case invalidSessionID

    public var errorDescription: String? {
        switch self {
        case .invalidDaemonInstanceID: return "runtime profile daemon instance ID is missing"
        case .invalidProfileRevision: return "runtime profile revision must be positive"
        case .invalidProfileHash: return "runtime profile hash must be a 64-character hexadecimal SHA-256"
        case .invalidSessionID: return "runtime profile session ID is missing"
        }
    }
}

/// Immutable identity binding a daemon-confirmed profile to one provider session.
public struct RuntimeProfileIdentity: Codable, Equatable, Sendable {
    public let daemonInstanceID: String
    public let profileRevision: UInt64
    public let profileHash: String
    public let sessionID: String

    public init(daemonInstanceID: String, profileRevision: UInt64, profileHash: String, sessionID: String) {
        self.daemonInstanceID = daemonInstanceID
        self.profileRevision = profileRevision
        self.profileHash = profileHash
        self.sessionID = sessionID
    }

    @discardableResult
    public func validated() throws -> RuntimeProfileIdentity {
        guard Self.isSafeIdentifier(daemonInstanceID) else { throw RuntimeProfileIdentityError.invalidDaemonInstanceID }
        guard profileRevision > 0 else { throw RuntimeProfileIdentityError.invalidProfileRevision }
        guard profileHash.count == 64, profileHash.allSatisfy({ $0.isHexDigit }) else {
            throw RuntimeProfileIdentityError.invalidProfileHash
        }
        guard Self.isSafeIdentifier(sessionID) else { throw RuntimeProfileIdentityError.invalidSessionID }
        return self
    }

    private static func isSafeIdentifier(_ value: String) -> Bool {
        let normalized = value.trimmingCharacters(in: .whitespacesAndNewlines)
        return !normalized.isEmpty && normalized == value && !normalized.contains("\n") && !normalized.contains("\r")
    }

    private enum CodingKeys: String, CodingKey {
        case daemonInstanceID = "daemon_instance_id"
        case profileRevision = "profile_revision"
        case profileHash = "profile_hash"
        case sessionID = "session_id"
    }
}

/// Exact active lease: stable routing identity plus its renewable validity deadline.
public struct RuntimeProfileLease: Equatable, Sendable {
    public let identity: RuntimeProfileIdentity
    public let validUntil: Date

    public init(identity: RuntimeProfileIdentity, validUntil: Date) {
        self.identity = identity
        self.validUntil = validUntil
    }
}

/// The authoritative status payload exchanged with the host UI and provider.
public struct ConnectionStatus: Codable, Equatable, Sendable {
    public let state: ConnectionLifecycleState
    public let transport: TransportState
    public let systemVPN: SystemVPNState
    public let providerState: ConnectionLifecycleState
    public let daemonInstanceID: String?
    public let profileRevision: UInt64?
    public let profileHash: String?
    public let sessionID: String?
    public let profileValidUntil: Date?
    public let message: String?

    public var profileIdentity: RuntimeProfileIdentity? {
        guard let daemonInstanceID, let profileRevision, let profileHash, let sessionID else { return nil }
        return RuntimeProfileIdentity(
            daemonInstanceID: daemonInstanceID,
            profileRevision: profileRevision,
            profileHash: profileHash,
            sessionID: sessionID
        )
    }

    public var profileLease: RuntimeProfileLease? {
        guard let identity = profileIdentity, let profileValidUntil else { return nil }
        return RuntimeProfileLease(identity: identity, validUntil: profileValidUntil)
    }

    public init(
        state: ConnectionLifecycleState,
        transport: TransportState,
        systemVPN: SystemVPNState,
        providerState: ConnectionLifecycleState,
        profileIdentity: RuntimeProfileIdentity? = nil,
        profileValidUntil: Date? = nil,
        message: String? = nil
    ) {
        self.state = state
        self.transport = transport
        self.systemVPN = systemVPN
        self.providerState = providerState
        self.daemonInstanceID = profileIdentity?.daemonInstanceID
        self.profileRevision = profileIdentity?.profileRevision
        self.profileHash = profileIdentity?.profileHash
        self.sessionID = profileIdentity?.sessionID
        self.profileValidUntil = profileValidUntil
        self.message = message
    }

    private enum CodingKeys: String, CodingKey {
        case state
        case transport
        case systemVPN = "system_vpn"
        case providerState = "provider_state"
        case daemonInstanceID = "daemon_instance_id"
        case profileRevision = "profile_revision"
        case profileHash = "profile_hash"
        case sessionID = "session_id"
        case profileValidUntil = "profile_valid_until"
        case message
    }
}

public enum RuntimeProviderStatusValidationError: Error, Equatable, Sendable {
    case profileMissing
    case profileMismatch
    case profileExpired
    case providerNotConnected
}

/// Requires provider-originated connected state for the exact profile requested by the host.
public enum RuntimeProviderStatusValidator {
    public static func requireConnected(
        _ status: ConnectionStatus,
        expected: RuntimeProfileLease,
        now: Date = Date()
    ) throws {
        guard let actual = status.profileLease else {
            throw RuntimeProviderStatusValidationError.profileMissing
        }
        guard actual == expected else { throw RuntimeProviderStatusValidationError.profileMismatch }
        guard actual.validUntil > now else { throw RuntimeProviderStatusValidationError.profileExpired }
        guard status.providerState == .connected, status.transport == .connected, status.state == .connected else {
            throw RuntimeProviderStatusValidationError.providerNotConnected
        }
    }
}

/// Provider commands intentionally use a small, versionable JSON protocol.
public enum ProviderCommand: String, Codable, Equatable, Sendable {
    case status
    case stop
}

public struct ProviderMessage: Codable, Equatable, Sendable {
    public let command: ProviderCommand
    public let requestID: String?

    public init(command: ProviderCommand, requestID: String? = nil) {
        self.command = command
        self.requestID = requestID
    }

    private enum CodingKeys: String, CodingKey {
        case command
        case requestID = "request_id"
    }
}

public struct ProviderMessageResponse: Codable, Equatable, Sendable {
    public let success: Bool
    public let state: ConnectionLifecycleState
    public let error: String?

    public init(success: Bool, state: ConnectionLifecycleState, error: String? = nil) {
        self.success = success
        self.state = state
        self.error = error
    }
}

public enum ProviderMessageCodecError: Error, Equatable {
    case invalidMessage
}

public enum ProviderMessageCodec {
    public static func encode(_ message: ProviderMessage) throws -> Data {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        return try encoder.encode(message)
    }

    public static func decode(_ data: Data) throws -> ProviderMessage {
        do {
            return try JSONDecoder().decode(ProviderMessage.self, from: data)
        } catch {
            throw ProviderMessageCodecError.invalidMessage
        }
    }

    public static func encode(_ response: ProviderMessageResponse) throws -> Data {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        return try encoder.encode(response)
    }
}

/// Merges transport and OS-VPN state without ever manufacturing a connected state.
public enum ConnectionStateReducer {
    public static func reduce(transport: TransportState, systemVPN: SystemVPNState) -> ConnectionLifecycleState {
        if systemVPN == .permissionRequired { return .permissionRequired }
        if transport == .unsupported || systemVPN == .unsupported { return .unsupported }
        if transport == .error || systemVPN == .error { return .error }
        if transport == .disconnecting || systemVPN == .disconnecting { return .disconnecting }
        if transport == .degraded || systemVPN == .degraded { return .degraded }
        if transport == .connected && systemVPN == .connected { return .connected }
        if transport == .connecting || systemVPN == .connecting { return .connecting }
        return .disconnected
    }
}

/// Public seam used by the provider to start/stop packet forwarding.
public protocol PacketFlowBridgeControlling: AnyObject {
    func start() throws
    func stop()
}
