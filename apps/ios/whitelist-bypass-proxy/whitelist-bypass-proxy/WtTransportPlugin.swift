import Foundation
import Combine
import Capacitor

// iOS side of the single native bridge between the React client UI
// (apps/client-web) and the existing transport layer (ProxyManager →
// gomobile Mobile framework / PacketTunnel). Mirrors the Android
// WtTransportPlugin.kt and the same JS contract (apps/client-web/.../wt-transport.ts).
//
// SCOPE (PoC): proves bidirectional bridge plumbing — the WebView can command
// ProxyManager and receive its @Published status/log stream. It does NOT carry
// traffic in the Simulator: the PacketTunnel Network Extension cannot run there,
// and ProxyManager.connect() needs a callUrl produced by the WebView join/VK-auth
// flow (post-PoC, same as Android's startJoinFor). So connect() proves
// "button → ProxyManager.connect → status streams back", not a working tunnel.
@objc(WtTransportPlugin)
public class WtTransportPlugin: CAPPlugin, CAPBridgedPlugin {
    public let identifier = "WtTransportPlugin"
    public let jsName = "WtTransport"
    public let pluginMethods: [CAPPluginMethod] = [
        CAPPluginMethod(name: "getStatus", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "connect", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "disconnect", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "setMode", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "getSocksInfo", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "scanRooms", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "beginRoomAuth", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "getRoomAuthStatus", returnType: CAPPluginReturnPromise),
    ]

    // Shared ProxyManager — the same instance ContentView used via
    // @EnvironmentObject. Set by the app entry when wiring the bridge
    // (see whitelist_bypass_proxyApp.swift). Weak to avoid a retain cycle.
    // Not public: ProxyManager is an internal type; only wired within the module.
    static weak var proxyManager: ProxyManager?

    private var cancellables = Set<AnyCancellable>()
    private var roomAuthState = "not_ready"
    private var roomAuthMessage = "Войдите в WB Stream для подключения комнаты."

    override public func load() {
        guard let pm = WtTransportPlugin.proxyManager else { return }
        // Forward ProxyManager's @Published status/logs to JS, mirroring the
        // Android TunnelServiceState.vpnStatusCallback → notifyListeners pattern.
        pm.$status
            .receive(on: DispatchQueue.main)
            .sink { [weak self] status in
                self?.notifyListeners("statusChanged", data: [
                    "status": status.rawValue,
                    "active": status == .tunnelConnected,
                ])
            }
            .store(in: &cancellables)

        pm.$logs
            .receive(on: DispatchQueue.main)
            .sink { [weak self] logs in
                if let last = logs.last {
                    self?.notifyListeners("log", data: ["message": last])
                }
            }
            .store(in: &cancellables)
    }

    @objc func getStatus(_ call: CAPPluginCall) {
        let pm = WtTransportPlugin.proxyManager
        call.resolve([
            "active": pm?.isRunning ?? false,
            "mode": (pm?.isRunning ?? false) ? "proxy" : "off",
        ])
    }

    @objc func connect(_ call: CAPPluginCall) {
        // PoC: drives ProxyManager.connect(). It reads callUrl (normally set by
        // the WebView join flow); without one it errors, but that status streams
        // back — which is exactly what proves the bridge. Real connect = join port.
        DispatchQueue.main.async {
            WtTransportPlugin.proxyManager?.connect()
            call.resolve()
        }
    }

    @objc func disconnect(_ call: CAPPluginCall) {
        DispatchQueue.main.async {
            WtTransportPlugin.proxyManager?.disconnect()
            call.resolve()
        }
    }

    @objc func setMode(_ call: CAPPluginCall) {
        // tunnel/off switching needs the join + PacketTunnel (post-PoC). Accept
        // so the UI mode toggle resolves.
        call.resolve()
    }

    @objc func getSocksInfo(_ call: CAPPluginCall) {
        let port = WtTransportPlugin.proxyManager?.socksPort ?? 1080
        call.resolve(["host": "127.0.0.1", "port": port])
    }

    @objc func scanRooms(_ call: CAPPluginCall) {
        // Discovery lives in the join orchestration (post-PoC). No-op for now.
        call.resolve()
    }

    @objc func beginRoomAuth(_ call: CAPPluginCall) {
        DispatchQueue.main.async { [weak self] in
            guard let presenter = self?.bridge?.viewController else {
                call.reject("WhiteTransport UI is not ready for room authentication")
                return
            }
            let controller = WBStreamRoomAuthViewController { [weak self] result in
                switch result {
                case .success(let session):
                    do {
                        try WTCoreRoomFirstRuntime.startIfProvisioned(session: session)
                        self?.roomAuthState = "ready"
                        self?.roomAuthMessage = "Локальная сессия WB Stream передана транспортному ядру."
                    } catch {
                        self?.roomAuthState = "error"
                        self?.roomAuthMessage = "Вход завершён, но транспортное ядро пока не готово."
                    }
                case .failure:
                    self?.roomAuthState = "error"
                    self?.roomAuthMessage = "Не удалось сохранить локальную сессию WB Stream."
                }
            }
            let navigation = UINavigationController(rootViewController: controller)
            navigation.modalPresentationStyle = .fullScreen
            presenter.present(navigation, animated: true)
            call.resolve(["opened": true])
        }
    }

    @objc func getRoomAuthStatus(_ call: CAPPluginCall) {
        let hasSession = WBStreamRoomSessionStore().hasSession()
        call.resolve([
            "ready": hasSession && roomAuthState == "ready",
            "state": hasSession ? roomAuthState : "not_ready",
            "message": hasSession ? roomAuthMessage : "Войдите в WB Stream для подключения комнаты.",
        ])
    }
}
