package bypass.whitelist.planner

import android.content.Context
import bypass.whitelist.tunnel.LoopbackSocksEndpoint
import org.json.JSONArray
import org.json.JSONObject
import java.lang.reflect.InvocationTargetException

object GoRuntimeController {
    private const val LEGACY_CLASS = "androidbind.Androidbind"
    private const val MOBILE_CLASS = "mobile.Mobile"

    @Volatile
    private var mobileClass: Class<*>? = null

    @Volatile
    private var lastConfigJson: String? = null

    fun isAvailable(): Boolean = runCatching { loadMobileClass() }.isSuccess && !isLegacyOnlyAar()

    fun ensureStarted(context: Context, configJson: String? = null): JSONObject {
        val cfg = configJson?.trim()?.takeIf { it.isNotEmpty() }
            ?.let { RuntimeConfigStore.validateConfigJson(it).toString() }
            ?: lastConfigJson
            ?: RuntimeConfigStore.resolveConfigJson(context)
        if (!isStarted()) {
            call("startTransport", arrayOf(String::class.java), cfg)
            lastConfigJson = cfg
        }
        return status(context)
    }

    fun stop() {
        if (!isAvailable()) return
        call("stopTransport", emptyArray<Class<*>>())
    }

    fun connect(context: Context, nodeId: String?, configJson: String? = null): JSONObject {
        ensureStarted(context, configJson)
        val requestedNode = nodeId?.trim()?.takeIf(String::isNotEmpty)
        val preferredNode = requestedNode ?: RuntimeConfigStore.preferredNodeId(context)
        val candidates = waitForCandidateNodes(preferredNode, DEFAULT_NODE_WAIT_MS, strictPreferred = requestedNode != null)
        var lastError: Throwable? = null
        for (candidate in candidates) {
            try {
                call("connectTransport", arrayOf(String::class.java), candidate)
                val connected = status(context)
                val confirmedNode = requireConfirmedTransportNode(candidate, connected.optString("active_node_id").trim())
                return connected.put("node_id", confirmedNode)
            } catch (error: Throwable) {
                lastError = error
                runCatching { call("disconnectTransport", emptyArray<Class<*>>()) }
            }
        }
        throw lastError ?: error("no discovered nodes")
    }

    /**
     * Connect the native runtime for a payload-only probe without claiming an
     * Android TUN profile. The production coordinator uses [connect], which
     * additionally requires the system VPN profile before reporting connected.
     */
    fun connectRuntimeForPayload(context: Context, nodeId: String?, configJson: String? = null): JSONObject {
        ensureStarted(context, configJson)
        val requestedNode = nodeId?.trim()?.takeIf(String::isNotEmpty)
        val candidates = waitForCandidateNodes(requestedNode, DEFAULT_NODE_WAIT_MS, strictPreferred = requestedNode != null)
        var lastError: Throwable? = null
        for (candidate in candidates) {
            try {
                call("connectTransport", arrayOf(String::class.java), candidate)
                val connected = status(context)
                val activeNode = connected.optString("active_node_id").trim()
                require(activeNode.isNotEmpty()) { "connected runtime status lacks active_node_id" }
                require(requestedNode.isNullOrEmpty() || activeNode == requestedNode) {
                    "selected node mismatch between request and connected runtime"
                }
                return connected.put("node_id", activeNode)
            } catch (error: Throwable) {
                lastError = error
                runCatching { call("disconnectTransport", emptyArray<Class<*>>()) }
            }
        }
        throw lastError ?: error("no discovered nodes")
    }

    fun waitForFirstNode(timeoutMs: Long = DEFAULT_NODE_WAIT_MS): String = waitForNode(null, timeoutMs)

    fun waitForNode(preferredNodeId: String?, timeoutMs: Long = DEFAULT_NODE_WAIT_MS): String {
        return waitForCandidateNodes(preferredNodeId, timeoutMs).first()
    }

    fun waitForCandidateNodes(preferredNodeId: String?, timeoutMs: Long = DEFAULT_NODE_WAIT_MS): List<String> {
        return waitForCandidateNodes(preferredNodeId, timeoutMs, strictPreferred = false)
    }

    private fun waitForCandidateNodes(preferredNodeId: String?, timeoutMs: Long, strictPreferred: Boolean): List<String> {
        val deadline = System.currentTimeMillis() + timeoutMs
        var last = listNodes()
        while (System.currentTimeMillis() < deadline) {
            if (!preferredNodeId.isNullOrBlank() && containsNodeId(last, preferredNodeId)) {
                return if (strictPreferred) listOf(preferredNodeId) else orderedNodeIds(last, preferredNodeId)
            }
            if (preferredNodeId.isNullOrBlank()) {
                orderedNodeIds(last, null).takeIf { it.isNotEmpty() }?.let { return it }
            }
            Thread.sleep(1_000)
            last = listNodes()
        }
        if (strictPreferred && !preferredNodeId.isNullOrBlank()) error("selected node was not discovered: $preferredNodeId")
        return orderedNodeIds(last, preferredNodeId).takeIf { it.isNotEmpty() } ?: error("no discovered nodes")
    }

    fun disconnect(context: Context): JSONObject {
        if (!isAvailable()) return stoppedStatus()
        call("disconnectTransport", emptyArray<Class<*>>())
        return status(context)
    }

    /** Pin an active session to one negotiated endpoint for an explicit diagnostic. */
    fun selectEgressEndpoint(endpointId: String): JSONObject {
        require(endpointId.isNotBlank()) { "egress endpoint ID is required" }
        check(isAvailable() && isStarted()) { "Go runtime is not started" }
        val statusJson = call(
            "selectEgressEndpoint",
            arrayOf(String::class.java),
            endpointId,
        ) as? String ?: error("Go runtime returned no endpoint selection status")
        return JSONObject(statusJson)
    }

    fun listNodes(): JSONArray {
        if (!isAvailable() || !isStarted()) return JSONArray()
        return JSONArray(callString("listNodes"))
    }

    fun health(): JSONObject {
        if (!isAvailable() || !isStarted()) return JSONObject()
        return JSONObject(callString("getHealth"))
    }

    fun status(context: Context): JSONObject {
        if (!isAvailable()) {
            return stoppedStatus().put("backend", "gomobile-missing")
        }
        if (!isStarted()) {
            return stoppedStatus().put("backend", "gomobile")
        }

        val status = JSONObject(callString("getStatus"))
        val socks = socksAddr()
        status.put("backend", "gomobile")
        status.put("active", status.optString("state") == "connected" || status.optString("state") == "running")
        status.put("mode", if (status.optBoolean("active")) "proxy" else "off")
        if (socks.isNotBlank()) {
            status.put("socks_listen", socks)
            status.put("socksHost", socks.substringBeforeLast(":", "127.0.0.1"))
            status.put("socksPort", socks.substringAfterLast(":", "1080").toIntOrNull() ?: 1080)
        }
        status.put("package", context.packageName)
        return status
    }

    fun socksInfo(): JSONObject {
        val socks = if (isAvailable() && isStarted()) socksAddr() else ""
        val host = socks.substringBeforeLast(":", "127.0.0.1").ifBlank { "127.0.0.1" }
        val port = socks.substringAfterLast(":", "1080").toIntOrNull() ?: 1080
        return JSONObject().put("host", host).put("port", port).put("backend", "gomobile")
    }

    /** Resolve the authoritative no-auth SOCKS endpoint reported by Go. */
    fun runtimeSocksEndpoint(status: JSONObject, configJson: String? = null): LoopbackSocksEndpoint {
        val statusListen = status.optString("socks_listen").trim()
        if (statusListen.isNotEmpty()) {
            return LoopbackSocksEndpoint.runtimeFromListenAddress(statusListen)
        }
        val statusHost = status.optString("socksHost").trim()
        val statusPort = status.optInt("socksPort", 0)
        if (statusHost.isNotEmpty() && statusPort > 0) {
            return LoopbackSocksEndpoint.runtime(statusHost, statusPort)
        }
        val configListen = configJson
            ?.trim()
            ?.takeIf(String::isNotEmpty)
            ?.let(::JSONObject)
            ?.optString("socks_listen")
            ?.trim()
            .orEmpty()
        require(configListen.isNotEmpty()) { "Go runtime did not report a SOCKS listen endpoint" }
        return LoopbackSocksEndpoint.runtimeFromListenAddress(configListen)
    }

    // ── tun2socks VPN bridge ────────────────────────────────────────────

    /**
     * Start the TUN-to-SOCKS5 bridge in the Go runtime. Reads IP packets
     * from the VPN TUN fd and forwards TCP/UDP flows through the local
     * SOCKS5 proxy. Returns after the pinned native engine is initialized.
     */
    fun startTun2Socks(fd: Long, mtu: Long, socksPort: Long, socksUser: String, socksPass: String): Throwable? {
        if (!isAvailable()) return IllegalStateException("Go runtime not available")
        return try {
            call("startTun2Socks",
                arrayOf(Long::class.java, Long::class.java, Long::class.java, String::class.java, String::class.java),
                fd, mtu, socksPort, socksUser, socksPass)
            null
        } catch (e: Throwable) {
            e.cause ?: e
        }
    }

    /** Stop the native bridge and propagate native/reflection failures. */
    fun stopTun2Socks() {
        invokeRequiredTun2SocksStop(isAvailable()) {
            call("stopTun2Socks", emptyArray<Class<*>>())
        }
    }

    private fun isStarted(): Boolean {
        if (!isAvailable()) return false
        return call("isStarted", emptyArray<Class<*>>()) as? Boolean ?: false
    }

    private fun socksAddr(): String = callString("getSocksAddr")

    private fun stoppedStatus(): JSONObject = JSONObject()
        .put("state", "stopped")
        .put("active", false)
        .put("mode", "off")

    private fun callString(name: String): String = call(name, emptyArray<Class<*>>()) as? String ?: ""

    private fun firstNodeId(nodes: JSONArray): String? {
        for (index in 0 until nodes.length()) {
            val id = nodes.optJSONObject(index)?.optString("node_id")?.takeIf { it.isNotBlank() }
            if (id != null) return id
        }
        return null
    }

    private fun containsNodeId(nodes: JSONArray, nodeID: String): Boolean {
        for (index in 0 until nodes.length()) {
            if (nodes.optJSONObject(index)?.optString("node_id") == nodeID) return true
        }
        return false
    }

    private fun orderedNodeIds(nodes: JSONArray, preferredNodeId: String?): List<String> {
        val ids = mutableListOf<String>()
        for (index in 0 until nodes.length()) {
            val id = nodes.optJSONObject(index)?.optString("node_id")?.takeIf { it.isNotBlank() } ?: continue
            if (!ids.contains(id)) ids += id
        }
        if (preferredNodeId.isNullOrBlank() || !ids.remove(preferredNodeId)) return ids
        return listOf(preferredNodeId) + ids
    }

    private fun call(name: String, types: Array<Class<*>>, vararg args: Any?): Any? {
        val cls = loadMobileClass()
        val method = runCatching { cls.getMethod(name, *types) }
            .getOrElse { cls.getMethod(name.replaceFirstChar { it.uppercaseChar() }, *types) }
        return try {
            method.invoke(null, *args)
        } catch (error: InvocationTargetException) {
            throw unwrapInvocationTarget(error)
        }
    }

    private fun loadMobileClass(): Class<*> {
        mobileClass?.let { return it }
        return Class.forName(MOBILE_CLASS).also { mobileClass = it }
    }

    private fun isLegacyOnlyAar(): Boolean = runCatching { Class.forName(LEGACY_CLASS) }.isSuccess &&
        runCatching { loadMobileClass().getMethod("startTransport", String::class.java) }.isFailure
}

/** Expose the gomobile method failure instead of leaking reflection wrappers. */
internal fun unwrapInvocationTarget(error: Throwable): Throwable {
    var current = error
    while (current is InvocationTargetException && current.cause != null && current.cause !== current) {
        current = current.cause!!
    }
    return current
}

/** Require both runtime status and the hashed native profile to name one exact node. */
internal fun requireConfirmedSelectedNode(requestedNodeId: String?, status: JSONObject): String {
    val activeNodeId = status.optString("active_node_id").trim()
    val profileNodeId = status.optJSONObject("system_vpn_profile")
        ?.optString("selected_node_id")
        ?.trim()
        .orEmpty()
    return requireConfirmedSelectedNode(requestedNodeId, activeNodeId, profileNodeId)
}

/** Confirm transport ownership before the asynchronous system VPN profile refresh completes. */
internal fun requireConfirmedTransportNode(requestedNodeId: String?, activeNodeId: String): String {
    require(activeNodeId.isNotEmpty()) { "connected runtime status lacks active_node_id" }
    val requested = requestedNodeId?.trim().orEmpty()
    require(requested.isEmpty() || activeNodeId == requested) {
        "selected node mismatch between request and connected runtime"
    }
    return activeNodeId
}

/** Pure contract used by JVM tests without Android's unmocked JSONObject. */
internal fun requireConfirmedSelectedNode(requestedNodeId: String?, activeNodeId: String, profileNodeId: String): String {
    require(activeNodeId.isNotEmpty()) { "connected runtime status lacks active_node_id" }
    require(profileNodeId.isNotEmpty()) { "connected runtime status lacks system_vpn_profile.selected_node_id" }
    require(profileNodeId == activeNodeId) {
        "selected node mismatch between runtime status and system VPN profile"
    }
    val requested = requestedNodeId?.trim().orEmpty()
    require(requested.isEmpty() || activeNodeId == requested) {
        "selected node mismatch between request and connected runtime"
    }
    return activeNodeId
}

/** Keep stop failures observable so TunnelVpnService can publish CALL_FAILED. */
internal fun invokeRequiredTun2SocksStop(available: Boolean, stop: () -> Unit) {
    check(available) { "Go runtime not available" }
    try {
        stop()
    } catch (error: Throwable) {
        throw (error.cause ?: error)
    }
}
    private const val DEFAULT_NODE_WAIT_MS = 45_000L
