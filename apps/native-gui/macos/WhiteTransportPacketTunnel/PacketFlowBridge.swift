import Foundation

#if canImport(Darwin)
import Darwin
#elseif canImport(Glibc)
import Glibc
#endif

public enum PacketFlowBridgeState: String, Equatable, Sendable {
    case idle
    case running
    case stopped
    case failed
}

public enum PacketFlowBridgeError: Error, Equatable, LocalizedError, Sendable {
    case alreadyStarted
    case notRunning
    case invalidSocketPair(Int32)
    case engineStartFailed(String)
    case unsupportedIPVersion(UInt8)
    case emptyPacket
    case protocolCountMismatch
    case socketWriteFailed(Int32)
    case socketReadFailed(Int32)
    case socketReadClosed
    case queueOverflow
    case packetFlowWriteFailed

    public var errorDescription: String? {
        switch self {
        case .alreadyStarted: return "packet-flow bridge is already started"
        case .notRunning: return "packet-flow bridge is not running"
        case .invalidSocketPair(let error): return "socketpair failed: errno \(error)"
        case .engineStartFailed(let error): return "packet engine failed to start: \(error)"
        case .unsupportedIPVersion(let version): return "unsupported IP version: \(version)"
        case .emptyPacket: return "empty packet cannot be forwarded"
        case .protocolCountMismatch: return "packet and protocol arrays differ in length"
        case .socketWriteFailed(let error): return "packet socket write failed: errno \(error)"
        case .socketReadFailed(let error): return "packet socket read failed: errno \(error)"
        case .socketReadClosed: return "packet socket peer closed"
        case .queueOverflow: return "packet bridge queue overflowed"
        case .packetFlowWriteFailed: return "NEPacketTunnelFlow.writePackets returned false"
        }
    }
}

/// Public packet-flow surface implemented by NEPacketTunnelFlow and deterministic tests.
public protocol PacketFlowBridgePacketFlow: AnyObject {
    func readPackets(completionHandler: @Sendable @escaping ([Data], [NSNumber]) -> Void)
    @discardableResult
    func writePackets(_ packets: [Data], withProtocols protocols: [NSNumber]) -> Bool
}

#if canImport(NetworkExtension)
@preconcurrency import NetworkExtension
extension NEPacketTunnelFlow: PacketFlowBridgePacketFlow {}
#endif

/// The engine owns `fileDescriptor` after `start` succeeds and must close it from `stop`.
/// If `start` throws, ownership remains with the bridge and the bridge closes the descriptor.
public protocol PacketFlowBridgeEngine: AnyObject {
    func start(fileDescriptor: Int32, offset: Int32) throws
    func stop()
}

/// Full-duplex public-API bridge between NEPacketTunnelFlow and a tun2socks engine socket.
/// Every packet is one AF_UNIX/SOCK_DGRAM datagram; no stream framing or private KVC is used.
public final class PacketFlowBridge: @unchecked Sendable, PacketFlowBridgeControlling {
    public var state: PacketFlowBridgeState { queue.sync { lifecycleState } }
    public var lastError: PacketFlowBridgeError? { queue.sync { lifecycleError } }

    private let flow: PacketFlowBridgePacketFlow
    private let engine: PacketFlowBridgeEngine
    private let maxQueuedPackets: Int
    private let failureHandler: @Sendable (Error) -> Void
    private let queue = DispatchQueue(label: "com.meanwebuser.whitetransport.packet-flow")

    private var lifecycleState: PacketFlowBridgeState = .idle
    private var lifecycleError: PacketFlowBridgeError?
    private var generation: UInt64 = 0
    private var bridgeFD: Int32 = -1
    private var bridgeDescriptorOwner: SwiftOwnedDescriptor?
    private var readSource: DispatchSourceRead?
    private var writeSource: DispatchSourceWrite?
    private var writeRetry: DispatchWorkItem?
    private var writeBackoffMilliseconds = 5
    private var queuedPackets: [Data] = []
    private var readPending = false
    private var readPaused = false
    private var engineStarted = false
    private var cleanupPerformed = true
    private var failureDelivered = false

    public init(
        flow: PacketFlowBridgePacketFlow,
        engine: PacketFlowBridgeEngine,
        maxQueuedPackets: Int = 256,
        failureHandler: @escaping @Sendable (Error) -> Void = { _ in }
    ) {
        self.flow = flow
        self.engine = engine
        self.maxQueuedPackets = max(1, maxQueuedPackets)
        self.failureHandler = failureHandler
    }

    public func start() throws {
        try queue.sync {
            if lifecycleState == .running { return }
            try startLocked()
        }
    }

    public func stop() {
        queue.sync {
            if lifecycleState == .stopped || lifecycleState == .idle { lifecycleState = .stopped; return }
            generation &+= 1
            cleanupLocked()
            lifecycleState = .stopped
        }
    }

    public func submitPacket(_ packet: Data) throws {
        try queue.sync { try submitPacketLocked(packet) }
    }

    public func ingestEnginePacket(_ packet: Data) throws {
        try queue.sync { try ingestEnginePacketLocked(packet) }
    }

    public static func protocolNumber(for packet: Data) throws -> NSNumber {
        guard let firstByte = packet.first else { throw PacketFlowBridgeError.emptyPacket }
        switch firstByte >> 4 {
        case 4: return NSNumber(value: AF_INET)
        case 6: return NSNumber(value: AF_INET6)
        case let version: throw PacketFlowBridgeError.unsupportedIPVersion(version)
        }
    }

    private func startLocked() throws {
        generation &+= 1
        lifecycleError = nil
        queuedPackets.removeAll(keepingCapacity: false)
        writeBackoffMilliseconds = 5
        readPending = false
        readPaused = false
        failureDelivered = false
        cleanupPerformed = false

        var descriptors = [Int32](repeating: -1, count: 2)
        guard socketpair(AF_UNIX, Int32(SOCK_DGRAM), 0, &descriptors) == 0 else {
            let error = PacketFlowBridgeError.invalidSocketPair(errno)
            lifecycleState = .failed
            lifecycleError = error
            cleanupPerformed = true
            throw error
        }

        do {
            try Self.makeNonblocking(descriptors[0])
            try Self.makeNonblocking(descriptors[1])
        } catch {
            close(descriptors[0])
            close(descriptors[1])
            lifecycleState = .failed
            lifecycleError = error as? PacketFlowBridgeError
            cleanupPerformed = true
            throw error
        }

        bridgeFD = descriptors[0]
        bridgeDescriptorOwner = SwiftOwnedDescriptor(descriptor: descriptors[0])
        let engineDescriptor = dup(descriptors[1])
        close(descriptors[1])
        guard engineDescriptor >= 0 else {
            let error = PacketFlowBridgeError.invalidSocketPair(errno)
            bridgeDescriptorOwner?.close()
            bridgeDescriptorOwner = nil
            bridgeFD = -1
            lifecycleState = .failed
            lifecycleError = error
            cleanupPerformed = true
            throw error
        }

        do {
            try engine.start(fileDescriptor: engineDescriptor, offset: 0)
            engineStarted = true // Ownership transferred; only the engine may close this descriptor now.
        } catch {
            close(engineDescriptor)
            let bridgeError = PacketFlowBridgeError.engineStartFailed(String(describing: error))
            lifecycleError = bridgeError
            lifecycleState = .failed
            cleanupLocked()
            throw bridgeError
        }

        let source = DispatchSource.makeReadSource(fileDescriptor: bridgeFD, queue: queue)
        source.setEventHandler { [weak self] in self?.handleReadableLocked() }
        let descriptorOwner = bridgeDescriptorOwner
        source.setCancelHandler { descriptorOwner?.close() }
        readSource = source
        source.resume()
        lifecycleState = .running
        schedulePacketFlowReadLocked()
    }

    private static func makeNonblocking(_ descriptor: Int32) throws {
        let current = fcntl(descriptor, F_GETFL, 0)
        guard current >= 0, fcntl(descriptor, F_SETFL, current | O_NONBLOCK) == 0 else {
            throw PacketFlowBridgeError.invalidSocketPair(errno)
        }
    }

    private func schedulePacketFlowReadLocked() {
        guard lifecycleState == .running, !readPending, !readPaused else { return }
        readPending = true
        let activeGeneration = generation
        flow.readPackets { [weak self] packets, protocols in
            self?.queue.async { [weak self] in
                guard let self, self.generation == activeGeneration, self.lifecycleState == .running else { return }
                self.readPending = false
                do {
                    guard packets.count == protocols.count else { throw PacketFlowBridgeError.protocolCountMismatch }
                    for packet in packets { try self.submitPacketLocked(packet) }
                    if self.queuedPackets.isEmpty { self.schedulePacketFlowReadLocked() }
                } catch let error as PacketFlowBridgeError {
                    self.failLocked(error)
                } catch {
                    self.failLocked(.socketWriteFailed(errno))
                }
            }
        }
    }

    private func submitPacketLocked(_ packet: Data) throws {
        guard lifecycleState == .running else { throw PacketFlowBridgeError.notRunning }
        _ = try Self.protocolNumber(for: packet)
        if !queuedPackets.isEmpty {
            try enqueueLocked(packet)
            return
        }
        let result = sendDatagramLocked(packet)
        if result == packet.count { return }
        if result < 0, Self.isBackpressureError(errno) {
            try enqueueLocked(packet)
            return
        }
        let error = PacketFlowBridgeError.socketWriteFailed(result >= 0 ? EIO : errno)
        failLocked(error)
        throw error
    }

    private func enqueueLocked(_ packet: Data) throws {
        guard queuedPackets.count < maxQueuedPackets else {
            failLocked(.queueOverflow)
            throw PacketFlowBridgeError.queueOverflow
        }
        queuedPackets.append(packet)
        readPaused = true
        armWriteReadinessLocked()
    }

    private func sendDatagramLocked(_ packet: Data) -> Int {
        packet.withUnsafeBytes { bytes in
            guard let baseAddress = bytes.baseAddress else { return -1 }
            return send(bridgeFD, baseAddress, packet.count, 0)
        }
    }

    private func armWriteReadinessLocked() {
        guard lifecycleState == .running, bridgeFD >= 0, writeSource == nil, writeRetry == nil else { return }
        let source = DispatchSource.makeWriteSource(fileDescriptor: bridgeFD, queue: queue)
        source.setEventHandler { [weak self] in self?.handleWritableLocked() }
        writeSource = source
        source.resume()
    }

    private func handleWritableLocked() {
        writeSource?.cancel()
        writeSource = nil
        do {
            if try drainQueuedPacketsLocked() {
                writeBackoffMilliseconds = 5
                readPaused = false
                schedulePacketFlowReadLocked()
            } else {
                scheduleWriteRetryLocked()
            }
        } catch let error as PacketFlowBridgeError {
            failLocked(error)
        } catch {
            failLocked(.socketWriteFailed(errno))
        }
    }

    private func drainQueuedPacketsLocked() throws -> Bool {
        while let packet = queuedPackets.first {
            let result = sendDatagramLocked(packet)
            if result == packet.count {
                queuedPackets.removeFirst()
                continue
            }
            if result < 0, Self.isBackpressureError(errno) { return false }
            let error = PacketFlowBridgeError.socketWriteFailed(result >= 0 ? EIO : errno)
            failLocked(error)
            throw error
        }
        return true
    }

    private func scheduleWriteRetryLocked() {
        guard writeRetry == nil, lifecycleState == .running else { return }
        let delay = min(writeBackoffMilliseconds, 250)
        writeBackoffMilliseconds = min(delay * 2, 250)
        let work = DispatchWorkItem { [weak self] in
            guard let self else { return }
            self.writeRetry = nil
            self.armWriteReadinessLocked()
        }
        writeRetry = work
        queue.asyncAfter(deadline: .now() + .milliseconds(delay), execute: work)
    }

    private static func isBackpressureError(_ value: Int32) -> Bool {
        // Darwin AF_UNIX datagrams may report ENOBUFS instead of EAGAIN when the peer queue is saturated.
        value == EAGAIN || value == EWOULDBLOCK || value == ENOBUFS
    }

    private func handleReadableLocked() {
        guard lifecycleState == .running, bridgeFD >= 0 else { return }
        var buffer = [UInt8](repeating: 0, count: 65_535)
        while true {
            let count = recv(bridgeFD, &buffer, buffer.count, 0)
            if count > 0 {
                do { try ingestEnginePacketLocked(Data(buffer.prefix(count))) }
                catch { return }
                continue
            }
            if count == 0 {
                failLocked(.socketReadClosed)
                return
            }
            if errno == EINTR { continue }
            if errno == EAGAIN || errno == EWOULDBLOCK { return }
            failLocked(.socketReadFailed(errno))
            return
        }
    }

    private func ingestEnginePacketLocked(_ packet: Data) throws {
        guard lifecycleState == .running else { throw PacketFlowBridgeError.notRunning }
        let protocolNumber = try Self.protocolNumber(for: packet)
        guard flow.writePackets([packet], withProtocols: [protocolNumber]) else {
            failLocked(.packetFlowWriteFailed)
            throw PacketFlowBridgeError.packetFlowWriteFailed
        }
    }

    private func failLocked(_ error: PacketFlowBridgeError) {
        guard lifecycleState == .running || lifecycleState == .failed else { return }
        lifecycleError = error
        lifecycleState = .failed
        generation &+= 1
        cleanupLocked()
        guard !failureDelivered else { return }
        failureDelivered = true
        let handler = failureHandler
        DispatchQueue.global(qos: .userInitiated).async { handler(error) }
    }

    private func cleanupLocked() {
        guard !cleanupPerformed else { return }
        cleanupPerformed = true
        writeRetry?.cancel()
        writeRetry = nil
        writeSource?.cancel()
        writeSource = nil
        readSource?.cancel()
        readSource = nil
        bridgeDescriptorOwner?.close()
        bridgeDescriptorOwner = nil
        bridgeFD = -1
        queuedPackets.removeAll(keepingCapacity: false)
        readPending = false
        readPaused = false
        if engineStarted {
            engineStarted = false
            engine.stop()
        }
    }
}

/// Closes a Swift-owned descriptor once even when cleanup and a dispatch-source cancel handler race.
private final class SwiftOwnedDescriptor: @unchecked Sendable {
    private let lock = NSLock()
    private var descriptor: Int32?

    init(descriptor: Int32) { self.descriptor = descriptor }

    func close() {
        lock.lock()
        let descriptor = self.descriptor
        self.descriptor = nil
        lock.unlock()
        if let descriptor {
            #if canImport(Darwin)
            Darwin.close(descriptor)
            #elseif canImport(Glibc)
            Glibc.close(descriptor)
            #endif
        }
    }

    deinit { close() }
}
