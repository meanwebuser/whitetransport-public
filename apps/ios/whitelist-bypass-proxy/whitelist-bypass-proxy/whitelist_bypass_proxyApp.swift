import SwiftUI

@main
struct WhitelistBypassProxyApp: App {
    @StateObject private var proxyManager = ProxyManager()

    var body: some Scene {
        WindowGroup {
            // Unified client UI = the React app in a Capacitor WebView (parity with
            // Android's CapacitorMainActivity). The legacy SwiftUI ContentView is
            // retained in the target for rollback but no longer the root.
            CapacitorWebView(proxyManager: proxyManager)
                .environmentObject(proxyManager)
                .ignoresSafeArea()
                .task {
                    await LaunchTestContract.runIfRequested(proxyManager: proxyManager)
                }
        }
    }
}
