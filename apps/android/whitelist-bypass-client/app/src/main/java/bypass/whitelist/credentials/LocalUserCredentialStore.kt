package bypass.whitelist.credentials

import android.content.Context
import android.util.Base64
import org.json.JSONObject
import java.nio.ByteBuffer
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/** Encrypted app-private storage for provider user sessions. */
object LocalUserCredentialStore {
    private const val PREFS_NAME = "local_user_credentials"
    private const val KEY_ALIAS = "whitetransport.local-user-credentials.v1"
    private const val VALUE_KEY_PREFIX = "credential."
    private const val ROOM_SESSION_KEY_PREFIX = "room_session."
    private const val IV_BYTES = 12

    /** A short-lived room credential kept only in encrypted app-private storage. */
    data class RoomSession(val accessToken: String, val cookieHeader: String) {
        fun runtimeJson(): String = JSONObject()
            .put("platform", "wbstream")
            .put("access_token", accessToken)
            .put("cookie_header", cookieHeader)
            .toString()
    }

    /** Store a user session; this data never enters RuntimeConfigStore/TokenStore. */
    fun put(context: Context, provider: String, accessToken: String, refreshToken: String? = null) {
        val normalized = LocalUserCredentialPolicy.requireUserProvider(provider)
        require(accessToken.isNotBlank()) { "user access token must not be empty" }
        val payload = JSONObject().put("access_token", accessToken)
        refreshToken?.takeIf(String::isNotBlank)?.let { payload.put("refresh_token", it) }
        val encrypted = encrypt(payload.toString().toByteArray(Charsets.UTF_8))
        context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE).edit()
            .putString(VALUE_KEY_PREFIX + normalized, Base64.encodeToString(encrypted, Base64.NO_WRAP))
            .apply()
    }

    /** Execute a provider operation with the decrypted token without exposing a serializable object. */
    fun <T> withAccessToken(context: Context, provider: String, block: (String) -> T): T? {
        val normalized = LocalUserCredentialPolicy.requireUserProvider(provider)
        val encoded = context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
            .getString(VALUE_KEY_PREFIX + normalized, null) ?: return null
        val payload = JSONObject(String(decrypt(Base64.decode(encoded, Base64.NO_WRAP)), Charsets.UTF_8))
        val token = payload.optString("access_token").takeIf(String::isNotBlank) ?: return null
        return block(token)
    }

    fun clear(context: Context, provider: String) {
        val normalized = LocalUserCredentialPolicy.requireUserProvider(provider)
        context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE).edit()
            .remove(VALUE_KEY_PREFIX + normalized)
            .apply()
    }

    /** Delete all locally held user sessions during account deletion/reset. */
    fun clearAll(context: Context) {
        context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE).edit().clear().apply()
    }

    fun hasCredential(context: Context, provider: String): Boolean {
        val normalized = LocalUserCredentialPolicy.requireUserProvider(provider)
        return context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
            .contains(VALUE_KEY_PREFIX + normalized)
    }

    /** Store a WBStream room-creation session. It is never put in TokenStore or runtime config. */
    fun putRoomSession(context: Context, provider: String, accessToken: String, cookieHeader: String) {
        val normalized = LocalUserCredentialPolicy.requireRoomSessionProvider(provider)
        require(accessToken.isNotBlank()) { "room session access token must not be empty" }
        require(cookieHeader.isNotBlank()) { "room session cookie header must not be empty" }
        val payload = JSONObject()
            .put("access_token", accessToken)
            .put("cookie_header", cookieHeader)
        val encrypted = encrypt(payload.toString().toByteArray(Charsets.UTF_8))
        context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE).edit()
            .putString(ROOM_SESSION_KEY_PREFIX + normalized, Base64.encodeToString(encrypted, Base64.NO_WRAP))
            .apply()
    }

    /** Invoke a Go mobile binding with one decrypted room session and do not retain it in controller state. */
    fun <T> withRoomSession(context: Context, provider: String, block: (RoomSession) -> T): T? {
        val normalized = LocalUserCredentialPolicy.requireRoomSessionProvider(provider)
        val encoded = context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
            .getString(ROOM_SESSION_KEY_PREFIX + normalized, null) ?: return null
        val payload = JSONObject(String(decrypt(Base64.decode(encoded, Base64.NO_WRAP)), Charsets.UTF_8))
        val accessToken = payload.optString("access_token").takeIf(String::isNotBlank) ?: return null
        val cookieHeader = payload.optString("cookie_header").takeIf(String::isNotBlank) ?: return null
        return block(RoomSession(accessToken, cookieHeader))
    }

    fun clearRoomSession(context: Context, provider: String) {
        val normalized = LocalUserCredentialPolicy.requireRoomSessionProvider(provider)
        context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE).edit()
            .remove(ROOM_SESSION_KEY_PREFIX + normalized)
            .apply()
    }

    fun hasRoomSession(context: Context, provider: String): Boolean {
        val normalized = LocalUserCredentialPolicy.requireRoomSessionProvider(provider)
        return context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
            .contains(ROOM_SESSION_KEY_PREFIX + normalized)
    }

    private fun encrypt(plain: ByteArray): ByteArray {
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, key())
        val ciphertext = cipher.doFinal(plain)
        return ByteBuffer.allocate(IV_BYTES + ciphertext.size)
            .put(cipher.iv)
            .put(ciphertext)
            .array()
    }

    private fun decrypt(encoded: ByteArray): ByteArray {
        require(encoded.size > IV_BYTES) { "stored local credential is invalid" }
        val iv = encoded.copyOfRange(0, IV_BYTES)
        val ciphertext = encoded.copyOfRange(IV_BYTES, encoded.size)
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, iv))
        return cipher.doFinal(ciphertext)
    }

    private fun key(): SecretKey {
        val keyStore = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (keyStore.getKey(KEY_ALIAS, null) as? SecretKey)?.let { return it }
        return KeyGenerator.getInstance("AES", "AndroidKeyStore").apply {
            init(android.security.keystore.KeyGenParameterSpec.Builder(
                KEY_ALIAS,
                android.security.keystore.KeyProperties.PURPOSE_ENCRYPT or android.security.keystore.KeyProperties.PURPOSE_DECRYPT,
            ).setBlockModes(android.security.keystore.KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(android.security.keystore.KeyProperties.ENCRYPTION_PADDING_NONE)
                .setUserAuthenticationRequired(false)
                .build())
        }.generateKey()
    }
}
