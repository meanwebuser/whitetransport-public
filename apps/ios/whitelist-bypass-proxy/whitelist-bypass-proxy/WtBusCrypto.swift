import CryptoKit
import Foundation

enum WtBusCrypto {
    private static let b64url = Array("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_")
    private static let custom64 = Array("АБВГДЕЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯабвгдежзийклмнопрстуфхцчшщъыьэюя")

    static func encryptEnvelope(prefix: String, payload: [String: Any]) -> String? {
        let kid = WtBusSecrets.keyID.isEmpty ? "k1" : WtBusSecrets.keyID
        guard !WtBusSecrets.keyB64.isEmpty,
              let keyData = decodeBase64URL(WtBusSecrets.keyB64),
              keyData.count == 32,
              JSONSerialization.isValidJSONObject(payload),
              let plain = try? JSONSerialization.data(withJSONObject: payload, options: []) else { return nil }
        do {
            let key = SymmetricKey(data: keyData)
            let nonceData = Data((0..<12).map { _ in UInt8.random(in: 0...255) })
            let nonce = try AES.GCM.Nonce(data: nonceData)
            let aad = Data("\(prefix).\(kid)".utf8)
            let box = try AES.GCM.seal(plain, using: key, nonce: nonce, authenticating: aad)
            var blob = Data()
            blob.append(nonceData)
            blob.append(box.ciphertext)
            blob.append(box.tag)
            return "\(prefix).\(kid).\(encodeCustom(blob))"
        } catch {
            return nil
        }
    }

    static func decryptEnvelope(prefix: String, kid: String, encoded: String) -> [String: Any]? {
        guard !WtBusSecrets.keyB64.isEmpty,
              let keyData = decodeBase64URL(WtBusSecrets.keyB64),
              keyData.count == 32,
              let blob = decodeCustom(encoded),
              blob.count >= 12 + 16 else { return nil }
        let nonceData = blob.prefix(12)
        let cipherAndTag = blob.dropFirst(12)
        guard cipherAndTag.count >= 16 else { return nil }
        let ciphertext = cipherAndTag.dropLast(16)
        let tag = cipherAndTag.suffix(16)
        do {
            let key = SymmetricKey(data: keyData)
            let nonce = try AES.GCM.Nonce(data: nonceData)
            let box = try AES.GCM.SealedBox(nonce: nonce, ciphertext: ciphertext, tag: tag)
            let aad = Data("\(prefix).\(kid)".utf8)
            let plain = try AES.GCM.open(box, using: key, authenticating: aad)
            return try JSONSerialization.jsonObject(with: plain) as? [String: Any]
        } catch {
            return nil
        }
    }

    private static func encodeCustom(_ data: Data) -> String {
        let raw = data.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
        var result = ""
        for ch in raw {
            if let idx = b64url.firstIndex(of: ch) {
                result.append(custom64[idx])
            }
        }
        return result
    }

    private static func decodeCustom(_ text: String) -> Data? {
        var ascii = ""
        for ch in text {
            guard let idx = custom64.firstIndex(of: ch) else { return nil }
            ascii.append(b64url[idx])
        }
        return decodeBase64URL(ascii)
    }

    private static func decodeBase64URL(_ text: String) -> Data? {
        var value = text.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
        let pad = value.count % 4
        if pad > 0 { value += String(repeating: "=", count: 4 - pad) }
        return Data(base64Encoded: value)
    }
}
