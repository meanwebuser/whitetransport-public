package bypass.whitelist.credentials

import org.json.JSONArray
import org.json.JSONObject

/**
 * Defines the boundary for credentials obtained from a user's provider login.
 *
 * Bootstrap/service credentials are a separate build-time concern. User
 * sessions are accepted only for VK or OK and must never be represented as a
 * runtime-config/TokenStore field or attached to another provider request.
 */
object LocalUserCredentialPolicy {
    private val forbiddenSerializationKeys = setOf(
        "user_token",
        "user_access_token",
        "user_refresh_token",
        "session_token",
        "session_access_token",
    )

    fun requireUserProvider(provider: String): String {
        val normalized = provider.trim().lowercase()
        require(normalized == "vk" || normalized == "ok") {
            "local user credentials are supported only for VK or OK"
        }
        return normalized
    }

    /** WBStream room sessions are distinct from VK/OK user identity sessions. */
    fun requireRoomSessionProvider(provider: String): String {
        val normalized = provider.trim().lowercase()
        require(normalized == "wbstream") {
            "local room sessions are supported only for WBStream"
        }
        return normalized
    }

    fun rejectUserTokenOnRequest(provider: String, userToken: String?) {
        if (userToken.isNullOrBlank()) return
        val normalized = provider.trim().lowercase()
        require(normalized == "vk" || normalized == "ok") {
            "user credentials must not be attached to $normalized requests"
        }
    }

    fun rejectUserCredentialSerialization(fields: Map<String, Any?>) {
        val scope = fields.entries.firstOrNull { (key, _) ->
            key.trim().lowercase() in setOf("credential_scope", "credential_origin")
        }?.value?.toString()?.trim()?.lowercase()
        require(scope != "user") {
            "user credentials are local-only and must not be serialized"
        }
        require(fields.keys.none { it.trim().lowercase() in forbiddenSerializationKeys }) {
            "user credential fields are local-only and must not be serialized"
        }
    }

    fun rejectUserCredentialSerialization(value: JSONObject) {
        val keys = value.keys()
        while (keys.hasNext()) {
            val key = keys.next()
            val nested = value.opt(key)
            val normalized = key.trim().lowercase()
            if (normalized in forbiddenSerializationKeys ||
                (normalized in setOf("credential_scope", "credential_origin") && nested.toString().trim().lowercase() == "user")
            ) {
                throw IllegalArgumentException("user credentials are local-only and must not be serialized")
            }
            when (nested) {
                is JSONObject -> rejectUserCredentialSerialization(nested)
                is JSONArray -> for (index in 0 until nested.length()) {
                    nested.opt(index).takeIf { it is JSONObject }?.let { rejectUserCredentialSerialization(it as JSONObject) }
                }
            }
        }
    }
}
