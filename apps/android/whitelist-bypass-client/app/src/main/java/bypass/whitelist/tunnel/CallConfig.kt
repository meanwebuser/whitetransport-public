package bypass.whitelist.tunnel

import org.json.JSONArray
import org.json.JSONObject
import java.util.UUID

data class CallConfig(
    val id: String,
    val name: String,
    val url: String,
    val tunnelMode: TunnelMode? = null,
    val vp8Fps: Int? = null,
    val vp8Batch: Int? = null,
    val dualTrack: Boolean? = null,
    val autoDiscovered: Boolean = false,
    val slotId: String? = null,
    val leaseId: String? = null,
    val expiresAt: Long? = null,
    val nodeLabel: String? = null,
    val ownerClientId: String? = null,
) {
    val platform: CallPlatform get() = CallPlatform.fromUrl(url)

    val platformGlyph: String get() = when (platform) {
        CallPlatform.VK -> "VK"
        CallPlatform.TELEMOST -> "TM"
        CallPlatform.WBSTREAM -> "WB"
        CallPlatform.DION -> "DN"
    }

    val platformLabel: String get() = when (platform) {
        CallPlatform.VK -> "VK"
        CallPlatform.TELEMOST -> "Telemost"
        CallPlatform.WBSTREAM -> "WB Stream"
        CallPlatform.DION -> "DION"
    }

    fun toJson(): JSONObject = JSONObject().apply {
        put("id", id)
        put("name", name)
        put("url", url)
        tunnelMode?.let { put("tunnelMode", it.name) }
        vp8Fps?.let { put("vp8Fps", it) }
        vp8Batch?.let { put("vp8Batch", it) }
        dualTrack?.let { put("dualTrack", it) }
        if (autoDiscovered) put("autoDiscovered", true)
        slotId?.let { put("slotId", it) }
        leaseId?.let { put("leaseId", it) }
        expiresAt?.let { put("expiresAt", it) }
        nodeLabel?.let { put("nodeLabel", it) }
        ownerClientId?.let { put("ownerClientId", it) }
    }

    companion object {
        fun newWith(name: String, url: String): CallConfig =
            CallConfig(id = UUID.randomUUID().toString(), name = name, url = url)

        fun autoWith(
            name: String,
            url: String,
            slotId: String?,
            leaseId: String?,
            expiresAt: Long?,
            advertisedTunnelMode: TunnelMode? = null,
            nodeLabel: String? = null,
            ownerClientId: String? = null,
        ): CallConfig {
            val stable = listOfNotNull(slotId, leaseId).joinToString(":").ifBlank { url }
            return CallConfig(
                id = UUID.nameUUIDFromBytes(stable.toByteArray()).toString(),
                name = name,
                url = url,
                tunnelMode = TransportPolicy.autoModeFor(url, advertisedTunnelMode),
                autoDiscovered = true,
                slotId = slotId,
                leaseId = leaseId,
                expiresAt = expiresAt,
                nodeLabel = nodeLabel,
                ownerClientId = ownerClientId,
            )
        }

        fun fromJson(obj: JSONObject): CallConfig = CallConfig(
            id = obj.getString("id"),
            name = obj.getString("name"),
            url = obj.getString("url"),
            tunnelMode = if (obj.has("tunnelMode")) try { TunnelMode.valueOf(obj.getString("tunnelMode")) } catch(e: Exception) { null } else null,
            vp8Fps = if (obj.has("vp8Fps")) obj.getInt("vp8Fps") else null,
            vp8Batch = if (obj.has("vp8Batch")) obj.getInt("vp8Batch") else null,
            dualTrack = if (obj.has("dualTrack")) obj.getBoolean("dualTrack") else null,
            autoDiscovered = obj.optBoolean("autoDiscovered", false),
            slotId = obj.optString("slotId").takeIf { it.isNotBlank() },
            leaseId = obj.optString("leaseId").takeIf { it.isNotBlank() },
            expiresAt = if (obj.has("expiresAt")) obj.optLong("expiresAt") else null,
            nodeLabel = obj.optString("nodeLabel").takeIf { it.isNotBlank() },
            ownerClientId = obj.optString("ownerClientId").takeIf { it.isNotBlank() },
        )

        fun listToJson(items: List<CallConfig>): String {
            val arr = JSONArray()
            items.forEach { arr.put(it.toJson()) }
            return arr.toString()
        }

        fun listFromJson(raw: String): List<CallConfig> {
            if (raw.isBlank()) return emptyList()
            return try {
                val arr = JSONArray(raw)
                buildList(arr.length()) {
                    for (i in 0 until arr.length()) add(fromJson(arr.getJSONObject(i)))
                }
            } catch (_: Exception) {
                emptyList()
            }
        }

        fun suggestNameFor(url: String): String {
            val platform = CallPlatform.fromUrl(url)
            return when (platform) {
                CallPlatform.VK -> "VK call"
                CallPlatform.TELEMOST -> "Telemost"
                CallPlatform.WBSTREAM -> "WB Stream"
                CallPlatform.DION -> "DION"
            }
        }
    }
}
