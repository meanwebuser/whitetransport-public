package bypass.whitelist.discovery

import android.util.Base64
import bypass.whitelist.BuildConfig
import org.json.JSONObject
import java.security.SecureRandom
import javax.crypto.Cipher
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

object WtBusCrypto {
    private const val B64URL = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
    private const val CUSTOM64 = "АБВГДЕЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯабвгдежзийклмнопрстуфхцчшщъыьэюя"

    fun encryptEnvelope(prefix: String, payload: JSONObject): String? {
        return try {
            val key = decodeB64Url(BuildConfig.WTBUS_KEY_B64.ifBlank { return null })
            if (key.size != 32) return null
            val kid = BuildConfig.WTBUS_KEY_ID.ifBlank { "k1" }
            val nonce = ByteArray(12)
            SecureRandom().nextBytes(nonce)
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.ENCRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(128, nonce))
            cipher.updateAAD("$prefix.$kid".toByteArray(Charsets.UTF_8))
            val encrypted = cipher.doFinal(payload.toString().toByteArray(Charsets.UTF_8))
            val blob = ByteArray(nonce.size + encrypted.size)
            System.arraycopy(nonce, 0, blob, 0, nonce.size)
            System.arraycopy(encrypted, 0, blob, nonce.size, encrypted.size)
            "$prefix.$kid.${encodeCustom(blob)}"
        } catch (_: Exception) {
            null
        }
    }

    fun decryptEnvelope(prefix: String, kid: String, encoded: String): JSONObject? {
        return try {
            val key = decodeB64Url(BuildConfig.WTBUS_KEY_B64.ifBlank { return null })
            if (key.size != 32) return null
            val blob = decodeCustom(encoded)
            if (blob.size < 12 + 16) return null
            val nonce = blob.copyOfRange(0, 12)
            val cipherText = blob.copyOfRange(12, blob.size)
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.DECRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(128, nonce))
            cipher.updateAAD("$prefix.$kid".toByteArray(Charsets.UTF_8))
            val plain = cipher.doFinal(cipherText)
            JSONObject(String(plain, Charsets.UTF_8))
        } catch (_: Exception) {
            null
        }
    }

    private fun encodeCustom(data: ByteArray): String {
        val b64 = Base64.encodeToString(data, Base64.URL_SAFE or Base64.NO_WRAP or Base64.NO_PADDING)
        return buildString(b64.length) {
            for (ch in b64) {
                val idx = B64URL.indexOf(ch)
                if (idx >= 0) append(CUSTOM64[idx])
            }
        }
    }

    private fun decodeCustom(text: String): ByteArray {
        val b64 = buildString(text.length) {
            for (ch in text) {
                val idx = CUSTOM64.indexOf(ch)
                if (idx < 0) throw IllegalArgumentException("bad wtbus char")
                append(B64URL[idx])
            }
        }
        return decodeB64Url(b64)
    }

    private fun decodeB64Url(value: String): ByteArray {
        val padded = value + "=".repeat((4 - value.length % 4) % 4)
        return Base64.decode(padded, Base64.URL_SAFE or Base64.NO_WRAP)
    }
}
