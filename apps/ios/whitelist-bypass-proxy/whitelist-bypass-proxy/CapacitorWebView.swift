import SwiftUI
import WebKit
import Capacitor
import os

// Hosts the Capacitor WebView (the unified React client UI from apps/client-web)
// inside the SwiftUI app, mirroring Android's CapacitorMainActivity. The bridge
// auto-loads capacitor.config.json + public/ from the app bundle and registers
// WtTransportPlugin.
//
// Critical wiring: the plugin reaches the transport layer through
// WtTransportPlugin.proxyManager. We inject the SwiftUI @EnvironmentObject
// instance here so plugin calls act on the same ProxyManager the native UI used.
struct CapacitorWebView: UIViewControllerRepresentable {
    let proxyManager: ProxyManager

    private static let log = Logger(subsystem: "bypass.whitelist", category: "webview")

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIViewController(context: Context) -> CAPBridgeViewController {
        WtTransportPlugin.proxyManager = proxyManager
        let vc = CAPBridgeViewController()
        // Attach navigation + console instrumentation once the bridge's WKWebView
        // exists (it's created during viewDidLoad, after the VC is returned), so
        // defer to the next runloop tick.
        DispatchQueue.main.async {
            context.coordinator.attach(to: vc)
        }
        return vc
    }

    func updateUIViewController(_ uiViewController: CAPBridgeViewController, context: Context) {
        WtTransportPlugin.proxyManager = proxyManager
    }

    // Ground-truth instrumentation: logs navigation success/failure and forwards
    // the page's console.log to the native unified log (iOS does not surface
    // WKWebView console/asset traffic otherwise).
    final class Coordinator: NSObject, WKNavigationDelegate, WKScriptMessageHandler {
        private weak var webView: WKWebView?

        func attach(to vc: UIViewController) {
            guard let wv = Coordinator.findWebView(in: vc.view) else {
                CapacitorWebView.log.error("attach: no WKWebView found in VC view tree")
                return
            }
            webView = wv
            wv.navigationDelegate = self
            let frame = wv.frame
            CapacitorWebView.log.info("attach: WKWebView frame=\(NSCoder.string(for: frame), privacy: .public) url=\(wv.url?.absoluteString ?? "nil", privacy: .public)")
            let src = "(function(){var o=console.log;console.log=function(){try{window.webkit.messageHandlers.wtlog.postMessage(Array.from(arguments).join(' '))}catch(e){};o.apply(console,arguments)};window.addEventListener('error',function(e){try{window.webkit.messageHandlers.wtlog.postMessage('JSERR: '+e.message+' @ '+e.filename+':'+e.lineno)}catch(_){}});})();"
            let script = WKUserScript(source: src, injectionTime: .atDocumentStart, forMainFrameOnly: false)
            wv.configuration.userContentController.addUserScript(script)
            wv.configuration.userContentController.add(self, name: "wtlog")
        }

        static func findWebView(in view: UIView) -> WKWebView? {
            if let wv = view as? WKWebView { return wv }
            for sub in view.subviews {
                if let found = findWebView(in: sub) { return found }
            }
            return nil
        }

        func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
            CapacitorWebView.log.info("didFinish url=\(webView.url?.absoluteString ?? "nil", privacy: .public) frame=\(NSCoder.string(for: webView.frame), privacy: .public)")
        }
        func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
            CapacitorWebView.log.error("didFailProvisional: \(error.localizedDescription, privacy: .public)")
        }
        func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
            CapacitorWebView.log.error("didFail: \(error.localizedDescription, privacy: .public)")
        }
        func userContentController(_ ucc: WKUserContentController, didReceive message: WKScriptMessage) {
            CapacitorWebView.log.info("JS: \(String(describing: message.body), privacy: .public)")
        }
    }
}
