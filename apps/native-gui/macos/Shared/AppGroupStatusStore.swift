import Darwin
import Foundation

public enum WhiteTransportAppGroup {
    public static let identifier = "group.com.meanwebuser.whitetransport"
    public static let schemaVersion = 2
}

public enum AppGroupLogLevel: String, Codable, Equatable, Sendable {
    case info
    case warning
    case error
}

public enum AppGroupLogEvent: String, Codable, Equatable, Sendable {
    case message
    case statusTransition = "status_transition"
    case error
}

/// JSON-compatible metadata used by the app-group event exchange.
public indirect enum AppGroupJSONValue: Codable, Equatable, Sendable {
    case string(String)
    case integer(Int64)
    case decimal(Double)
    case boolean(Bool)
    case object([String: AppGroupJSONValue])
    case array([AppGroupJSONValue])
    case null

    public init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        if container.decodeNil() {
            self = .null
        } else if let value = try? container.decode(Bool.self) {
            self = .boolean(value)
        } else if let value = try? container.decode(Int64.self) {
            self = .integer(value)
        } else if let value = try? container.decode(Double.self) {
            self = .decimal(value)
        } else if let value = try? container.decode(String.self) {
            self = .string(value)
        } else if let value = try? container.decode([String: AppGroupJSONValue].self) {
            self = .object(value)
        } else if let value = try? container.decode([AppGroupJSONValue].self) {
            self = .array(value)
        } else {
            throw AppGroupStatusStoreError.invalidRecord
        }
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        switch self {
        case let .string(value): try container.encode(value)
        case let .integer(value): try container.encode(value)
        case let .decimal(value): try container.encode(value)
        case let .boolean(value): try container.encode(value)
        case let .object(value): try container.encode(value)
        case let .array(value): try container.encode(value)
        case .null: try container.encodeNil()
        }
    }
}

public struct AppGroupStatusRecord: Codable, Equatable, Sendable {
    public let schemaVersion: Int
    public let sequence: UInt64
    public let updatedAt: Date
    public let status: ConnectionStatus

    private enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case sequence
        case updatedAt = "updated_at"
        case status
    }
}

public struct AppGroupErrorRecord: Codable, Equatable, Sendable {
    public let domain: String
    public let code: Int
    public let message: String
}

public struct AppGroupLogRecord: Codable, Equatable, Sendable {
    public let schemaVersion: Int
    public let timestamp: Date
    public let level: AppGroupLogLevel
    public let event: AppGroupLogEvent
    public let message: String?
    public let previousState: ConnectionLifecycleState?
    public let status: ConnectionStatus?
    public let error: AppGroupErrorRecord?
    public let metadata: [String: AppGroupJSONValue]

    private enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case timestamp
        case level
        case event
        case message
        case previousState = "previous_state"
        case status
        case error
        case metadata
    }
}

public enum AppGroupStatusStoreError: Error, Equatable {
    case containerUnavailable(String)
    case invalidRecord
    case lockFailed(Int32)
    case recordTooLarge(maximumBytes: Int)
}

/// Versioned, redacted exchange shared by the containing app and packet-tunnel extension.
public final class AppGroupStatusStore: @unchecked Sendable {
    public let statusFileURL: URL
    public let logFileURL: URL
    public let rotatedLogFileURL: URL
    public let lockFileURL: URL

    private let directoryURL: URL
    private let maxLogBytes: Int
    private let now: @Sendable () -> Date
    private let queue = DispatchQueue(label: "com.meanwebuser.whitetransport.app-group-store")
    private var sequence: UInt64 = 0

    public init(
        directoryURL: URL,
        maxLogBytes: Int = 256 * 1024,
        now: @escaping @Sendable () -> Date = Date.init
    ) {
        precondition(maxLogBytes > 0, "maxLogBytes must be positive")
        self.directoryURL = directoryURL
        self.statusFileURL = directoryURL.appendingPathComponent("status-v2.json")
        self.logFileURL = directoryURL.appendingPathComponent("events-v2.jsonl")
        self.rotatedLogFileURL = directoryURL.appendingPathComponent("events-v2.previous.jsonl")
        self.lockFileURL = directoryURL.appendingPathComponent("store-v2.lock")
        self.maxLogBytes = maxLogBytes
        self.now = now
    }

    public convenience init(appGroupIdentifier: String = WhiteTransportAppGroup.identifier) throws {
        guard let directory = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroupIdentifier) else {
            throw AppGroupStatusStoreError.containerUnavailable(appGroupIdentifier)
        }
        self.init(directoryURL: directory)
    }

    /// Persists the latest status and appends a structured transition event under one file lock.
    public func write(status: ConnectionStatus, metadata: [String: AppGroupJSONValue] = [:]) throws {
        try queue.sync {
            try withExclusiveFileLock {
                let previousRecord = try readStatusLocked()
                let baseSequence = max(sequence, previousRecord?.sequence ?? 0)
                guard baseSequence < UInt64.max else { throw AppGroupStatusStoreError.invalidRecord }
                let nextSequence = baseSequence + 1
                let redactedStatus = SharedDataRedactor.redact(status)
                let record = AppGroupStatusRecord(
                    schemaVersion: WhiteTransportAppGroup.schemaVersion,
                    sequence: nextSequence,
                    updatedAt: now(),
                    status: redactedStatus
                )
                let event = AppGroupLogRecord(
                    schemaVersion: WhiteTransportAppGroup.schemaVersion,
                    timestamp: now(),
                    level: .info,
                    event: .statusTransition,
                    message: nil,
                    previousState: previousRecord?.status.state,
                    status: redactedStatus,
                    error: nil,
                    metadata: SharedDataRedactor.redact(metadata)
                )
                try appendLogRecordLocked(event)
                try encode(record).write(to: statusFileURL, options: .atomic)
                try setPrivatePermissions(statusFileURL)
                sequence = nextSequence
            }
        }
    }

    public func readStatus() throws -> AppGroupStatusRecord? {
        try queue.sync { try readStatusGuarded() }
    }

    public func readStatus(
        callbackQueue: DispatchQueue,
        completion: @escaping @Sendable (Result<AppGroupStatusRecord?, Error>) -> Void
    ) {
        queue.async { [self] in
            let result = Result { try readStatusGuarded() }
            callbackQueue.async { completion(result) }
        }
    }

    public func appendLog(
        level: AppGroupLogLevel,
        message: String,
        metadata: [String: AppGroupJSONValue] = [:]
    ) throws {
        let record = AppGroupLogRecord(
            schemaVersion: WhiteTransportAppGroup.schemaVersion,
            timestamp: now(),
            level: level,
            event: .message,
            message: SharedDataRedactor.redact(message),
            previousState: nil,
            status: nil,
            error: nil,
            metadata: SharedDataRedactor.redact(metadata)
        )
        try append(record)
    }

    /// Appends a structured, recursively redacted error event for host diagnostics.
    public func appendError(
        _ error: Error,
        status: ConnectionStatus? = nil,
        metadata: [String: AppGroupJSONValue] = [:]
    ) throws {
        let cocoaError = error as NSError
        let record = AppGroupLogRecord(
            schemaVersion: WhiteTransportAppGroup.schemaVersion,
            timestamp: now(),
            level: .error,
            event: .error,
            message: nil,
            previousState: nil,
            status: status.map(SharedDataRedactor.redact),
            error: AppGroupErrorRecord(
                domain: SharedDataRedactor.redact(cocoaError.domain),
                code: cocoaError.code,
                message: SharedDataRedactor.redact(cocoaError.localizedDescription)
            ),
            metadata: SharedDataRedactor.redact(metadata)
        )
        try append(record)
    }

    public func readLogs() throws -> [AppGroupLogRecord] {
        try queue.sync { try readLogsGuarded() }
    }

    public func readLogs(
        callbackQueue: DispatchQueue,
        completion: @escaping @Sendable (Result<[AppGroupLogRecord], Error>) -> Void
    ) {
        queue.async { [self] in
            let result = Result { try readLogsGuarded() }
            callbackQueue.async { completion(result) }
        }
    }

    private func append(_ record: AppGroupLogRecord) throws {
        try queue.sync {
            try withExclusiveFileLock { try appendLogRecordLocked(record) }
        }
    }

    private func readStatusGuarded() throws -> AppGroupStatusRecord? {
        try withExclusiveFileLock {
            let record = try readStatusLocked()
            if let record { sequence = max(sequence, record.sequence) }
            return record
        }
    }

    private func readStatusLocked() throws -> AppGroupStatusRecord? {
        guard FileManager.default.fileExists(atPath: statusFileURL.path) else { return nil }
        let record = try decode(AppGroupStatusRecord.self, from: Data(contentsOf: statusFileURL))
        guard record.schemaVersion == WhiteTransportAppGroup.schemaVersion else {
            throw AppGroupStatusStoreError.invalidRecord
        }
        return record
    }

    private func readLogsGuarded() throws -> [AppGroupLogRecord] {
        try withExclusiveFileLock {
            try [rotatedLogFileURL, logFileURL].flatMap(readLogFile)
        }
    }

    private func readLogFile(_ url: URL) throws -> [AppGroupLogRecord] {
        guard FileManager.default.fileExists(atPath: url.path) else { return [] }
        return try Data(contentsOf: url).split(separator: 0x0A).map { line in
            let record = try decode(AppGroupLogRecord.self, from: Data(line))
            guard record.schemaVersion == WhiteTransportAppGroup.schemaVersion else {
                throw AppGroupStatusStoreError.invalidRecord
            }
            return record
        }
    }

    private func appendLogRecordLocked(_ record: AppGroupLogRecord) throws {
        var data = try encode(record)
        data.append(0x0A)
        guard data.count <= maxLogBytes else {
            throw AppGroupStatusStoreError.recordTooLarge(maximumBytes: maxLogBytes)
        }

        let currentSize = try fileSizeIfPresent(logFileURL)
        if currentSize + data.count > maxLogBytes, currentSize > 0 {
            if FileManager.default.fileExists(atPath: rotatedLogFileURL.path) {
                try FileManager.default.removeItem(at: rotatedLogFileURL)
            }
            try FileManager.default.moveItem(at: logFileURL, to: rotatedLogFileURL)
            try setPrivatePermissions(rotatedLogFileURL)
        }

        if FileManager.default.fileExists(atPath: logFileURL.path) {
            let handle = try FileHandle(forWritingTo: logFileURL)
            defer { try? handle.close() }
            try handle.seekToEnd()
            try handle.write(contentsOf: data)
        } else {
            try data.write(to: logFileURL, options: .atomic)
        }
        try setPrivatePermissions(logFileURL)
    }

    private func fileSizeIfPresent(_ url: URL) throws -> Int {
        guard FileManager.default.fileExists(atPath: url.path) else { return 0 }
        let attributes = try FileManager.default.attributesOfItem(atPath: url.path)
        return (attributes[.size] as? NSNumber)?.intValue ?? 0
    }

    private func withExclusiveFileLock<T>(_ body: () throws -> T) throws -> T {
        try ensureDirectory()
        let descriptor = lockFileURL.path.withCString {
            Darwin.open($0, O_CREAT | O_RDWR, mode_t(S_IRUSR | S_IWUSR))
        }
        guard descriptor >= 0 else { throw AppGroupStatusStoreError.lockFailed(errno) }
        defer { Darwin.close(descriptor) }
        try setPrivatePermissions(lockFileURL)

        while flock(descriptor, LOCK_EX) != 0 {
            guard errno == EINTR else { throw AppGroupStatusStoreError.lockFailed(errno) }
        }
        defer { flock(descriptor, LOCK_UN) }
        return try body()
    }

    private func ensureDirectory() throws {
        try FileManager.default.createDirectory(at: directoryURL, withIntermediateDirectories: true)
    }

    private func setPrivatePermissions(_ url: URL) throws {
        try FileManager.default.setAttributes([.posixPermissions: NSNumber(value: Int16(0o600))], ofItemAtPath: url.path)
    }

    private func encode<T: Encodable>(_ value: T) throws -> Data {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        encoder.outputFormatting = [.sortedKeys]
        return try encoder.encode(value)
    }

    private func decode<T: Decodable>(_ type: T.Type, from data: Data) throws -> T {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        return try decoder.decode(type, from: data)
    }
}

public enum SharedDataRedactor {
    private static let credentialExpression = try! NSRegularExpression(
        pattern: #"(?i)(?:authorization\s*[=:]\s*(?:bearer\s+)?\S+|(?:access[_-]?token|token|cookie|password|secret)\s*[=:]\s*\S+)"#
    )
    private static let endpointExpression = try! NSRegularExpression(
        pattern: #"(?i)(?:\b(?:https?|socks5)://\S+|\b(?:host|hostname|url|endpoint)\s*[=:]\s*\S+)"#
    )
    private static let bareHostExpression = try! NSRegularExpression(
        pattern: #"(?i)(?<![a-z0-9_-])(?:[a-z0-9](?:[a-z0-9-]{0,62}\.)+[a-z]{2,63}|(?:\d{1,3}\.){3}\d{1,3})(?::\d{1,5})?(?![a-z0-9_-])"#
    )
    private static let credentialKeyFragments = ["token", "password", "cookie", "secret", "authorization"]
    private static let endpointKeyFragments = ["host", "url", "endpoint"]

    public static func redact(_ status: ConnectionStatus) -> ConnectionStatus {
        ConnectionStatus(
            state: status.state,
            transport: status.transport,
            systemVPN: status.systemVPN,
            providerState: status.providerState,
            profileIdentity: status.profileIdentity,
            profileValidUntil: status.profileValidUntil,
            message: status.message.map(redact)
        )
    }

    public static func redact(_ metadata: [String: AppGroupJSONValue]) -> [String: AppGroupJSONValue] {
        metadata.reduce(into: [:]) { result, pair in
            result[pair.key] = redact(pair.value, key: pair.key)
        }
    }

    public static func redact(_ value: String) -> String {
        let endpointRange = NSRange(value.startIndex..<value.endIndex, in: value)
        let withoutEndpoints = endpointExpression.stringByReplacingMatches(
            in: value,
            range: endpointRange,
            withTemplate: "<endpoint>"
        )
        let hostRange = NSRange(withoutEndpoints.startIndex..<withoutEndpoints.endIndex, in: withoutEndpoints)
        let withoutHosts = bareHostExpression.stringByReplacingMatches(
            in: withoutEndpoints,
            range: hostRange,
            withTemplate: "<endpoint>"
        )
        let credentialRange = NSRange(withoutHosts.startIndex..<withoutHosts.endIndex, in: withoutHosts)
        return credentialExpression.stringByReplacingMatches(
            in: withoutHosts,
            range: credentialRange,
            withTemplate: "<redacted>"
        )
    }

    private static func redact(_ value: AppGroupJSONValue, key: String?) -> AppGroupJSONValue {
        if let key {
            let normalizedKey = key.lowercased()
            if credentialKeyFragments.contains(where: normalizedKey.contains) { return .string("<redacted>") }
            if endpointKeyFragments.contains(where: normalizedKey.contains) { return .string("<endpoint>") }
        }
        switch value {
        case let .string(value): return .string(redact(value))
        case let .object(value): return .object(redact(value))
        case let .array(value): return .array(value.map { redact($0, key: nil) })
        case .integer, .decimal, .boolean, .null: return value
        }
    }
}
