import Foundation
import XCTest
@testable import WhiteTransportMacOS

#if canImport(Darwin)
import Darwin
#endif

final class PacketFlowBridgeTests: XCTestCase {
    func testPacketProtocolUsesIPVersionNibble() throws {
        XCTAssertEqual(try PacketFlowBridge.protocolNumber(for: Data([0x45, 0x00])).intValue, 2)
        XCTAssertEqual(try PacketFlowBridge.protocolNumber(for: Data([0x60, 0x00])).intValue, 30)
    }

    func testPacketProtocolRejectsNonIPPackets() {
        XCTAssertThrowsError(try PacketFlowBridge.protocolNumber(for: Data([0x11, 0x00]))) { error in
            XCTAssertEqual(error as? PacketFlowBridgeError, .unsupportedIPVersion(1))
        }
    }

    func testSocketPairMovesOneDatagramEachDirection() throws {
        let reverseWritten = expectation(description: "reverse packet written to NE flow")
        let flow = TestPacketFlow(writeHandler: { packets, protocols in
            XCTAssertEqual(packets, [Data([0x60, 0x01, 0x02])])
            XCTAssertEqual(protocols.map { $0.intValue }, [Int(AF_INET6)])
            reverseWritten.fulfill()
            return true
        })
        let engine = TestPacketEngine()
        let bridge = PacketFlowBridge(flow: flow, engine: engine, failureHandler: { _ in
            XCTFail("unexpected bridge failure")
        })
        try bridge.start()
        defer { bridge.stop() }

        let outbound = Data([0x45, 0x00, 0x01, 0x02])
        try flow.emit(outbound, protocolNumber: AF_INET)
        XCTAssertEqual(try engine.receive(timeoutMilliseconds: 1_000), outbound)

        try engine.send(Data([0x60, 0x01, 0x02]))
        wait(for: [reverseWritten], timeout: 2)
    }

    func testReverseWriteFailureReportsAsynchronouslyAndCleansUpOnce() throws {
        let failure = expectation(description: "bridge failure callback")
        failure.expectedFulfillmentCount = 1
        let flow = TestPacketFlow(writeHandler: { _, _ in false })
        let engine = TestPacketEngine()
        let bridge = PacketFlowBridge(flow: flow, engine: engine, failureHandler: { error in
            XCTAssertEqual(error as? PacketFlowBridgeError, .packetFlowWriteFailed)
            failure.fulfill()
        })
        try bridge.start()

        try engine.send(Data([0x45, 0x00, 0x01]))
        wait(for: [failure], timeout: 2)
        XCTAssertEqual(bridge.state, .failed)
        XCTAssertEqual(engine.stopCount, 1)

        bridge.stop()
        bridge.stop()
        XCTAssertEqual(engine.stopCount, 1)
    }

    func testEngineOwnsTransferredDescriptorAfterSuccessfulStart() throws {
        let flow = TestPacketFlow()
        let engine = TestPacketEngine(closeDescriptorOnStop: false)
        let bridge = PacketFlowBridge(flow: flow, engine: engine, failureHandler: { _ in })
        try bridge.start()
        let descriptor = try XCTUnwrap(engine.fileDescriptor)

        bridge.stop()

        XCTAssertEqual(engine.stopCount, 1)
        XCTAssertNotEqual(fcntl(descriptor, F_GETFD), -1, "bridge must not close an engine-owned descriptor")
        close(descriptor)
    }

    func testEAGAINPausesReadsAndBoundedQueueOverflowCancelsOnce() throws {
        let failure = expectation(description: "queue overflow failure")
        let flow = TestPacketFlow()
        let engine = TestPacketEngine(receiveBufferBytes: 1_024)
        let bridge = PacketFlowBridge(flow: flow, engine: engine, maxQueuedPackets: 1, failureHandler: { error in
            XCTAssertEqual(error as? PacketFlowBridgeError, .queueOverflow)
            failure.fulfill()
        })
        try bridge.start()

        let packet = Data([0x45]) + Data(repeating: 0xAB, count: 511)
        var overflow: Error?
        for _ in 0..<10_000 {
            do { try bridge.submitPacket(packet) }
            catch {
                overflow = error
                break
            }
        }

        XCTAssertEqual(overflow as? PacketFlowBridgeError, .queueOverflow)
        wait(for: [failure], timeout: 2)
        XCTAssertEqual(engine.stopCount, 1)
    }

    func testStopCancelsReadLoopAndIgnoresStalePacketCallback() throws {
        let flow = TestPacketFlow()
        let engine = TestPacketEngine()
        let bridge = PacketFlowBridge(flow: flow, engine: engine, failureHandler: { _ in
            XCTFail("stale callback must not fail stopped bridge")
        })
        try bridge.start()
        bridge.stop()

        try flow.emit(Data([0x45, 0x00]), protocolNumber: AF_INET)

        XCTAssertEqual(bridge.state, .stopped)
        XCTAssertEqual(engine.stopCount, 1)
        XCTAssertThrowsError(try engine.receive(timeoutMilliseconds: 50))
    }

    func testRepeatedStartStopClosesEverySwiftOwnedDescriptor() throws {
        // Warm libdispatch's first file-source bookkeeping before measuring product-owned descriptors.
        let warmup = PacketFlowBridge(flow: TestPacketFlow(), engine: TestPacketEngine(), failureHandler: { _ in })
        try warmup.start()
        warmup.stop()
        let baseline = try openDescriptorCount()

        for _ in 0..<32 {
            let bridge = PacketFlowBridge(flow: TestPacketFlow(), engine: TestPacketEngine(), failureHandler: { _ in })
            try bridge.start()
            bridge.stop()
            bridge.stop()
        }

        XCTAssertEqual(try openDescriptorCount(), baseline)
    }

    private func openDescriptorCount() throws -> Int {
        try FileManager.default.contentsOfDirectory(atPath: "/dev/fd").count
    }
}

private enum PacketTestError: Error { case timeout, descriptorUnavailable, sendFailed(Int32) }

private final class TestPacketFlow: PacketFlowBridgePacketFlow, @unchecked Sendable {
    private let lock = NSLock()
    private let writeHandler: ([Data], [NSNumber]) -> Bool
    private var readHandler: (@Sendable ([Data], [NSNumber]) -> Void)?

    init(writeHandler: @escaping ([Data], [NSNumber]) -> Bool = { _, _ in true }) {
        self.writeHandler = writeHandler
    }

    func readPackets(completionHandler: @Sendable @escaping ([Data], [NSNumber]) -> Void) {
        lock.lock()
        readHandler = completionHandler
        lock.unlock()
    }

    func writePackets(_ packets: [Data], withProtocols protocols: [NSNumber]) -> Bool {
        writeHandler(packets, protocols)
    }

    func emit(_ packet: Data, protocolNumber: Int32) throws {
        lock.lock()
        let handler = readHandler
        lock.unlock()
        guard let handler else { throw PacketTestError.descriptorUnavailable }
        handler([packet], [NSNumber(value: protocolNumber)])
    }
}

private final class TestPacketEngine: PacketFlowBridgeEngine, @unchecked Sendable {
    private let closeDescriptorOnStop: Bool
    private let receiveBufferBytes: Int32?
    private(set) var fileDescriptor: Int32?
    private(set) var offset: Int32?
    private(set) var stopCount = 0

    init(closeDescriptorOnStop: Bool = true, receiveBufferBytes: Int32? = nil) {
        self.closeDescriptorOnStop = closeDescriptorOnStop
        self.receiveBufferBytes = receiveBufferBytes
    }

    func start(fileDescriptor: Int32, offset: Int32) throws {
        self.fileDescriptor = fileDescriptor
        self.offset = offset
        if var receiveBufferBytes {
            setsockopt(fileDescriptor, SOL_SOCKET, SO_RCVBUF, &receiveBufferBytes, socklen_t(MemoryLayout<Int32>.size))
        }
    }

    func stop() {
        stopCount += 1
        if closeDescriptorOnStop, let fileDescriptor {
            close(fileDescriptor)
            self.fileDescriptor = nil
        }
    }

    func send(_ packet: Data) throws {
        guard let fileDescriptor else { throw PacketTestError.descriptorUnavailable }
        let result = packet.withUnsafeBytes { bytes in
            Darwin.send(fileDescriptor, bytes.baseAddress, packet.count, 0)
        }
        guard result == packet.count else { throw PacketTestError.sendFailed(errno) }
    }

    func receive(timeoutMilliseconds: Int32) throws -> Data {
        guard let fileDescriptor else { throw PacketTestError.descriptorUnavailable }
        var descriptor = pollfd(fd: fileDescriptor, events: Int16(POLLIN), revents: 0)
        guard poll(&descriptor, 1, timeoutMilliseconds) > 0 else { throw PacketTestError.timeout }
        var buffer = [UInt8](repeating: 0, count: 65_535)
        let count = Darwin.recv(fileDescriptor, &buffer, buffer.count, 0)
        guard count > 0 else { throw PacketTestError.sendFailed(errno) }
        return Data(buffer.prefix(count))
    }
}
