import XCTest
@testable import whitelist_bypass_proxy

/// Add this file to an XCTest bundle when the iOS project gains one.
/// The application target intentionally has no XCTest target today, so keeping
/// this outside the synchronized app source group avoids shipping XCTest.
final class LaunchTestContractTests: XCTestCase {
    func testRequiresBothXCTestEnvironmentAndConnectArgument() {
        XCTAssertNil(LaunchTestContract.parse(
            arguments: ["app", LaunchTestContract.connectArgument, LaunchTestContract.callURLArgument, "https://example.invalid/room"],
            environment: [:]
        ))
        XCTAssertNil(LaunchTestContract.parse(
            arguments: ["app", LaunchTestContract.callURLArgument, "https://example.invalid/room"],
            environment: [LaunchTestContract.testEnvironmentKey: "1"]
        ))
    }

    func testAcceptsExplicitRoomLinkWithoutClaimingNodeSelection() {
        let request = LaunchTestContract.parse(
            arguments: ["app", LaunchTestContract.connectArgument, LaunchTestContract.callURLArgument, "https://example.invalid/room"],
            environment: [LaunchTestContract.testEnvironmentKey: "1"]
        )

        XCTAssertEqual(request, LaunchTestContract.Request(callURL: "https://example.invalid/room"))
    }

    func testRejectsMissingOrMalformedRoomLink() {
        XCTAssertNil(LaunchTestContract.parse(
            arguments: ["app", LaunchTestContract.connectArgument],
            environment: [LaunchTestContract.testEnvironmentKey: "1"]
        ))
        XCTAssertNil(LaunchTestContract.parse(
            arguments: ["app", LaunchTestContract.connectArgument, LaunchTestContract.callURLArgument, "not a url"],
            environment: [LaunchTestContract.testEnvironmentKey: "1"]
        ))
    }
}
