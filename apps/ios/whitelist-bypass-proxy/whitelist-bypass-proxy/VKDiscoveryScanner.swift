import Foundation
import os
import UIKit

struct DiscoveryRoom: Identifiable {
    let id: String
    let status: String
    let room: String
    let creator: String
    let node: String
    let location: String
    let createdAt: Int64
    let expiresAt: Int64
    let seq: Int64

    var order: Int64 { seq > 0 ? seq : createdAt }
    var isFree: Bool {
        status == "free" && !room.isEmpty && expiresAt > Int64(Date().timeIntervalSince1970)
    }
    var displayName: String {
        let left = node.isEmpty ? creator : node
        if !left.isEmpty && !location.isEmpty { return "\(left) · \(location)" }
        if !left.isEmpty { return left }
        return "VK discovery"
    }
}

final class VKDiscoveryScanner {
    private let logger = Logger(subsystem: "bypass.whitelist", category: "discovery")
    private let userAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1"
    private var logPeerCursor = 0
    private let logPeerLock = NSLock()
    private var urls: [URL] {
        guard let groupId = Bundle.main.object(forInfoDictionaryKey: "VKDiscoveryGroupID") as? String,
              !groupId.isEmpty,
              groupId.allSatisfy({ $0.isNumber }) else { return [] }
        return [
            "https://m.vk.ru/club\(groupId)",
            "https://m.vk.com/club\(groupId)",
            "https://vk.ru/club\(groupId)",
            "https://vk.com/club\(groupId)",
        ].compactMap(URL.init(string:))
    }

    func scan(completion: @escaping (_ rooms: [DiscoveryRoom], _ source: String?) -> Void) {
        logger.info("scanner scan started")
        scanPrivateBus { [weak self] rooms in
            guard let self = self else { return }
            if !rooms.isEmpty {
                self.logger.info("private-bus returned rooms=\(rooms.count, privacy: .public) free=\(rooms.filter { $0.isFree }.count, privacy: .public)")
                completion(rooms, "vk-private-bus")
                return
            }
            self.logger.warning("private-bus returned no rooms; falling back to wall")
            self.scanWall(urls: self.urls, firstSource: nil, completion: completion)
        }
    }



    func sendTelemetry(clientId: String, level: String, event: String, messageText: String, room: String? = nil, meta: [String: Any] = [:], completion: ((Bool) -> Void)? = nil) {
        guard !WtBusSecrets.vkBotToken.isEmpty, let peerID = nextLogPeerID() else {
            logger.error("telemetry skipped: empty token or telemetry peer id")
            completion?(false)
            return
        }
        let now = Int64(Date().timeIntervalSince1970)
        var payload: [String: Any] = [
            "v": 1,
            "kind": "telemetry",
            "level": level,
            "event": event,
            "client_id": clientId,
            "platform": "ios",
            "app_version": AppVersion.name,
            "app_build": AppVersion.code,
            "device": UIDevice.current.model,
            "system_version": UIDevice.current.systemVersion,
            "room": room ?? "",
            "message": String(messageText.prefix(900)),
            "created_at": now,
            "seq": now,
            "nonce": UUID().uuidString,
        ]
        payload["meta"] = meta
        guard let encrypted = WtBusCrypto.encryptEnvelope(prefix: "wtlog1", payload: payload),
              let url = URL(string: "https://api.vk.com/method/messages.send") else {
            logger.error("telemetry encryption failed event=\(event, privacy: .public)")
            completion?(false)
            return
        }
        sendVKMessage(url: url, peerID: peerID, message: encrypted, completion: completion)
    }

    private func sendVKMessage(url: URL, peerID: String, message: String, completion: ((Bool) -> Void)? = nil) {
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.timeoutInterval = 10
        request.setValue("BEZabotny-NET iOS telemetry", forHTTPHeaderField: "User-Agent")
        request.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        let params: [(String, String)] = [
            ("peer_id", peerID),
            ("random_id", String(Int(Date().timeIntervalSince1970 * 1000))),
            ("message", message),
            ("access_token", WtBusSecrets.vkBotToken),
            ("v", "5.199"),
        ]
        request.httpBody = params.map { key, value in
            "\(key)=\(value.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? value)"
        }.joined(separator: "&").data(using: .utf8)
        URLSession.shared.dataTask(with: request) { [weak self] data, _, error in
            if let error = error {
                self?.logger.error("VK send failed: \(error.localizedDescription, privacy: .public)")
                completion?(false)
                return
            }
            let ok: Bool
            if let data = data,
               let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any] {
                ok = json["error"] == nil && json["response"] != nil
            } else {
                ok = false
            }
            completion?(ok)
        }.resume()
    }

    func sendClientEvent(type: String, clientId: String, room: String?, reason: String, badRooms: [String] = [], completion: ((Bool) -> Void)? = nil) {
        guard !WtBusSecrets.vkBotToken.isEmpty, !WtBusSecrets.vkBotPeerID.isEmpty else {
            logger.warning("client event skipped: empty token or peer id")
            completion?(false)
            return
        }
        let now = Int(Date().timeIntervalSince1970)
        let payload: [String: Any] = [
            "v": 2,
            "type": type,
            "client_id": clientId,
            "platform": "ios",
            "app_version": AppVersion.name,
            "app_build": AppVersion.code,
            "device": UIDevice.current.model,
            "system_version": UIDevice.current.systemVersion,
            "room": room ?? "",
            "bad_rooms": badRooms,
            "reason": reason,
            "created_at": now,
            "seq": now,
            "nonce": UUID().uuidString,
        ]
        guard let message = WtBusCrypto.encryptEnvelope(prefix: "wtclient2", payload: payload),
              let url = URL(string: "https://api.vk.com/method/messages.send") else {
            logger.error("client event encryption failed type=\(type, privacy: .public)")
            completion?(false)
            return
        }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.timeoutInterval = 10
        request.setValue("BEZabotny-NET iOS private bus", forHTTPHeaderField: "User-Agent")
        request.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        let params: [(String, String)] = [
            ("peer_id", WtBusSecrets.vkBotPeerID),
            ("random_id", String(Int(Date().timeIntervalSince1970 * 1000))),
            ("message", message),
            ("access_token", WtBusSecrets.vkBotToken),
            ("v", "5.199"),
        ]
        request.httpBody = params.map { key, value in
            "\(key)=\(value.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? value)"
        }.joined(separator: "&").data(using: .utf8)
        URLSession.shared.dataTask(with: request) { [weak self] data, _, error in
            if let error = error {
                self?.logger.error("client event send failed type=\(type, privacy: .public) error=\(error.localizedDescription, privacy: .public)")
                completion?(false)
                return
            }
            let ok: Bool
            if let data = data,
               let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any] {
                ok = json["error"] == nil && json["response"] != nil
            } else {
                ok = false
            }
            self?.logger.info("client event sent type=\(type, privacy: .public) ok=\(ok, privacy: .public)")
            completion?(ok)
        }.resume()
    }


    private func scanOkGraphBus(completion: @escaping ([DiscoveryRoom]) -> Void) {
        // okGraph (OK.ru graph) discovery was imported incomplete (commit 86b54a0):
        // it referenced a `Defaults` type and okGraph iOS secrets that never existed
        // in this tree (Android has OK_GRAPH_* buildConfig; iOS does not). Disabled —
        // returns no rooms — pending proper WtBusSecrets.okGraph* wiring. To re-enable,
        // add okGraphToken/okGraphChatID to WtBusSecrets (+ generator + example) and
        // restore the original api.ok.ru/graph/me/messages request here.
        completion([])
    }

    private func scanPrivateBus(completion: @escaping ([DiscoveryRoom]) -> Void) {
        let peerIDs = vkBusPeerIDs()
        guard !WtBusSecrets.vkBotToken.isEmpty, !peerIDs.isEmpty else {
            logger.warning("private-bus skipped: empty token or peer id")
            completion([])
            return
        }
        guard let url = URL(string: "https://api.vk.com/method/messages.getHistory") else {
            logger.error("private-bus URL construction failed")
            completion([])
            return
        }
        fetchPrivateBusTexts(url: url, peerIDs: peerIDs, index: 0, texts: []) { [weak self] texts in
            guard let self = self else { return }
            let rooms = self.parse(text: texts.joined(separator: "\n"))
            self.logger.info("private-bus lanes=\(peerIDs.count, privacy: .public) rooms=\(rooms.count, privacy: .public)")
            completion(rooms)
        }
    }

    private func fetchPrivateBusTexts(url: URL, peerIDs: [String], index: Int, texts: [String], completion: @escaping ([String]) -> Void) {
        guard index < peerIDs.count else {
            completion(texts)
            return
        }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.timeoutInterval = 10
        request.setValue("BEZabotny-NET iOS private bus", forHTTPHeaderField: "User-Agent")
        request.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        let params: [(String, String)] = [
            ("peer_id", peerIDs[index]),
            ("count", "100"),
            ("access_token", WtBusSecrets.vkBotToken),
            ("v", "5.199"),
        ]
        request.httpBody = params.map { key, value in
            "\(key)=\(value.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? value)"
        }.joined(separator: "&").data(using: .utf8)
        URLSession.shared.dataTask(with: request) { [weak self] data, response, error in
            var nextTexts = texts
            if let error = error {
                self?.logger.error("private-bus lane network error: \(error.localizedDescription, privacy: .public)")
            } else if let http = response as? HTTPURLResponse {
                self?.logger.info("private-bus lane HTTP \(http.statusCode, privacy: .public)")
            }
            if let data = data,
               let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
               let response = json["response"] as? [String: Any],
               let items = response["items"] as? [[String: Any]] {
                nextTexts.append(items.compactMap { $0["text"] as? String }.joined(separator: "\n"))
            }
            self?.fetchPrivateBusTexts(url: url, peerIDs: peerIDs, index: index + 1, texts: nextTexts, completion: completion)
        }.resume()
    }

    private func scanWall(urls: [URL], firstSource: String?, completion: @escaping ([DiscoveryRoom], String?) -> Void) {
        guard let url = urls.first else {
            completion([], firstSource)
            return
        }
        var request = URLRequest(url: url)
        request.timeoutInterval = 10
        request.setValue(userAgent, forHTTPHeaderField: "User-Agent")
        request.setValue("text/html,application/xhtml+xml", forHTTPHeaderField: "Accept")
        URLSession.shared.dataTask(with: request) { [weak self] data, response, error in
            guard let self = self else { return }
            let source = firstSource ?? url.absoluteString
            if let error = error {
                self.logger.warning("wall fetch failed url=\(url.absoluteString, privacy: .public) error=\(error.localizedDescription, privacy: .public)")
            } else if let http = response as? HTTPURLResponse {
                self.logger.info("wall fetch HTTP \(http.statusCode, privacy: .public) url=\(url.absoluteString, privacy: .public)")
            }
            if let data = data, let html = String(data: data, encoding: .utf8) {
                let rooms = self.parse(text: html)
                self.logger.info("wall parse url=\(url.absoluteString, privacy: .public) bytes=\(data.count, privacy: .public) rooms=\(rooms.count, privacy: .public)")
                if !rooms.isEmpty {
                    completion(rooms, source)
                    return
                }
            }
            self.scanWall(urls: Array(urls.dropFirst()), firstSource: source, completion: completion)
        }.resume()
    }

    private func parse(text raw: String) -> [DiscoveryRoom] {
        let text = normalize(raw)
        var rooms: [DiscoveryRoom] = []
        rooms.append(contentsOf: parseEncryptedPayloads(text))
        rooms.append(contentsOf: parseLegacyPayloads(text))
        rooms.append(contentsOf: parseLegacyRooms(text))
        var unique: [String: DiscoveryRoom] = [:]
        for room in rooms { unique[room.id] = room }
        let sorted = unique.values.sorted { $0.order > $1.order }
        logger.info("parse summary encrypted=\(rooms.count, privacy: .public) unique=\(sorted.count, privacy: .public) free=\(sorted.filter { $0.isFree }.count, privacy: .public)")
        return sorted
    }

    private func parseEncryptedPayloads(_ text: String) -> [DiscoveryRoom] {
        guard let regex = try? NSRegularExpression(pattern: "(wt(?:room|bus)2)\\.([A-Za-z0-9_-]{1,16})\\.([А-Яа-я]{24,})") else { return [] }
        let ns = text as NSString
        return regex.matches(in: text, range: NSRange(location: 0, length: ns.length)).compactMap { match in
            guard match.numberOfRanges >= 4 else { return nil }
            let prefix = ns.substring(with: match.range(at: 1))
            let kid = ns.substring(with: match.range(at: 2))
            let encoded = ns.substring(with: match.range(at: 3))
            guard let json = WtBusCrypto.decryptEnvelope(prefix: prefix, kid: kid, encoded: encoded) else { return nil }
            return jsonToRoom(json)
        }
    }

    private func parseLegacyPayloads(_ text: String) -> [DiscoveryRoom] {
        guard let regex = try? NSRegularExpression(pattern: "wt1\\.([A-Za-z0-9_-]{24,})") else { return [] }
        let ns = text as NSString
        return regex.matches(in: text, range: NSRange(location: 0, length: ns.length)).compactMap { match in
            guard match.numberOfRanges >= 2 else { return nil }
            let encoded = ns.substring(with: match.range(at: 1))
            guard let data = base64URLDecode(encoded),
                  let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return nil }
            return jsonToRoom(json)
        }
    }

    private func jsonToRoom(_ json: [String: Any]) -> DiscoveryRoom {
        let status = (json["status"] as? String) ?? "free"
        let room = (json["room"] as? String) ?? ""
        let creator = (json["creator"] as? String) ?? ""
        let node = (json["node"] as? String) ?? (json["node_name"] as? String) ?? ""
        let location = (json["location"] as? String) ?? [json["country"] as? String, json["city"] as? String, json["region"] as? String].compactMap { $0 }.joined(separator: " ")
        let createdAt = int64(json["created_at"]) ?? int64(json["createdAt"]) ?? 0
        let expiresAt = int64(json["expires_at"]) ?? int64(json["expiresAt"]) ?? Int64.max
        let seq = int64(json["seq"]) ?? 0
        let streamId = (json["stream_id"] as? String) ?? (json["streamId"] as? String) ?? room
        return DiscoveryRoom(id: streamId.isEmpty ? room : streamId, status: status, room: room, creator: creator, node: node, location: location, createdAt: createdAt, expiresAt: expiresAt, seq: seq)
    }

    private func parseLegacyRooms(_ text: String) -> [DiscoveryRoom] {
        guard let regex = try? NSRegularExpression(pattern: "wbstream://[A-Za-z0-9._~:/?#\\[\\]@!$&'()*+,;=%-]+") else { return [] }
        let ns = text as NSString
        return regex.matches(in: text, range: NSRange(location: 0, length: ns.length)).map { match in
            let room = ns.substring(with: match.range)
            return DiscoveryRoom(id: room, status: "free", room: room, creator: "legacy", node: "legacy", location: "", createdAt: 0, expiresAt: Int64.max, seq: 0)
        }
    }

    private func normalize(_ raw: String) -> String {
        raw
            .replacingOccurrences(of: "\\u002F", with: "/")
            .replacingOccurrences(of: "\\/", with: "/")
            .replacingOccurrences(of: "&quot;", with: "\"")
            .replacingOccurrences(of: "&#34;", with: "\"")
            .replacingOccurrences(of: "&amp;", with: "&")
    }

    private func int64(_ value: Any?) -> Int64? {
        if let n = value as? Int64 { return n }
        if let n = value as? Int { return Int64(n) }
        if let n = value as? Double { return Int64(n) }
        if let s = value as? String { return Int64(s) }
        return nil
    }

    private func base64URLDecode(_ text: String) -> Data? {
        var s = text.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
        let pad = s.count % 4
        if pad > 0 { s += String(repeating: "=", count: 4 - pad) }
        return Data(base64Encoded: s)
    }

    private func vkBusPeerIDs() -> [String] {
        if !WtBusSecrets.vkBusPeerIDs.isEmpty { return WtBusSecrets.vkBusPeerIDs }
        return WtBusSecrets.vkBotPeerID.isEmpty ? [] : [WtBusSecrets.vkBotPeerID]
    }

    private func vkLogPeerIDs() -> [String] {
        if !WtBusSecrets.vkLogPeerIDs.isEmpty { return WtBusSecrets.vkLogPeerIDs }
        return WtBusSecrets.vkTelemetryPeerID.isEmpty ? [] : [WtBusSecrets.vkTelemetryPeerID]
    }

    private func nextLogPeerID() -> String? {
        let peerIDs = vkLogPeerIDs()
        guard !peerIDs.isEmpty else { return nil }
        logPeerLock.lock()
        let index = logPeerCursor % peerIDs.count
        logPeerCursor += 1
        logPeerLock.unlock()
        return peerIDs[index]
    }
}
