package bypass.whitelist

import android.app.Activity
import android.os.Handler
import android.os.Looper
import bypass.whitelist.discovery.VkDiscoveryScanner
import bypass.whitelist.tunnel.CallConfig
import bypass.whitelist.util.Prefs
import org.json.JSONObject
import kotlin.concurrent.thread

/**
 * Handles VK room discovery scanning, caching, prewarming, bad-room recovery,
 * and private-bus client events for [MainActivity].
 */
internal class MainDiscoveryController(private val host: DiscoveryHost) {

    internal interface DiscoveryHost {
        val activity: Activity

        fun appendLog(message: String)
        fun getMainFragmentBridge(): MainTransportController.MainFragmentBridge?
        fun isTransportResetInProgress(): Boolean
        fun isTransportConnected(): Boolean
        fun isAnyTunnelRunning(): Boolean
        fun startJoinForConfig(config: CallConfig)
        fun onDestinationsChanged()
        fun getCurrentJoinUrl(): String
        fun triggerFullReset()
    }

    var discoveryScanRunning: Boolean = false
        private set
    var cachedDiscoveryConfigs: List<CallConfig> = emptyList()
        private set
    var cachedDiscoverySummary: String = "Комнаты: проверяю…"
        private set
    var pendingDiscoveryRescan: Boolean = false

    private var roomRequestSentForEmptyPool: Boolean = false
    private val badDiscoveryRooms = linkedSetOf<String>()
    private var currentDiscoveryRoom: String? = null
    private var staleRoomRecoveryInProgress: Boolean = false
    private val handler = Handler(Looper.getMainLooper())

    fun handleDiscoveryConnectPressed() {
        if (host.isTransportResetInProgress() || host.isAnyTunnelRunning()) {
            pendingDiscoveryRescan = true
            host.getMainFragmentBridge()?.onStatusTextChanged(
                "Остановите текущую сессию перед сканированием"
            )
            return
        }
        val cached = cachedDiscoveryConfigs.firstOrNull { !badDiscoveryRooms.contains(it.url) }
        if (cached != null) {
            host.appendLog("Discovery connect uses prewarmed room: ${cached.nodeLabel ?: cached.name}")
            useDiscoveredRoom(cached, connectNow = true)
            return
        }
        scanRooms(reason = "connect", force = true, connectWhenReady = true)
    }

    fun handleDiscoveryRefreshPressed() {
        scanRooms(reason = "manual-refresh", force = true, connectWhenReady = false)
    }

    fun handleDiscoveryRoomSelected(url: String) {
        val room = cachedDiscoveryConfigs.firstOrNull { it.url == url } ?: return
        useDiscoveredRoom(room, connectNow = false)
        host.getMainFragmentBridge()?.onStatusTextChanged(
            "Выбран сервер: ${room.nodeLabel ?: room.name}"
        )
    }

    fun handlePrewarmRooms() {
        if (host.isAnyTunnelRunning() || host.isTransportResetInProgress()) return
        if (discoveryScanRunning || cachedDiscoveryConfigs.isNotEmpty()) return
        scanRooms(reason = "startup", force = false, connectWhenReady = false)
    }

    private fun scanRooms(reason: String, force: Boolean, connectWhenReady: Boolean) {
        if (discoveryScanRunning && !force) return
        discoveryScanRunning = true
        host.getMainFragmentBridge()?.onStatusTextChanged("Сканирование комнат…")
        host.appendLog(
            "Discovery scan started: reason=$reason connectWhenReady=$connectWhenReady " +
                "private-bus first, then public VK fallback"
        )
        val activity = host.activity
        VkDiscoveryScanner.scanWithWebView(
            activity = activity,
            onProgress = { step ->
                activity.runOnUiThread {
                    host.getMainFragmentBridge()?.onStatusTextChanged(step)
                    host.appendLog("Discovery: $step")
                }
            },
            onDone = { result ->
                activity.runOnUiThread {
                    discoveryScanRunning = false
                    val allConfigs = result.configs
                    val myClientId = Prefs.discoveryClientId
                    val configs = allConfigs.filter { cfg ->
                        !badDiscoveryRooms.contains(cfg.url) &&
                            (cfg.ownerClientId.isNullOrBlank() || cfg.ownerClientId == myClientId)
                    }
                    cachedDiscoveryConfigs = configs
                    val nodes = configs.map { room -> room.nodeLabel ?: room.name }.distinct().take(3)
                    cachedDiscoverySummary = if (configs.isNotEmpty()) {
                        "Свободно: ${configs.size} · ${nodes.joinToString(", ")}"
                    } else {
                        "Свободных комнат нет · запрошена новая"
                    }
                    host.getMainFragmentBridge()?.onStatusTextChanged(cachedDiscoverySummary)
                    host.appendLog(
                        "Discovery scan finished: picked_free=${configs.size}, " +
                            "total_free=${allConfigs.size}, method=${result.method}, " +
                            "source=${result.source ?: "none"}, ${result.stats.summary()}"
                    )
                    if (configs.isNotEmpty()) {
                        roomRequestSentForEmptyPool = false
                        if (connectWhenReady) useDiscoveredRoom(configs.first(), connectNow = true)
                    } else if (!roomRequestSentForEmptyPool) {
                        roomRequestSentForEmptyPool = true
                        sendPrivateBusClientEvent("request_room", null, "prewarm_no_free_rooms")
                        if (connectWhenReady) {
                            host.appendLog("Discovery: no room yet after request_room; retrying scan soon")
                            handler.postDelayed({
                                if (!host.isTransportConnected() &&
                                    !host.isTransportResetInProgress() &&
                                    !discoveryScanRunning
                                ) {
                                    scanRooms(
                                        reason = "connect-retry-after-request-room",
                                        force = true, connectWhenReady = true,
                                    )
                                }
                            }, 8_000L)
                        }
                    } else if (connectWhenReady) {
                        host.appendLog("Discovery: still no room; retrying scan soon")
                        handler.postDelayed({
                            if (!host.isTransportConnected() &&
                                !host.isTransportResetInProgress() &&
                                !discoveryScanRunning
                            ) {
                                scanRooms(
                                    reason = "connect-retry-no-room",
                                    force = true, connectWhenReady = true,
                                )
                            }
                        }, 8_000L)
                    }
                }
            },
        )
    }

    private fun useDiscoveredRoom(picked: CallConfig, connectNow: Boolean) {
        currentDiscoveryRoom = picked.url
        Prefs.autoDestination = picked
        Prefs.activeDestinationId = picked.id
        host.onDestinationsChanged()
        host.appendLog(
            "Discovery selected room: node=${picked.nodeLabel ?: picked.name} " +
                "owner=${picked.ownerClientId ?: "free"} slot=${picked.slotId ?: "?"} " +
                "lease=${picked.leaseId ?: "?"}"
        )
        if (connectNow) {
            sendPrivateBusClientEvent("claim_room", picked.url, "connect")
            host.startJoinForConfig(picked)
        }
    }

    fun scheduleBadRoomRecovery(reason: String) {
        val room = currentDiscoveryRoom?.takeIf { it.startsWith("wbstream://") }
            ?: host.getCurrentJoinUrl().takeIf { it.startsWith("wbstream://") }
            ?: return
        if (staleRoomRecoveryInProgress) return
        staleRoomRecoveryInProgress = true
        badDiscoveryRooms.add(room)
        host.appendLog("Room looks stale; blacklisted locally and requesting replacement")
        sendPrivateBusClientEvent("bad_room", room, reason)
        pendingDiscoveryRescan = true
        host.activity.runOnUiThread {
            if (!host.isTransportResetInProgress()) host.triggerFullReset()
            staleRoomRecoveryInProgress = false
        }
    }

    fun handlePendingRescan() {
        if (!pendingDiscoveryRescan) return
        pendingDiscoveryRescan = false
        host.appendLog("Previous session stopped, rescanning discovery")
        handleDiscoveryConnectPressed()
    }

    private fun sendPrivateBusClientEvent(type: String, room: String?, reason: String) {
        val badSnapshot = synchronized(badDiscoveryRooms) { badDiscoveryRooms.toList() }
        thread(name = "wt-client-event") {
            val ok = VkDiscoveryScanner.sendClientEvent(
                type = type, clientId = Prefs.discoveryClientId,
                room = room, reason = reason, badRooms = badSnapshot,
            )
            host.appendLog("Private-bus $type sent: $ok")
            if (Prefs.telemetryEnabled) VkDiscoveryScanner.sendTelemetry(
                clientId = Prefs.discoveryClientId,
                level = if (ok) "info" else "error",
                event = "private_bus.$type",
                messageText = "Private-bus $type sent: $ok",
                room = room,
                meta = JSONObject().apply {
                    put("reason", reason)
                    put("ok", ok)
                    put("bad_rooms_count", badSnapshot.size)
                },
            )
        }
    }
}
