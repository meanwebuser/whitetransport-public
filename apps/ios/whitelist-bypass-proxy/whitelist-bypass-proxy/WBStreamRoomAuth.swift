import Foundation
import Security
import UIKit
import WebKit

/// Provider-owned session captured only by the local iOS app. It is never
/// returned to Capacitor, written to a browser profile, or copied into an App
/// Group container.
struct WBStreamRoomSession: Codable, Equatable {
    let accessToken: String
    let cookieHeader: String
}

enum WBStreamRoomAuthPolicy {
    static let loginURL = URL(string: "https://stream.wb.ru/login")!

    private static let allowedHosts: Set<String> = ["stream.wb.ru", "wb.ru", "wildberries.ru"]
    private static let capturedCookieNames: Set<String> = ["x_wbaas_token", "wbx-validation-key", "_wbauid"]

    static func isAllowedNavigation(_ url: URL) -> Bool {
        guard url.scheme?.lowercased() == "https", let host = url.host?.lowercased() else { return false }
        return allowedHosts.contains(host)
    }

    static func cookieHeader(from cookies: [HTTPCookie]) -> String? {
        let values = cookies
            .filter { cookie in
                guard let host = URL(string: "https://" + cookie.domain.trimmingCharacters(in: CharacterSet(charactersIn: ".")))?.host else {
                    return false
                }
                return isAllowedNavigation(URL(string: "https://" + host)!) && capturedCookieNames.contains(cookie.name)
            }
            .sorted { $0.name < $1.name }
            .map { "\($0.name)=\($0.value)" }
        return values.isEmpty ? nil : values.joined(separator: "; ")
    }

    static func accessToken(fromJavaScriptResult result: Any?) -> String? {
        guard let raw = result as? String else { return nil }
        if let json = raw.data(using: .utf8),
           let object = try? JSONSerialization.jsonObject(with: json),
           let token = findAccessToken(in: object) {
            return token
        }

        let pattern = #"\\?\"accessToken\\?\"\s*:\s*\\?\"([^\"\\]+)"#
        guard let regex = try? NSRegularExpression(pattern: pattern),
              let match = regex.firstMatch(in: raw, range: NSRange(raw.startIndex..., in: raw)),
              let range = Range(match.range(at: 1), in: raw) else {
            return nil
        }
        return String(raw[range]).trimmingCharacters(in: .whitespacesAndNewlines).nilIfEmpty
    }

    private static func findAccessToken(in value: Any) -> String? {
        if let dictionary = value as? [String: Any] {
            if let token = (dictionary["accessToken"] as? String)?.trimmingCharacters(in: .whitespacesAndNewlines).nilIfEmpty {
                return token
            }
            return dictionary.values.compactMap(findAccessToken).first
        }
        if let array = value as? [Any] {
            return array.compactMap(findAccessToken).first
        }
        if let quoted = value as? String,
           let json = quoted.data(using: .utf8),
           let nested = try? JSONSerialization.jsonObject(with: json) {
            return findAccessToken(in: nested)
        }
        return nil
    }
}

private extension String {
    var nilIfEmpty: String? { isEmpty ? nil : self }
}

final class WBStreamRoomSessionStore {
    private let service = "com.whitetransport.room-auth"
    private let account = "wbstream-client-session"

    func hasSession() -> Bool { load() != nil }

    func load() -> WBStreamRoomSession? {
        var query = baseQuery
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &result) == errSecSuccess,
              let data = result as? Data else {
            return nil
        }
        return try? JSONDecoder().decode(WBStreamRoomSession.self, from: data)
    }

    func save(_ session: WBStreamRoomSession) throws {
        let data = try JSONEncoder().encode(session)
        let attributes: [String: Any] = [
            kSecValueData as String: data,
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
        ]
        let updateStatus = SecItemUpdate(baseQuery as CFDictionary, attributes as CFDictionary)
        if updateStatus == errSecSuccess { return }
        guard updateStatus == errSecItemNotFound else { throw KeychainError.status(updateStatus) }

        var add = baseQuery
        attributes.forEach { add[$0.key] = $0.value }
        let addStatus = SecItemAdd(add as CFDictionary, nil)
        guard addStatus == errSecSuccess else { throw KeychainError.status(addStatus) }
    }

    private var baseQuery: [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
    }

    private enum KeychainError: Error {
        case status(OSStatus)
    }
}

/// A deliberately constrained provider login surface. Its non-persistent
/// WebKit storage is used only while the user is completing the current login.
final class WBStreamRoomAuthViewController: UIViewController, WKNavigationDelegate {
    private let store: WBStreamRoomSessionStore
    private let completion: (Result<WBStreamRoomSession, Error>) -> Void
    private let webView: WKWebView
    private var completing = false

    init(store: WBStreamRoomSessionStore = WBStreamRoomSessionStore(), completion: @escaping (Result<WBStreamRoomSession, Error>) -> Void) {
        self.store = store
        self.completion = completion
        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = WKWebsiteDataStore.nonPersistent()
        configuration.preferences.javaScriptCanOpenWindowsAutomatically = false
        self.webView = WKWebView(frame: .zero, configuration: configuration)
        super.init(nibName: nil, bundle: nil)
    }

    required init?(coder: NSCoder) { nil }

    override func viewDidLoad() {
        super.viewDidLoad()
        title = "Вход WB Stream"
        view.backgroundColor = .systemBackground
        webView.navigationDelegate = self
        webView.allowsBackForwardNavigationGestures = false
        webView.translatesAutoresizingMaskIntoConstraints = false
        view.addSubview(webView)
        NSLayoutConstraint.activate([
            webView.leadingAnchor.constraint(equalTo: view.leadingAnchor),
            webView.trailingAnchor.constraint(equalTo: view.trailingAnchor),
            webView.topAnchor.constraint(equalTo: view.safeAreaLayoutGuide.topAnchor),
            webView.bottomAnchor.constraint(equalTo: view.bottomAnchor),
        ])
        navigationItem.rightBarButtonItem = UIBarButtonItem(title: "Готово", style: .done, target: self, action: #selector(confirmSession))
        webView.load(URLRequest(url: WBStreamRoomAuthPolicy.loginURL))
    }

    @objc private func confirmSession() { captureSessionIfReady(showError: true) }

    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction, decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        guard let url = navigationAction.request.url, WBStreamRoomAuthPolicy.isAllowedNavigation(url) else {
            decisionHandler(.cancel)
            return
        }
        decisionHandler(.allow)
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        guard let url = webView.url, WBStreamRoomAuthPolicy.isAllowedNavigation(url) else { return }
        captureSessionIfReady(showError: false)
    }

    private func captureSessionIfReady(showError: Bool) {
        guard !completing else { return }
        webView.configuration.websiteDataStore.httpCookieStore.getAllCookies { [weak self] cookies in
            DispatchQueue.main.async {
                guard let self, let cookieHeader = WBStreamRoomAuthPolicy.cookieHeader(from: cookies) else {
                    if showError { self?.showMissingSessionMessage() }
                    return
                }
                self.webView.evaluateJavaScript("(function(){try{return localStorage.getItem('wb_auth_auth_slice')||''}catch(_){return ''}})()") { value, _ in
                    guard let token = WBStreamRoomAuthPolicy.accessToken(fromJavaScriptResult: value) else {
                        if showError { self.showMissingSessionMessage() }
                        return
                    }
                    self.completing = true
                    do {
                        let session = WBStreamRoomSession(accessToken: token, cookieHeader: cookieHeader)
                        try self.store.save(session)
                        self.completion(.success(session))
                        self.dismiss(animated: true)
                    } catch {
                        self.completion(.failure(error))
                        self.completing = false
                    }
                }
            }
        }
    }

    private func showMissingSessionMessage() {
        let alert = UIAlertController(title: "Вход ещё не завершён", message: "Завершите вход в WB Stream и повторите попытку.", preferredStyle: .alert)
        alert.addAction(UIAlertAction(title: "OK", style: .default))
        present(alert, animated: true)
    }
}
