import AppKit
import Foundation
import WebKit

private let loginURL = URL(string: "https://stream.wb.ru/login")!
private let allowedHosts: Set<String> = ["stream.wb.ru", "wb.ru", "wildberries.ru"]
private let requiredCookieNames: Set<String> = ["x_wbaas_token", "wbx-validation-key", "_wbauid"]

@main
final class RoomAuthHelper: NSObject, NSApplicationDelegate, WKNavigationDelegate {
    private var window: NSWindow!
    private var webView: WKWebView!
    private var completed = false

    static func main() {
        let app = NSApplication.shared
        let delegate = RoomAuthHelper()
        app.delegate = delegate
        app.setActivationPolicy(.regular)
        app.run()
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = WKWebsiteDataStore.nonPersistent()
        configuration.preferences.javaScriptCanOpenWindowsAutomatically = false

        webView = WKWebView(frame: .zero, configuration: configuration)
        webView.navigationDelegate = self
        webView.allowsBackForwardNavigationGestures = true

        window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 960, height: 700),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = "WhiteTransport — вход WB Stream"
        window.contentView = webView
        window.center()
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        webView.load(URLRequest(url: loginURL))
    }

    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction, decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        guard let url = navigationAction.request.url, isAllowed(url) else {
            decisionHandler(.cancel)
            return
        }
        decisionHandler(.allow)
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        guard let url = webView.url, isAllowed(url), !completed else { return }
        captureIfReady()
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        writeError("provider navigation failed: \((error as NSError).code)")
    }

    private func isAllowed(_ url: URL) -> Bool {
        url.scheme?.lowercased() == "https" && allowedHosts.contains(url.host?.lowercased() ?? "")
    }

    private func captureIfReady() {
        webView.configuration.websiteDataStore.httpCookieStore.getAllCookies { [weak self] cookies in
            guard let self, !self.completed else { return }
            let cookieHeader = cookies
                .filter { requiredCookieNames.contains($0.name) && allowedHosts.contains($0.domain.trimmingCharacters(in: CharacterSet(charactersIn: ".")).lowercased()) }
                .map { "\($0.name)=\($0.value)" }
                .joined(separator: "; ")
            guard !cookieHeader.isEmpty else { return }
            self.webView.evaluateJavaScript("localStorage.getItem('wb_auth_auth_slice') || ''") { value, _ in
                guard let raw = value as? String,
                      let data = raw.data(using: .utf8),
                      let auth = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
                      let accessToken = (auth["accessToken"] as? String)?.trimmingCharacters(in: .whitespacesAndNewlines),
                      !accessToken.isEmpty else { return }
                self.complete(accessToken: accessToken, cookieHeader: cookieHeader)
            }
        }
    }

    private func complete(accessToken: String, cookieHeader: String) {
        guard !completed else { return }
        completed = true
        let payload: [String: String] = [
            "platform": "wbstream",
            "access_token": accessToken,
            "cookie_header": cookieHeader,
        ]
        guard let data = try? JSONSerialization.data(withJSONObject: payload) else {
            writeError("failed to encode local room session")
            return
        }
        FileHandle.standardOutput.write(data)
        FileHandle.standardOutput.write(Data([0x0a]))
        NSApp.terminate(nil)
    }

    private func writeError(_ message: String) {
        FileHandle.standardError.write(Data((message + "\n").utf8))
    }
}
