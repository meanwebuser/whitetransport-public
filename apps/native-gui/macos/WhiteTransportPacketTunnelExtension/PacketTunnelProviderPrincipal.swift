import WhiteTransportMacOS

/// Principal class binds the extension-local static Go engine; the containing app never loads it.
final class PacketTunnelProvider: WhiteTransportMacOS.PacketTunnelProvider, @unchecked Sendable {
    override func makeBridgeFactory(packetFlow: PacketFlowBridgePacketFlow) -> PacketTunnelBridgeBuilding {
        PacketTunnelExtensionBridgeFactory(flow: packetFlow, engineBuilder: DarwinPacketFlowEngineBuilder())
    }
}
