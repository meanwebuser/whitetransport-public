// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "WhiteTransportMacOS",
    platforms: [.macOS(.v12)],
    products: [
        .library(name: "WhiteTransportMacOS", targets: ["WhiteTransportMacOS"])
    ],
    targets: [
        .target(
            name: "WhiteTransportMacOS",
            path: ".",
            exclude: [
                "WhiteTransport.xcodeproj",
                "WhiteTransportApp",
                "EngineBridge",
                "TestFixtures",
                "scripts",
                "WhiteTransportPacketTunnelExtension",
                "WhiteTransportPacketTunnelTests",
                "direct-helper",
                "Package.swift",
                "README.md"
            ]
        ),
        .testTarget(
            name: "WhiteTransportPacketTunnelTests",
            dependencies: ["WhiteTransportMacOS"],
            path: "WhiteTransportPacketTunnelTests"
        )
    ]
)
