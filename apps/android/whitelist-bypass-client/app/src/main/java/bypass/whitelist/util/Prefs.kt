package bypass.whitelist.util

import java.util.UUID
import android.content.Context
import android.content.pm.PackageManager
import android.content.SharedPreferences
import androidx.core.content.edit
import bypass.whitelist.tunnel.CallConfig
import bypass.whitelist.tunnel.SplitTunnelingMode
import bypass.whitelist.tunnel.TunnelMode

object Prefs {

    const val AUTO_DESTINATION_GRACE_MS = 60_000L

    private lateinit var prefs: SharedPreferences

    fun init(context: Context) {
        prefs = context.getSharedPreferences("app_prefs", Context.MODE_PRIVATE)
        initializeDefaultSplitTunnel(context)
    }

    private fun initializeDefaultSplitTunnel(context: Context) {
        if (prefs.contains(PrefsKeys.SPLIT_TUNNELING_MODE) || prefs.contains(PrefsKeys.SPLIT_TUNNELING_PACKAGES)) return
        val installed = defaultSplitTunnelPackages.filter { isPackageInstalled(context, it) }.toSet()
        if (installed.isNotEmpty()) {
            prefs.edit {
                putString(PrefsKeys.SPLIT_TUNNELING_MODE, SplitTunnelingMode.ONLY.name)
                putStringSet(PrefsKeys.SPLIT_TUNNELING_PACKAGES, installed)
            }
        }
    }

    private fun isPackageInstalled(context: Context, packageName: String): Boolean {
        return try {
            context.packageManager.getPackageInfo(packageName, 0)
            true
        } catch (_: PackageManager.NameNotFoundException) {
            false
        }
    }

    private val defaultSplitTunnelPackages = listOf(
        "com.instagram.android",
        "com.google.android.youtube",
        "com.facebook.katana",
        "com.facebook.lite",
        "org.telegram.messenger",
        "org.telegram.messenger.web",
        "org.thunderdog.challegram",
        "com.whatsapp",
        "com.whatsapp.w4b",
        "com.openai.chatgpt",
        "ai.x.grok",
        "com.xai.grok",
        "com.x.grok",
        "com.twitter.android",
        "com.android.chrome",
        "com.chrome.beta",
        "com.chrome.dev",
        "com.chrome.canary",
    )

    var connectOnStart: Boolean
        get() = prefs.getBoolean(PrefsKeys.CONNECT_ON_START, false)
        set(value) = prefs.edit { putBoolean(PrefsKeys.CONNECT_ON_START, value) }

    var telemetryEnabled: Boolean
        get() = prefs.getBoolean(PrefsKeys.TELEMETRY_ENABLED, false)
        set(value) = prefs.edit { putBoolean(PrefsKeys.TELEMETRY_ENABLED, value) }

    val discoveryClientId: String
        get() {
            val existing = prefs.getString(PrefsKeys.DISCOVERY_CLIENT_ID, null)
            if (!existing.isNullOrBlank()) return existing
            val generated = "android-" + UUID.randomUUID().toString()
            prefs.edit { putString(PrefsKeys.DISCOVERY_CLIENT_ID, generated) }
            return generated
        }

    var tunnelMode: TunnelMode
        get() {
            val name = prefs.getString(PrefsKeys.TUNNEL_MODE, TunnelMode.VIDEO.name)!!
            return try {
                TunnelMode.valueOf(name)
            } catch (_: IllegalArgumentException) {
                TunnelMode.VIDEO
            }
        }
        set(value) = prefs.edit { putString(PrefsKeys.TUNNEL_MODE, value.name) }

    var splitTunnelingMode: SplitTunnelingMode
        get() {
            val title = prefs.getString(PrefsKeys.SPLIT_TUNNELING_MODE, SplitTunnelingMode.NONE.name)!!
            return try {
                SplitTunnelingMode.valueOf(title)
            } catch (_: IllegalArgumentException) {
                SplitTunnelingMode.NONE
            }
        }
        set(value) = prefs.edit { putString(PrefsKeys.SPLIT_TUNNELING_MODE, value.name) }

    var splitTunnelingPackages: Set<String>
        get() = prefs.getStringSet(PrefsKeys.SPLIT_TUNNELING_PACKAGES, emptySet()) ?: emptySet()
        set(value) = prefs.edit { putStringSet(PrefsKeys.SPLIT_TUNNELING_PACKAGES, value) }

    var autofillEnabled: Boolean
        get() = prefs.getBoolean(PrefsKeys.AUTOFILL_ENABLED, true)
        set(value) = prefs.edit { putBoolean(PrefsKeys.AUTOFILL_ENABLED, value) }

    var autofillName: String
        get() = prefs.getString(PrefsKeys.AUTOFILL_NAME, "Hello")!!
        set(value) = prefs.edit { putString(PrefsKeys.AUTOFILL_NAME, value) }

    var headless: Boolean
        get() = prefs.getBoolean(PrefsKeys.HEADLESS, true)
        set(value) = prefs.edit { putBoolean(PrefsKeys.HEADLESS, value) }

    var socksHost: String
        get() = prefs.getString(PrefsKeys.SOCKS_HOST, Net.LOCALHOST) ?: Net.LOCALHOST
        set(value) = prefs.edit { putString(PrefsKeys.SOCKS_HOST, value) }

    var socksPort: Long
        get() = prefs.getLong(PrefsKeys.SOCKS_PORT, Ports.DEFAULT_SOCKS)
        set(value) = prefs.edit { putLong(PrefsKeys.SOCKS_PORT, value) }

    var socksAuthMode: SocksAuthMode
        get() {
            val name = prefs.getString(PrefsKeys.SOCKS_AUTH_MODE, SocksAuthMode.AUTO.name)!!
            return try {
                SocksAuthMode.valueOf(name)
            } catch (_: IllegalArgumentException) {
                SocksAuthMode.AUTO
            }
        }
        set(value) = prefs.edit { putString(PrefsKeys.SOCKS_AUTH_MODE, value.name) }

    var socksUser: String
        get() = prefs.getString(PrefsKeys.SOCKS_USER, "")!!
        set(value) = prefs.edit { putString(PrefsKeys.SOCKS_USER, value) }

    var socksPass: String
        get() = prefs.getString(PrefsKeys.SOCKS_PASS, "")!!
        set(value) = prefs.edit { putString(PrefsKeys.SOCKS_PASS, value) }

    var proxyOnly: Boolean
        get() = prefs.getBoolean(PrefsKeys.PROXY_ONLY, false)
        set(value) = prefs.edit {
            putBoolean(PrefsKeys.PROXY_ONLY, value)
            putString(PrefsKeys.CAPACITOR_VPN_MODE, if (value) "proxy" else "tunnel")
        }

    /**
     * Product mode persisted for the Capacitor coordinator. Legacy screens
     * still write [proxyOnly]; that setter keeps this value in sync.
     */
    var capacitorVpnMode: String
        get() = prefs.getString(PrefsKeys.CAPACITOR_VPN_MODE, if (proxyOnly) "proxy" else "tunnel") ?: "tunnel"
        set(value) {
            val normalized = value.trim().lowercase().ifBlank { "tunnel" }
            prefs.edit {
                putString(PrefsKeys.CAPACITOR_VPN_MODE, normalized)
                when (normalized) {
                    "proxy" -> putBoolean(PrefsKeys.PROXY_ONLY, true)
                    "tunnel" -> putBoolean(PrefsKeys.PROXY_ONLY, false)
                }
            }
        }

    var dnsMode: DnsMode
        get() {
            val name = prefs.getString(PrefsKeys.DNS_MODE, DnsMode.SYSTEM.name)!!
            return try {
                DnsMode.valueOf(name)
            } catch (_: IllegalArgumentException) {
                DnsMode.SYSTEM
            }
        }
        set(value) = prefs.edit { putString(PrefsKeys.DNS_MODE, value.name) }

    var dnsPrimary: String
        get() = prefs.getString(PrefsKeys.DNS_PRIMARY, Vpn.DNS_PRIMARY)!!
        set(value) = prefs.edit { putString(PrefsKeys.DNS_PRIMARY, value) }

    var dnsSecondary: String
        get() = prefs.getString(PrefsKeys.DNS_SECONDARY, Vpn.DNS_SECONDARY)!!
        set(value) = prefs.edit { putString(PrefsKeys.DNS_SECONDARY, value) }

    var vp8Fps: Int
        get() = prefs.getInt(PrefsKeys.VP8_FPS, VP8Defaults.FPS)
        set(value) = prefs.edit { putInt(PrefsKeys.VP8_FPS, value) }

    var vp8Batch: Int
        get() = prefs.getInt(PrefsKeys.VP8_BATCH, VP8Defaults.BATCH)
        set(value) = prefs.edit { putInt(PrefsKeys.VP8_BATCH, value) }

    var dualTrack: Boolean
        get() = prefs.getBoolean(PrefsKeys.DUAL_TRACK, false)
        set(value) = prefs.edit { putBoolean(PrefsKeys.DUAL_TRACK, value) }

    var savedDestinations: List<CallConfig>
        get() = CallConfig.listFromJson(prefs.getString(PrefsKeys.SAVED_DESTINATIONS, "") ?: "")
        set(value) = prefs.edit { putString(PrefsKeys.SAVED_DESTINATIONS, CallConfig.listToJson(value)) }

    var activeDestinationId: String
        get() = prefs.getString(PrefsKeys.ACTIVE_DESTINATION_ID, "") ?: ""
        set(value) = prefs.edit { putString(PrefsKeys.ACTIVE_DESTINATION_ID, value) }


    var autoDestination: CallConfig?
        get() {
            val expires = prefs.getLong(PrefsKeys.AUTO_DESTINATION_EXPIRES_MS, 0L)
            if (expires <= System.currentTimeMillis()) {
                clearAutoDestination()
                return null
            }
            val raw = prefs.getString(PrefsKeys.AUTO_DESTINATION, "") ?: ""
            return raw.takeIf { it.isNotBlank() }?.let {
                try { CallConfig.fromJson(org.json.JSONObject(it)) } catch (_: Exception) { null }
            }
        }
        set(value) {
            if (value == null) {
                clearAutoDestination()
            } else {
                prefs.edit {
                    putString(PrefsKeys.AUTO_DESTINATION, value.toJson().toString())
                    putLong(PrefsKeys.AUTO_DESTINATION_EXPIRES_MS, System.currentTimeMillis() + AUTO_DESTINATION_GRACE_MS)
                }
            }
        }

    fun extendAutoDestinationGrace() {
        if (prefs.getString(PrefsKeys.AUTO_DESTINATION, "").orEmpty().isNotBlank()) {
            prefs.edit { putLong(PrefsKeys.AUTO_DESTINATION_EXPIRES_MS, System.currentTimeMillis() + AUTO_DESTINATION_GRACE_MS) }
        }
    }

    fun clearAutoDestination() {
        prefs.edit {
            remove(PrefsKeys.AUTO_DESTINATION)
            remove(PrefsKeys.AUTO_DESTINATION_EXPIRES_MS)
        }
    }

    var themeMode: ThemeMode
        get() {
            val name = prefs.getString(PrefsKeys.THEME_MODE, ThemeMode.SYSTEM.name) ?: ThemeMode.SYSTEM.name
            return try { ThemeMode.valueOf(name) } catch (_: IllegalArgumentException) { ThemeMode.SYSTEM }
        }
        set(value) = prefs.edit { putString(PrefsKeys.THEME_MODE, value.name) }

    var accentMode: AccentMode
        get() {
            val name = prefs.getString(PrefsKeys.ACCENT_MODE, AccentMode.BLUE.name) ?: AccentMode.BLUE.name
            return try { AccentMode.valueOf(name) } catch (_: IllegalArgumentException) { AccentMode.BLUE }
        }
        set(value) = prefs.edit { putString(PrefsKeys.ACCENT_MODE, value.name) }

    var languageMode: LanguageMode
        get() {
            val name = prefs.getString(PrefsKeys.LANGUAGE_MODE, LanguageMode.SYSTEM.name) ?: LanguageMode.SYSTEM.name
            return try { LanguageMode.valueOf(name) } catch (_: IllegalArgumentException) { LanguageMode.SYSTEM }
        }
        set(value) = prefs.edit { putString(PrefsKeys.LANGUAGE_MODE, value.name) }

    var showNotificationStatusText: Boolean
        get() = prefs.getBoolean(PrefsKeys.SHOW_NOTIFICATION_STATUS_TEXT, false)
        set(value) = prefs.edit { putBoolean(PrefsKeys.SHOW_NOTIFICATION_STATUS_TEXT, value) }

    var lastUpdateCheckMs: Long
        get() = prefs.getLong(PrefsKeys.LAST_UPDATE_CHECK_MS, 0L)
        set(value) = prefs.edit { putLong(PrefsKeys.LAST_UPDATE_CHECK_MS, value) }

    val activeDestination: CallConfig?
        get() {
            val id = activeDestinationId
            if (id.isEmpty()) return null
            autoDestination?.let { if (it.id == id) return it }
            return savedDestinations.firstOrNull { it.id == id }
        }

    val activeTunnelMode: TunnelMode
        get() = activeDestination?.tunnelMode ?: tunnelMode

    val activeVp8Fps: Int
        get() = activeDestination?.vp8Fps ?: vp8Fps

    val activeVp8Batch: Int
        get() = activeDestination?.vp8Batch ?: vp8Batch

    val activeDualTrack: Boolean
        get() = activeDestination?.dualTrack ?: dualTrack

    fun updateDestination(config: CallConfig) {
        val list = savedDestinations.toMutableList()
        val index = list.indexOfFirst { it.id == config.id }
        if (index != -1) {
            list[index] = config
            savedDestinations = list
        }
    }

    fun addDestination(config: CallConfig) {
        val list = savedDestinations.toMutableList()
        list.removeAll { it.id == config.id }
        list.add(0, config)
        savedDestinations = list
        activeDestinationId = config.id
    }

    fun removeDestination(id: String) {
        val list = savedDestinations.filter { it.id != id }
        savedDestinations = list
        if (activeDestinationId == id) {
            activeDestinationId = list.firstOrNull()?.id ?: ""
        }
    }

    fun renameDestination(id: String, newName: String) {
        val list = savedDestinations.map { if (it.id == id) it.copy(name = newName) else it }
        savedDestinations = list
    }

    fun resetAllSettings() {
        val keepDestinations = prefs.getString(PrefsKeys.SAVED_DESTINATIONS, null)
        val keepActiveId = prefs.getString(PrefsKeys.ACTIVE_DESTINATION_ID, null)
        prefs.edit {
            clear()
            if (keepDestinations != null) putString(PrefsKeys.SAVED_DESTINATIONS, keepDestinations)
            if (keepActiveId != null) putString(PrefsKeys.ACTIVE_DESTINATION_ID, keepActiveId)
        }
    }
}
