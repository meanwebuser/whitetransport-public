import Foundation
import ServiceManagement
import XCTest
@testable import WhiteTransportMacOS

final class SMAppServiceProbeTests: XCTestCase {
    func testProbeUsesMacOS13SMAppServiceDaemonAndHealthOperation() throws {
        if #available(macOS 13, *) {
            let service = SMAppService.daemon(plistName: "com.meanwebuser.whitetransport.net-helper.plist")
            XCTAssertNotNil(service)
        }
        XCTAssertEqual(WTMacAuthorizationProbe.serviceName, "com.meanwebuser.whitetransport.net-helper")
        let result = WTMacAuthorizationProbeResult(supported: false, registered: false, authorized: false, error: "unsupported")
        XCTAssertEqual(result.operation, "health")
        XCTAssertFalse(result.authorized)
    }
}
