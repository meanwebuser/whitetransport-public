import Foundation
import UIKit
import Combine
import Mobile
import os


enum ProxyStatus: String {
    case idle = "IDLE"
    case ready = "READY"
    case connecting = "CONNECTING"
    case reconnecting = "RECONNECTING"
    case tunnelConnected = "TUNNEL_CONNECTED"
    case tunnelLost = "TUNNEL_LOST"
    case error = "ERROR"

    var displayLabel: String {
        switch self {
        case .idle: return NSLocalizedString("status_idle", comment: "")
        case .ready: return NSLocalizedString("status_ready", comment: "")
        case .connecting: return NSLocalizedString("status_connecting", comment: "")
        case .reconnecting: return NSLocalizedString("status_connecting", comment: "")
        case .tunnelConnected: return NSLocalizedString("status_tunnel_active", comment: "")
        case .tunnelLost: return NSLocalizedString("status_tunnel_lost", comment: "")
        case .error: return NSLocalizedString("status_error", comment: "")
        }
    }
}

enum SocksAuthMode: String, CaseIterable {
    case none = "NONE"
    case auto = "AUTO"
    case manual = "MANUAL"
}

enum DnsMode: String, CaseIterable {
    case system = "SYSTEM"
    case custom = "CUSTOM"
}

class HeadlessCallbackBridge: NSObject, IosHeadlessCallbackProtocol {
    weak var manager: ProxyManager?

    func onLog(_ msg: String?) {
        guard let msg = msg else { return }
        print("[GO] \(msg)")
        let mgr = manager
        DispatchQueue.main.async { [weak mgr] in
            mgr?.appendLog(msg)
        }
    }

    func onStatus(_ status: String?) {
        guard let status = status else { return }
        print("[STATUS] \(status)")
        let mgr = manager
        DispatchQueue.main.async {
            mgr?.handleStatus(status)
        }
    }

    func saveCache(_ key: String?, value: String?) {
        guard let key = key, let value = value else { return }
        UserDefaults.standard.set(value, forKey: "cache_\(key)")
    }

    func loadCache(_ key: String?) -> String {
        guard let key = key else { return "" }
        return UserDefaults.standard.string(forKey: "cache_\(key)") ?? ""
    }

    func clearCache(_ key: String?) {
        guard let key = key else { return }
        UserDefaults.standard.removeObject(forKey: "cache_\(key)")
    }

    func resolveHost(_ hostname: String?) -> String {
        guard let hostname = hostname else { return "" }
        var result = ""
        let host = CFHostCreateWithName(nil, hostname as CFString).takeRetainedValue()
        CFHostStartInfoResolution(host, .addresses, nil)
        if let addresses = CFHostGetAddressing(host, nil)?.takeUnretainedValue() as? [Data] {
            for addressData in addresses {
                var storage = sockaddr_storage()
                addressData.withUnsafeBytes { buffer in
                    if let baseAddress = buffer.baseAddress {
                        memcpy(&storage, baseAddress, min(addressData.count, MemoryLayout<sockaddr_storage>.size))
                    }
                }
                if storage.ss_family == UInt8(AF_INET) {
                    var addr = sockaddr_in()
                    addressData.withUnsafeBytes { buffer in
                        if let baseAddress = buffer.baseAddress {
                            memcpy(&addr, baseAddress, MemoryLayout<sockaddr_in>.size)
                        }
                    }
                    var ipString = [CChar](repeating: 0, count: Int(INET_ADDRSTRLEN))
                    var inAddr = addr.sin_addr
                    inet_ntop(AF_INET, &inAddr, &ipString, socklen_t(INET_ADDRSTRLEN))
                    result = String(cString: ipString)
                    break
                }
            }
        }
        return result
    }
}

enum TunnelMode: String, CaseIterable {
    case dc = "dc"
    case video = "video"

    var label: String {
        switch self {
        case .dc: return "DC"
        case .video: return "Video"
        }
    }
}

enum CallPlatform: String {
    case vk = "vk"
    case telemost = "telemost"
    case wbstream = "wbstream"
    case dion = "dion"

    static let wbstreamPrefix = "wbstream://"
    static let wbstreamRoomURLInfix = "stream.wb.ru/room/"
    static let dionPrefix = "dion://"
    static let dionEventInfix = "dion.vc/event/"

    static func normalize(url: String) -> String {
        let trimmed = url.trimmingCharacters(in: .whitespacesAndNewlines)
        if let roomId = extractWBStreamRoomId(from: trimmed) {
            return "\(wbstreamPrefix)\(roomId)"
        }
        return trimmed
    }

    static func detect(url: String) -> CallPlatform {
        let normalized = normalize(url: url)
        if normalized.hasPrefix(dionPrefix) || normalized.contains(dionEventInfix) {
            return .dion
        }
        if normalized.hasPrefix(wbstreamPrefix) {
            return .wbstream
        }
        if normalized.contains("telemost.yandex") {
            return .telemost
        }
        return .vk
    }

    static func extractRoomId(url: String) -> String {
        let trimmed = normalize(url: url)
        if trimmed.hasPrefix(wbstreamPrefix) {
            return String(trimmed.dropFirst(wbstreamPrefix.count)).trimmingCharacters(in: .whitespacesAndNewlines)
        }
        if trimmed.hasPrefix(dionPrefix) {
            return String(trimmed.dropFirst(dionPrefix.count)).trimmingCharacters(in: .whitespacesAndNewlines)
        }
        if let range = trimmed.range(of: dionEventInfix) {
            var slug = String(trimmed[range.upperBound...])
            if let qmark = slug.firstIndex(of: "?") { slug = String(slug[..<qmark]) }
            if let hash = slug.firstIndex(of: "#") { slug = String(slug[..<hash]) }
            if let slash = slug.firstIndex(of: "/") { slug = String(slug[..<slash]) }
            return slug.trimmingCharacters(in: .whitespacesAndNewlines)
        }
        return trimmed
    }

    private static func extractWBStreamRoomId(from url: String) -> String? {
        guard let range = url.range(of: wbstreamRoomURLInfix) else { return nil }
        var roomId = String(url[range.upperBound...])
        if let qmark = roomId.firstIndex(of: "?") { roomId = String(roomId[..<qmark]) }
        if let hash = roomId.firstIndex(of: "#") { roomId = String(roomId[..<hash]) }
        if let slash = roomId.firstIndex(of: "/") { roomId = String(roomId[..<slash]) }
        roomId = roomId.trimmingCharacters(in: .whitespacesAndNewlines)
        return roomId.isEmpty ? nil : roomId
    }
}

class ProxyManager: ObservableObject {
    @Published var status: ProxyStatus = .idle
    @Published var errorMessage: String = ""
    @Published var logs: [String] = []
    @Published var isRunning: Bool = false
    @Published var toastMessage: String?
    @Published var statusText: String?
    @Published var vpnStatusText: String = "VPN: not configured"
    @Published var vpnAvailable: Bool = SystemVPNManager.shared.isPacketTunnelBundled
    @Published var discoveryEnabled: Bool = AppDefaults.discoveryEnabled { didSet { AppDefaults.discoveryEnabled = discoveryEnabled } }
    @Published var telemetryEnabled: Bool = AppDefaults.telemetryEnabled { didSet { AppDefaults.telemetryEnabled = telemetryEnabled } }
    @Published var discoveryStatus: String = NSLocalizedString("discovery_idle", comment: "")
    @Published var discoveredFreeCount: Int = 0
    @Published var lastDiscoverySource: String = ""
    @Published var roomWarmupSummary: String = "Rooms: checking…"
    @Published var roomWarmupRefreshing: Bool = false
    @Published var telegramCheckStatus: String = ""
    var detectedPlatform: CallPlatform = .vk
    private let discoveryScanner = VKDiscoveryScanner()
    private let discoveryLogger = Logger(subsystem: "bypass.whitelist", category: "discovery")

    @Published var callUrl: String = AppDefaults.lastUrl {
        didSet {
            let normalized = CallPlatform.normalize(url: callUrl)
            if normalized != callUrl {
                callUrl = normalized
                return
            }
            AppDefaults.lastUrl = callUrl
        }
    }
    @Published var socksPort: Int = AppDefaults.socksPort { didSet { AppDefaults.socksPort = socksPort } }
    @Published var tunnelMode: TunnelMode = AppDefaults.tunnelMode { didSet { AppDefaults.tunnelMode = tunnelMode } }
    @Published var displayName: String = AppDefaults.displayName { didSet { AppDefaults.displayName = displayName } }
    @Published var showLogs: Bool = AppDefaults.showLogs { didSet { AppDefaults.showLogs = showLogs } }
    @Published var socksAuthMode: SocksAuthMode = AppDefaults.socksAuthMode { didSet { AppDefaults.socksAuthMode = socksAuthMode } }
    @Published var manualSocksUser: String = AppDefaults.socksUser { didSet { AppDefaults.socksUser = manualSocksUser } }
    @Published var manualSocksPass: String = AppDefaults.socksPass { didSet { AppDefaults.socksPass = manualSocksPass } }
    @Published var vp8Fps: Int = AppDefaults.vp8Fps { didSet { AppDefaults.vp8Fps = vp8Fps } }
    @Published var vp8Batch: Int = AppDefaults.vp8Batch { didSet { AppDefaults.vp8Batch = vp8Batch } }
    @Published var dualTrack: Bool = AppDefaults.dualTrack { didSet { AppDefaults.dualTrack = dualTrack } }

    private let autoSocksUser: String
    private let autoSocksPass: String
    private var callbackBridge: HeadlessCallbackBridge?
    private let backgroundKeepAlive = BackgroundKeepAlive()

    private var pendingLogs: [String] = []
    private var logFlushScheduled = false
    private var staleRoomAutoRescanCount = 0
    private var badDiscoveryRooms: Set<String> = []
    // Cache of the last discovery scan's free rooms (read in connect() at :302,
    // written in scanRooms() at :469). Was referenced but never declared in the
    // monorepo import (commit 86b54a0); restored here as [DiscoveryRoom] to match
    // its write site (`= freeRooms`, a filtered [DiscoveryRoom]).
    private var cachedDiscoveryRooms: [DiscoveryRoom] = []
    // Debounce flag: true once a "request_room" event was sent for an empty free
    // pool, reset when free rooms appear (:482-490). Referenced but never declared
    // in the monorepo import (commit 86b54a0); restored as Bool to match its use.
    private var roomRequestSentForEmptyPool = false
    private var currentDiscoveryRoom: String?
    private let clientId = ProxyManager.stableClientId()

    private static func stableClientId() -> String {
        let key = "wt.discoveryClientId"
        if let existing = UserDefaults.standard.string(forKey: key), !existing.isEmpty { return existing }
        let generated = "ios-" + UUID().uuidString
        UserDefaults.standard.set(generated, forKey: key)
        return generated
    }

    var activeSocksUser: String {
        switch socksAuthMode {
        case .none: return ""
        case .manual: return manualSocksUser
        case .auto: return autoSocksUser
        }
    }

    var activeSocksPass: String {
        switch socksAuthMode {
        case .none: return ""
        case .manual: return manualSocksPass
        case .auto: return autoSocksPass
        }
    }

    var socksAuthEnabled: Bool { socksAuthMode != .none }

    init() {
        let chars = "abcdefghijklmnopqrstuvwxyz0123456789"
        autoSocksUser = String((0..<16).map { _ in chars.randomElement()! })
        autoSocksPass = String((0..<24).map { _ in chars.randomElement()! })
    }

    var socksUrl: String {
        if socksAuthEnabled {
            return "socks5://\(activeSocksUser):\(activeSocksPass)@127.0.0.1:\(socksPort)"
        }
        return "socks5://127.0.0.1:\(socksPort)"
    }

    private func isPortAvailable(_ port: Int) -> Bool {
        let socketFD = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP)
        if socketFD == -1 { return false }
        defer { close(socketFD) }

        var addr = sockaddr_in()
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = in_port_t(port).bigEndian
        addr.sin_addr.s_addr = INADDR_ANY

        let result = withUnsafePointer(to: &addr) { addrPtr in
            addrPtr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockaddrPtr in
                bind(socketFD, sockaddrPtr, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        return result == 0
    }

    func connect() {
        let normalizedCallUrl = CallPlatform.normalize(url: callUrl)
        if normalizedCallUrl != callUrl {
            callUrl = normalizedCallUrl
            appendLog("Normalized link: \(normalizedCallUrl)")
        }
        guard !callUrl.isEmpty else {
            if let cached = cachedDiscoveryRooms.first(where: { !badDiscoveryRooms.contains($0.room) }) {
                appendLog("Discovery connect uses prewarmed room: \(cached.displayName)")
                useDiscoveryRoom(cached, connectNow: true)
                return
            }
            scanAndConnect()
            return
        }

        if !isPortAvailable(socksPort) {
            let originalPort = socksPort
            let ranges: [ClosedRange<Int>] = [
                originalPort...min(originalPort + 100, 65535),
                1080...1380,
                8080...8380,
                9080...9380,
                49152...65535
            ]
            var foundPort = false
            for range in ranges {
                for candidatePort in range {
                    if isPortAvailable(candidatePort) {
                        socksPort = candidatePort
                        foundPort = true
                        break
                    }
                }
                if foundPort { break }
            }
            if !foundPort {
                let socketFD = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP)
                if socketFD != -1 {
                    var addr = sockaddr_in()
                    addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
                    addr.sin_family = sa_family_t(AF_INET)
                    addr.sin_port = 0
                    addr.sin_addr.s_addr = in_addr_t(INADDR_LOOPBACK).bigEndian
                    let bound = withUnsafePointer(to: &addr) { addrPtr in
                        addrPtr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockaddrPtr in
                            bind(socketFD, sockaddrPtr, socklen_t(MemoryLayout<sockaddr_in>.size))
                        }
                    }
                    if bound == 0 {
                        var boundAddr = sockaddr_in()
                        var addrLen = socklen_t(MemoryLayout<sockaddr_in>.size)
                        withUnsafeMutablePointer(to: &boundAddr) { ptr in
                            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockPtr in
                                getsockname(socketFD, sockPtr, &addrLen)
                            }
                        }
                        socksPort = Int(UInt16(bigEndian: boundAddr.sin_port))
                    }
                    close(socketFD)
                }
            }
            appendLog("Port \(originalPort) busy, using \(socksPort)")
        }

        logs.removeAll()
        pendingLogs.removeAll()
        badDiscoveryRooms.removeAll()
        currentDiscoveryRoom = nil
        staleRoomAutoRescanCount = 0
        errorMessage = ""
        status = .idle
        isRunning = true

        let bridge = HeadlessCallbackBridge()
        bridge.manager = self
        callbackBridge = bridge

        backgroundKeepAlive.start()
        detectedPlatform = CallPlatform.detect(url: callUrl)
        appendLog("Platform: \(detectedPlatform.rawValue)")

        if tunnelMode == .dc && (detectedPlatform == .telemost || detectedPlatform == .dion) {
            tunnelMode = .video
            showToast(NSLocalizedString("dc_mode_not_supported", comment: ""))
        }

        switch detectedPlatform {
        case .telemost:
            IosStartTelemostHeadless(socksPort, activeSocksUser, activeSocksPass, bridge)
            appendLog("Started Telemost headless joiner")
            let joinParams: [String: Any] = [
                "joinLink": callUrl,
                "displayName": displayName,
                "vp8Fps": vp8Fps,
                "vp8Batch": vp8Batch,
            ]
            if let jsonData = try? JSONSerialization.data(withJSONObject: joinParams),
               let jsonString = String(data: jsonData, encoding: .utf8) {
                IosSendJoinParams(jsonString)
                appendLog("Sent join params")
            }

        case .vk:
            IosStartVKHeadless(socksPort, activeSocksUser, activeSocksPass, callUrl, displayName, tunnelMode.rawValue, vp8Fps, vp8Batch, bridge)
            appendLog("Started VK headless joiner")

        case .wbstream:
            IosStartWBStreamHeadless(socksPort, activeSocksUser, activeSocksPass, bridge)
            appendLog("Started WB Stream headless joiner")
            let joinParams: [String: Any] = [
                "roomId": CallPlatform.extractRoomId(url: callUrl),
                "displayName": displayName,
                "tunnelMode": tunnelMode.rawValue,
                "vp8Fps": vp8Fps,
                "vp8Batch": vp8Batch,
                "dualTrack": dualTrack,
            ]
            if let jsonData = try? JSONSerialization.data(withJSONObject: joinParams),
               let jsonString = String(data: jsonData, encoding: .utf8) {
                IosSendJoinParams(jsonString)
                appendLog("Sent join params")
            }

        case .dion:
            IosStartDionHeadless(socksPort, activeSocksUser, activeSocksPass, bridge)
            appendLog("Started DION headless joiner")
            let joinParams: [String: Any] = [
                "roomId": CallPlatform.extractRoomId(url: callUrl),
                "displayName": displayName,
                "vp8Fps": vp8Fps,
                "vp8Batch": vp8Batch,
            ]
            if let jsonData = try? JSONSerialization.data(withJSONObject: joinParams),
               let jsonString = String(data: jsonData, encoding: .utf8) {
                IosSendJoinParams(jsonString)
                appendLog("Sent join params")
            }
        }
    }


    func prewarmRooms(reason: String = "lazy") {
        guard !isRunning else { return }
        scanRooms(reason: reason, connectWhenReady: false, force: false)
    }

    func refreshRooms() {
        scanRooms(reason: "manual-refresh", connectWhenReady: false, force: true)
    }

    func scanAndConnect() {
        scanRooms(reason: "connect", connectWhenReady: true, force: true)
    }

    private func scanRooms(reason: String, connectWhenReady: Bool, force: Bool) {
        guard discoveryEnabled else {
            showToast(NSLocalizedString("discovery_disabled", comment: ""))
            return
        }
        if roomWarmupRefreshing && !force { return }
        roomWarmupRefreshing = true
        discoveryStatus = NSLocalizedString("discovery_scanning", comment: "")
        roomWarmupSummary = "Rooms: updating…"
        statusText = discoveryStatus
        appendLog("Discovery scan started reason=\(reason) connectWhenReady=\(connectWhenReady)")
        discoveryScanner.scan { [weak self] rooms, source in
            DispatchQueue.main.async {
                guard let self = self else { return }
                self.roomWarmupRefreshing = false
                self.lastDiscoverySource = source ?? ""
                let allFreeRooms = rooms.filter { $0.isFree }
                let freeRooms = allFreeRooms.filter { !self.badDiscoveryRooms.contains($0.room) }
                let ignored = allFreeRooms.count - freeRooms.count
                self.cachedDiscoveryRooms = freeRooms
                self.discoveredFreeCount = freeRooms.count
                let names = Array(Set(freeRooms.map { $0.displayName })).prefix(3).joined(separator: ", ")
                self.discoveryStatus = String(format: NSLocalizedString("discovery_found", comment: ""), freeRooms.count)
                self.roomWarmupSummary = freeRooms.isEmpty ? "Rooms: none free, requested new" : "Rooms: free \(freeRooms.count) · \(names)"
                self.statusText = self.roomWarmupSummary
                self.appendLog("Discovery found free rooms: \(freeRooms.count)" + (ignored > 0 ? " (ignored bad: \(ignored))" : "") + " source=\(source ?? "unknown")")
                guard let selected = freeRooms.first else {
                    if !self.roomRequestSentForEmptyPool {
                        self.roomRequestSentForEmptyPool = true
                        self.discoveryScanner.sendClientEvent(type: "request_room", clientId: self.clientId, room: nil, reason: "prewarm_no_free_rooms", badRooms: Array(self.badDiscoveryRooms)) { [weak self] ok in
                            self?.appendLog("Private-bus request_room sent: \(ok)")
                        }
                    }
                    return
                }
                self.roomRequestSentForEmptyPool = false
                if connectWhenReady { self.useDiscoveryRoom(selected, connectNow: true) }
            }
        }
    }

    private func useDiscoveryRoom(_ selected: DiscoveryRoom, connectNow: Bool) {
        currentDiscoveryRoom = selected.room
        callUrl = selected.room
        statusText = "Using \(selected.displayName)"
        appendLog("Discovery selected room: id=\(selected.id) node=\(selected.displayName)")
        if connectNow { connect() }
    }

    func checkTelegramThroughTunnel() {
        guard isRunning else {
            showToast(NSLocalizedString("telegram_check_needs_connection", comment: ""))
            return
        }
        telegramCheckStatus = NSLocalizedString("telegram_check_running", comment: "")
        appendLog("Telegram check started")
        let start = Date()
        var request = URLRequest(url: URL(string: "https://t.me/Kuplinov_Telegram/1032")!)
        request.timeoutInterval = 12
        request.setValue("BEZabotny-NET iOS", forHTTPHeaderField: "User-Agent")
        let config = URLSessionConfiguration.ephemeral
        // iOS does not expose SOCKS CFNetwork proxy keys. When the system VPN
        // profile is active, this request is routed by the OS tunnel; in proxy-only
        // mode it remains a direct reachability check.
        URLSession(configuration: config).dataTask(with: request) { [weak self] _, response, error in
            DispatchQueue.main.async {
                guard let self = self else { return }
                if let error = error {
                    self.telegramCheckStatus = "Telegram: \(error.localizedDescription)"
                    self.appendLog("Telegram check failed: \(error.localizedDescription)")
                    return
                }
                let ms = Int(Date().timeIntervalSince(start) * 1000)
                let code = (response as? HTTPURLResponse)?.statusCode ?? 0
                self.telegramCheckStatus = "Telegram HTTP \(code) · \(ms) ms"
                self.appendLog("Telegram check: HTTP \(code), \(ms) ms")
            }
        }.resume()
    }

    func disconnect() {
        callbackBridge?.manager = nil
        callbackBridge = nil
        IosStopCaptchaProxy()
        IosStopHeadless()
        backgroundKeepAlive.stop()
        isRunning = false
        status = .idle
        appendLog("Disconnected")
    }

    func resetAll() {
        disconnect()
        captchaURL = nil
        statusText = nil
        logs.removeAll()
        pendingLogs.removeAll()
        badDiscoveryRooms.removeAll()
        currentDiscoveryRoom = nil
        staleRoomAutoRescanCount = 0
        errorMessage = ""
        socksPort = 1080
    }

    @Published var captchaURL: String?

    func handleStatus(_ statusString: String) {
        if statusString.hasPrefix("ERROR:") {
            let errorText = String(statusString.dropFirst(6))
            status = .error
            errorMessage = errorText
            isRunning = false
            captchaURL = nil
            appendLog("ERROR: \(errorText)")
            if errorText.localizedCaseInsensitiveContains("guest cannot create room") {
                let badRoom = currentDiscoveryRoom ?? callUrl
                if !badRoom.isEmpty {
                    badDiscoveryRooms.insert(badRoom)
                    discoveryScanner.sendClientEvent(type: "bad_room", clientId: clientId, room: badRoom, reason: "guest_cannot_create_room", badRooms: Array(badDiscoveryRooms)) { [weak self] ok in
                        DispatchQueue.main.async { self?.appendLog("Private-bus bad_room sent: \(ok)") }
                    }
                }
                appendLog("Room looks stale; blacklisted locally and rescanning discovery")
                if discoveryEnabled && staleRoomAutoRescanCount < 2 {
                    staleRoomAutoRescanCount += 1
                    callUrl = ""
                    DispatchQueue.main.asyncAfter(deadline: .now() + 1.0) { [weak self] in
                        self?.scanAndConnect()
                    }
                }
            }
        } else if statusString.hasPrefix("CAPTCHA:") {
            captchaURL = String(statusString.dropFirst(8))
            statusText = NSLocalizedString("status_solve_captcha", comment: "")
            appendLog("Captcha: \(captchaURL ?? "")")
        } else {
            if captchaURL != nil && statusString != "CAPTCHA" {
                captchaURL = nil
                statusText = nil
            }
            status = ProxyStatus(rawValue: statusString) ?? .idle
            if statusString.localizedCaseInsensitiveContains("TUNNEL") || statusString.localizedCaseInsensitiveContains("READY") {
                staleRoomAutoRescanCount = 0
            }
            appendLog("Status: \(statusString)")
        }
    }

    func appendLog(_ message: String) {
        let timestamp = DateFormatter.localizedString(from: Date(), dateStyle: .none, timeStyle: .medium)
        pendingLogs.append("[\(timestamp)] \(message)")

        if !logFlushScheduled {
            logFlushScheduled = true
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) { [weak self] in
                self?.flushLogs()
            }
        }
        maybeSendTelemetryLog(message)
    }

    private func maybeSendTelemetryLog(_ message: String) {
        let lower = message.lowercased()
        let shouldSend = lower.contains("error")
            || lower.contains("failed")
            || lower.contains("no free")
            || lower.contains("bad_room")
            || lower.contains("private-bus request_room sent: false")
            || lower.contains("private-bus bad_room sent: false")
        guard shouldSend, telemetryEnabled else { return }
        discoveryScanner.sendTelemetry(
            clientId: clientId,
            level: (lower.contains("error") || lower.contains("failed") || lower.contains("false")) ? "error" : "info",
            event: "client_log",
            messageText: message,
            room: currentDiscoveryRoom ?? callUrl,
            meta: [
                "status": status.rawValue,
                "discovery_status": discoveryStatus,
                "free_count": discoveredFreeCount,
            ]
        ) { [weak self] ok in
            if !ok { self?.discoveryLogger.error("telemetry client_log send failed") }
        }
    }

    private func flushLogs() {
        logFlushScheduled = false
        guard !pendingLogs.isEmpty else { return }
        logs.append(contentsOf: pendingLogs)
        pendingLogs.removeAll()
        if logs.count > 100 {
            logs.removeFirst(logs.count - 100)
        }
    }

    func copyLogs() {
        flushLogs()
        let text = logs.isEmpty ? "(empty log)" : logs.joined(separator: "\n")
        UIPasteboard.general.string = text
        showToast(NSLocalizedString("toast_logs_copied", comment: ""))
    }


    func installSystemVPNProfile() {
        SystemVPNManager.shared.install(callURL: callUrl) { [weak self] result in
            DispatchQueue.main.async {
                switch result {
                case .success(let message):
                    self?.vpnStatusText = message
                    self?.appendLog(message)
                case .failure(let error):
                    self?.vpnStatusText = "VPN install error: \(error.localizedDescription)"
                    self?.appendLog("VPN install error: \(error.localizedDescription)")
                }
            }
        }
    }

    func startSystemVPN() {
        SystemVPNManager.shared.start(callURL: callUrl) { [weak self] result in
            DispatchQueue.main.async {
                switch result {
                case .success(let message):
                    self?.vpnStatusText = message
                    self?.appendLog(message)
                case .failure(let error):
                    self?.vpnStatusText = "VPN start error: \(error.localizedDescription)"
                    self?.appendLog("VPN start error: \(error.localizedDescription)")
                }
            }
        }
    }

    func stopSystemVPN() {
        SystemVPNManager.shared.stop { [weak self] result in
            DispatchQueue.main.async {
                switch result {
                case .success(let message):
                    self?.vpnStatusText = message
                    self?.appendLog(message)
                case .failure(let error):
                    self?.vpnStatusText = "VPN stop error: \(error.localizedDescription)"
                    self?.appendLog("VPN stop error: \(error.localizedDescription)")
                }
            }
        }
    }

    func refreshSystemVPNStatus() {
        vpnAvailable = SystemVPNManager.shared.isPacketTunnelBundled
        guard vpnAvailable else {
            vpnStatusText = "VPN extension is not included in this build"
            return
        }
        SystemVPNManager.shared.status { [weak self] status in
            DispatchQueue.main.async {
                self?.vpnStatusText = status
            }
        }
    }

    func copyProxyUrl() {
        UIPasteboard.general.string = socksUrl
        showToast(NSLocalizedString("proxy_url_copied", comment: ""))
    }

    func showToast(_ message: String) {
        toastMessage = message
        DispatchQueue.main.asyncAfter(deadline: .now() + 2) { [weak self] in
            if self?.toastMessage == message {
                self?.toastMessage = nil
            }
        }
    }

    func openTelegramProxy() {
        var urlString = "tg://socks?server=127.0.0.1&port=\(socksPort)"
        if socksAuthEnabled {
            urlString += "&user=\(activeSocksUser)&pass=\(activeSocksPass)"
        }
        if let url = URL(string: urlString) {
            UIApplication.shared.open(url)
        }
    }

    var socks5ProxyUri: String {
        if socksAuthEnabled {
            return "socks5://\(activeSocksUser):\(activeSocksPass)@127.0.0.1:\(socksPort)#WLB-\(socksPort)"
        }
        return "socks5://127.0.0.1:\(socksPort)#WLB-\(socksPort)"
    }

    func openHappProxy() {
        UIPasteboard.general.string = socks5ProxyUri
        guard let encoded = socks5ProxyUri.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed),
              let url = URL(string: "happ://add/\(encoded)") else {
            showToast(NSLocalizedString("toast_happ_params_copied", comment: ""))
            return
        }
        UIApplication.shared.open(url) { [weak self] opened in
            DispatchQueue.main.async {
                self?.showToast(opened ? NSLocalizedString("toast_happ_opened", comment: "") : NSLocalizedString("toast_happ_params_copied", comment: ""))
            }
        }
    }

    func copyProxyProfile() {
        UIPasteboard.general.string = socks5ProxyUri
        showToast(NSLocalizedString("toast_proxy_profile_copied", comment: ""))
    }

}
