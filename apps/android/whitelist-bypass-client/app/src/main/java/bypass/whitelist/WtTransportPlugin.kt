package bypass.whitelist

import android.content.Intent
import android.util.Log
import bypass.whitelist.planner.RuntimeApiClient
import bypass.whitelist.planner.GoRuntimeController
import bypass.whitelist.planner.RuntimeConfigStore
import bypass.whitelist.credentials.LocalUserCredentialStore
import bypass.whitelist.tunnel.TunnelServiceState
import bypass.whitelist.tunnel.VpnStatus
import bypass.whitelist.util.LogWriter
import com.getcapacitor.JSObject
import com.getcapacitor.Plugin
import com.getcapacitor.PluginCall
import com.getcapacitor.PluginMethod
import com.getcapacitor.annotation.CapacitorPlugin
import org.json.JSONObject
import java.io.File

/**
 * Thin bridge between the Capacitor UI and the native transport/runtime
 * surfaces. Existing proxy-only methods remain intact; new runtime methods
 * expose the same whitetransportd control plane used by desktop/mac flows.
 */
@CapacitorPlugin(name = "WtTransport")
class WtTransportPlugin : Plugin() {
    private val tag = "WtTransportPlugin"

    companion object {
        private const val RUNTIME_URL = "http://127.0.0.1:17680"
    }

    override fun load() {
        super.load()
        coordinatorOrNull()?.statusListener = { status ->
            notifyListeners("statusChanged", JSObject(status.toJson().toString()))
        }
    }

    /** Open the restricted, product-owned WBStream sign-in surface. */
    @PluginMethod
    fun beginRoomAuth(call: PluginCall) {
        activity.startActivity(Intent(activity, WBStreamRoomAuthActivity::class.java))
        call.resolve(JSObject().put("opened", true))
    }

    /** Return only local room-session readiness; credential material is never bridged to JS. */
    @PluginMethod
    fun getRoomAuthStatus(call: PluginCall) {
        call.resolve(JSObject().put("ready", LocalUserCredentialStore.hasRoomSession(context, "wbstream")))
    }

    @PluginMethod
    fun getStatus(call: PluginCall) {
        coordinatorOrNull()?.let {
            call.resolve(jsObjectFrom(it.statusJson()))
            return
        }
        if (GoRuntimeController.isAvailable()) {
            call.resolve(jsObjectFrom(GoRuntimeController.status(context)))
            return
        }
        try {
            val result = RuntimeApiClient.fetchStatus(RUNTIME_URL)
            call.resolve(jsObjectFrom(result))
        } catch (_: Throwable) {
            val active = TunnelServiceState.isAnyTunnelComponentRunning(context)
            val ret = JSObject()
            ret.put("active", active)
            ret.put("mode", if (active) "proxy" else "off")
            ret.put("status", if (active) VpnStatus.TUNNEL_ACTIVE.name else VpnStatus.CALL_DISCONNECTED.name)
            call.resolve(ret)
        }
    }

    @PluginMethod
    fun connect(call: PluginCall) {
        val configJson = call.getString("configJson")
        coordinatorOrNull()?.let { coordinator ->
            val nodeId = call.getString("serverId")
            Log.i(tag, "WT_RUNTIME_UI connect start backend=capacitor node=${nodeId ?: "auto"}")
            coordinator.connect(nodeId, configJson) { result ->
                result.fold(
                    onSuccess = {
                        Log.i(
                            tag,
                            "WT_RUNTIME_UI connected backend=capacitor node=${it.selectedNodeId ?: nodeId ?: "auto"} system_vpn=${it.systemVpnConnected}",
                        )
                        call.resolve(jsObjectFrom(it.toJson()))
                    },
                    onFailure = {
                        Log.e(tag, "WT_RUNTIME_UI failed backend=capacitor error=${safeRuntimeError(it)}")
                        call.reject(it.message ?: "connect failed")
                    },
                )
            }
            return
        }
        try {
            val nodeId = call.getString("serverId")
            if (GoRuntimeController.isAvailable()) {
                Log.i(tag, "WT_RUNTIME_UI connect start backend=gomobile node=${nodeId ?: "auto"}")
                val result = GoRuntimeController.connect(context, nodeId, configJson)
                Log.i(tag, "WT_RUNTIME_UI connected backend=gomobile node=${result.optString("node_id", nodeId ?: "auto")}")
                notifyListeners("statusChanged", jsObjectFrom(GoRuntimeController.status(context)))
                call.resolve(jsObjectFrom(result))
                return
            }
            val result = RuntimeApiClient.connect(RUNTIME_URL, nodeId)
            Log.i(tag, "WT_RUNTIME_UI connected backend=daemon node=${result.optString("node_id", nodeId ?: "auto")}")
            notifyListeners("statusChanged", jsObjectFrom(RuntimeApiClient.fetchStatus(RUNTIME_URL)))
            call.resolve(jsObjectFrom(result))
        } catch (error: Throwable) {
            call.reject(error.message ?: "connect failed")
        }
    }

    @PluginMethod
    fun disconnect(call: PluginCall) {
        coordinatorOrNull()?.let { coordinator ->
            coordinator.disconnect { result ->
                result.fold(
                    onSuccess = { call.resolve(jsObjectFrom(it.toJson())) },
                    onFailure = { call.reject(it.message ?: "disconnect failed") },
                )
            }
            return
        }
        if (GoRuntimeController.isAvailable()) {
            val result = GoRuntimeController.disconnect(context)
            notifyListeners("statusChanged", jsObjectFrom(GoRuntimeController.status(context)))
            call.resolve(jsObjectFrom(result))
            return
        }
        try {
            val result = RuntimeApiClient.disconnect(RUNTIME_URL)
            notifyListeners("statusChanged", jsObjectFrom(RuntimeApiClient.fetchStatus(RUNTIME_URL)))
            call.resolve(jsObjectFrom(result))
        } catch (error: Throwable) {
            call.reject(error.message ?: "disconnect failed")
        }
    }

    @PluginMethod
    fun setMode(call: PluginCall) {
        val mode = CapacitorVpnMode.parse(call.getString("mode"))
        coordinatorOrNull()?.let { coordinator ->
            try {
                if (mode == CapacitorVpnMode.OFF) {
                    coordinator.setMode(mode)
                    coordinator.disconnect { result ->
                        result.fold(
                            onSuccess = { status -> call.resolve(jsObjectFrom(status.toJson())) },
                            onFailure = { error -> call.reject(error.message ?: "disconnect failed") },
                        )
                    }
                } else {
                    call.resolve(jsObjectFrom(coordinator.setMode(mode).toJson()))
                }
            } catch (error: Throwable) {
                call.reject(error.message ?: "unsupported connection mode")
            }
            return
        }
        TunnelServiceState.logCallback?.invoke("WtTransport: setMode(${mode.name.lowercase()})")
        call.resolve()
    }

    @PluginMethod
    fun requestVPNPermission(call: PluginCall) {
        coordinatorOrNull()?.let { coordinator ->
            coordinator.requestVpnPermission { result ->
                result.fold(
                    onSuccess = { call.resolve(jsObjectFrom(coordinator.statusJson())) },
                    onFailure = { call.reject(it.message ?: "VPN permission denied") },
                )
            }
            return
        }
        call.reject("VPN permission is unavailable on this host")
    }

    @PluginMethod
    fun startSystemVPN(call: PluginCall) {
        coordinatorOrNull()?.let { coordinator ->
            coordinator.setMode(CapacitorVpnMode.TUNNEL)
            coordinator.connect(call.getString("serverId"), call.getString("configJson")) { result ->
                result.fold(
                    onSuccess = { call.resolve(jsObjectFrom(it.toJson())) },
                    onFailure = { call.reject(it.message ?: "VPN start failed") },
                )
            }
            return
        }
        call.reject("system VPN is unavailable on this host")
    }

    @PluginMethod
    fun stopSystemVPN(call: PluginCall) {
        coordinatorOrNull()?.let {
            it.disconnect { result ->
                result.fold(
                    onSuccess = { call.resolve(jsObjectFrom(it.toJson())) },
                    onFailure = { call.reject(it.message ?: "VPN stop failed") },
                )
            }
            return
        }
        call.reject("system VPN is unavailable on this host")
    }

    @PluginMethod
    fun getConnectionState(call: PluginCall) {
        coordinatorOrNull()?.let {
            call.resolve(jsObjectFrom(it.statusJson()))
            return
        }
        call.reject("connection state is unavailable on this host")
    }

    @PluginMethod
    fun getCapabilities(call: PluginCall) {
        val capabilities = capacitorCapabilities(systemVpnAvailable = coordinatorOrNull() != null)
        val ret = JSObject().apply {
            put("host", capabilities.host)
            capabilities.booleans.forEach(::put)
        }
        call.resolve(ret)
    }

    @PluginMethod
    fun getLogInfo(call: PluginCall) {
        try {
            val logFile = File(context.cacheDir, "relay.log")
            val requestedLimit = call.getInt("limit") ?: 300
            val limit = requestedLimit.coerceIn(1, 300)
            val ret = JSObject()
            ret.put("path", logFile.absolutePath)
            ret.put("lines", org.json.JSONArray(LogWriter.readRedacted(logFile, limit)))
            ret.put("persistent", logFile.isFile)
            call.resolve(ret)
        } catch (error: Throwable) {
            call.reject(error.message ?: "logs unavailable")
        }
    }

    @PluginMethod
    fun getSplitRouting(call: PluginCall) {
        coordinatorOrNull()?.let {
            val split = it.getSplitRouting()
            call.resolve(splitRoutingJsObject(split))
            return
        }
        call.reject("split routing is unavailable on this host")
    }

    @PluginMethod
    fun setSplitRouting(call: PluginCall) {
        coordinatorOrNull()?.let { coordinator ->
            try {
                requireSupportedLanAccess(call.getBoolean("lan_access"))
                val mode = parseCapacitorSplitMode(call.getString("mode"))
                val packages = call.getArray("packages")?.toList<String>()?.toSet()
                    ?: call.getString("packages").orEmpty()
                        .split('\n', ',', ';')
                        .map(String::trim)
                        .filter(String::isNotEmpty)
                        .toSet()
                val split = coordinator.setSplitRouting(mode, packages)
                call.resolve(splitRoutingJsObject(split))
            } catch (error: Throwable) {
                call.reject(error.message ?: "invalid split-routing settings")
            }
            return
        }
        call.reject("split routing is unavailable on this host")
    }

    @PluginMethod
    fun getSocksInfo(call: PluginCall) {
        if (GoRuntimeController.isAvailable()) {
            call.resolve(jsObjectFrom(GoRuntimeController.socksInfo()))
            return
        }
        val ret = JSObject()
        ret.put("host", "127.0.0.1")
        val port = try {
            val status = RuntimeApiClient.fetchStatus(RUNTIME_URL)
            val socksListen = status.optString("socks_listen", "")
            if (socksListen.isNotEmpty()) {
                socksListen.split(":").last().toIntOrNull() ?: 1080
            } else {
                1080
            }
        } catch (_: Exception) {
            1080
        }
        ret.put("port", port)
        call.resolve(ret)
    }

    @PluginMethod
    fun listNodes(call: PluginCall) {
        val apiUrl = call.getString("apiUrl") ?: "http://127.0.0.1:17680"
        try {
            val ret = JSObject()
            if (GoRuntimeController.isAvailable()) {
                ret.put("nodes", GoRuntimeController.listNodes())
                ret.put("backend", "gomobile")
            } else {
                ret.put("nodes", RuntimeApiClient.fetchNodes(apiUrl))
                ret.put("backend", "daemon-http")
            }
            call.resolve(ret)
        } catch (error: Throwable) {
            call.reject(error.message ?: "runtime listNodes failed")
        }
    }

    @PluginMethod
    fun getRuntimeStatus(call: PluginCall) {
        val apiUrl = call.getString("apiUrl") ?: "http://127.0.0.1:17680"
        try {
            if (GoRuntimeController.isAvailable()) {
                call.resolve(jsObjectFrom(GoRuntimeController.status(context)))
            } else {
                call.resolve(jsObjectFrom(RuntimeApiClient.fetchStatus(apiUrl)))
            }
        } catch (error: Throwable) {
            call.reject(error.message ?: "runtime getStatus failed")
        }
    }

    @PluginMethod
    fun connectRuntime(call: PluginCall) {
        val apiUrl = call.getString("apiUrl") ?: "http://127.0.0.1:17680"
        coordinatorOrNull()?.let { coordinator ->
            coordinator.connect(call.getString("nodeId"), call.getString("configJson")) { result ->
                result.fold(
                    onSuccess = { call.resolve(jsObjectFrom(it.toJson())) },
                    onFailure = { call.reject(it.message ?: "runtime connect failed") },
                )
            }
            return
        }
        try {
            val nodeId = call.getString("nodeId")
            val configJson = call.getString("configJson")
            if (GoRuntimeController.isAvailable()) {
                call.resolve(jsObjectFrom(GoRuntimeController.connect(context, nodeId, configJson)))
            } else {
                call.resolve(jsObjectFrom(RuntimeApiClient.connect(apiUrl, nodeId)))
            }
        } catch (error: Throwable) {
            call.reject(error.message ?: "runtime connect failed")
        }
    }

    @PluginMethod
    fun disconnectRuntime(call: PluginCall) {
        val apiUrl = call.getString("apiUrl") ?: "http://127.0.0.1:17680"
        coordinatorOrNull()?.let { coordinator ->
            coordinator.disconnect { result ->
                result.fold(
                    onSuccess = { call.resolve(jsObjectFrom(it.toJson())) },
                    onFailure = { call.reject(it.message ?: "runtime disconnect failed") },
                )
            }
            return
        }
        try {
            if (GoRuntimeController.isAvailable()) {
                call.resolve(jsObjectFrom(GoRuntimeController.disconnect(context)))
            } else {
                call.resolve(jsObjectFrom(RuntimeApiClient.disconnect(apiUrl)))
            }
        } catch (error: Throwable) {
            call.reject(error.message ?: "runtime disconnect failed")
        }
    }

    @PluginMethod
    fun scanRooms(call: PluginCall) {
        call.resolve()
    }

    @PluginMethod
    fun startRuntime(call: PluginCall) {
        try {
            val configJson = call.getString("configJson")
            call.resolve(jsObjectFrom(GoRuntimeController.ensureStarted(context, configJson)))
        } catch (error: Throwable) {
            call.reject(error.message ?: "runtime start failed")
        }
    }

    @PluginMethod
    fun installRuntimeConfig(call: PluginCall) {
        try {
            val configJson = call.getString("configJson") ?: error("configJson is required")
            RuntimeConfigStore.saveConfigJson(context, configJson)
            call.resolve(jsObjectFrom(RuntimeConfigStore.status(context)))
        } catch (error: Throwable) {
            call.reject(error.message ?: "runtime config install failed")
        }
    }

    @PluginMethod
    fun importRuntimeConfigFromDeviceFile(call: PluginCall) {
        try {
            val path = call.getString("path") ?: error("path is required")
            call.resolve(jsObjectFrom(RuntimeConfigStore.importFromDeviceFile(context, path)))
        } catch (error: Throwable) {
            call.reject(error.message ?: "runtime config import failed")
        }
    }

    @PluginMethod
    fun getRuntimeConfigStatus(call: PluginCall) {
        call.resolve(jsObjectFrom(RuntimeConfigStore.status(context)))
    }

    @PluginMethod
    fun clearRuntimeConfig(call: PluginCall) {
        RuntimeConfigStore.clear(context)
        call.resolve(jsObjectFrom(RuntimeConfigStore.status(context)))
    }

    /** Clear all local account/session material; no network-side deletion is attempted. */
    @PluginMethod
    fun deleteLocalAccount(call: PluginCall) {
        try {
            GoRuntimeController.stop()
            LocalUserCredentialStore.clearAll(context)
            RuntimeConfigStore.clear(context)
            context.getSharedPreferences("app_prefs", android.content.Context.MODE_PRIVATE)
                .edit().clear().apply()
            File(context.cacheDir, "relay.log").delete()
            File(context.cacheDir, "relay.log.1").delete()
            call.resolve(JSObject().put("deleted", true))
        } catch (error: Throwable) {
            call.reject(error.message ?: "local account deletion failed")
        }
    }

    @PluginMethod
    fun stopRuntime(call: PluginCall) {
        try {
            GoRuntimeController.stop()
            call.resolve(jsObjectFrom(GoRuntimeController.status(context)))
        } catch (error: Throwable) {
            call.reject(error.message ?: "runtime stop failed")
        }
    }

    @PluginMethod
    fun getCarrierHealth(call: PluginCall) {
        try {
            call.resolve(jsObjectFrom(GoRuntimeController.health()))
        } catch (error: Throwable) {
            call.reject(error.message ?: "runtime carrier health failed")
        }
    }

    private fun jsObjectFrom(value: JSONObject): JSObject {
        return JSObject(value.toString())
    }

    private fun safeRuntimeError(error: Throwable): String = (error.message ?: error.javaClass.simpleName)
        .replace(Regex("(?i)vk1\\.[A-Za-z0-9._-]+"), "[REDACTED_VK_TOKEN]")
        .replace(Regex("(?i)eyJ[A-Za-z0-9._-]{20,}"), "[REDACTED_JWT]")
        .replace(Regex("(?i)(token|cookie|authorization)=\\S+"), "$1=[REDACTED]")
        .take(500)

    private fun splitRoutingJsObject(split: CapacitorVpnSplitSettings): JSObject {
        val response = capacitorSplitRoutingResponse(split.mode.name.lowercase(), split.packages)
        return JSObject().apply {
            put("mode", response.getValue("mode"))
            put("lan_access", response.getValue("lan_access"))
            @Suppress("UNCHECKED_CAST")
            put("packages", org.json.JSONArray(response.getValue("packages") as List<String>))
        }
    }

    private fun coordinatorOrNull(): CapacitorVpnCoordinator? =
        (activity as? CapacitorMainActivity)?.vpnCoordinator
}
