package bypass.whitelist

import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.util.Log
import androidx.core.content.ContextCompat
import bypass.whitelist.planner.GoRuntimeController
import bypass.whitelist.tunnel.LoopbackSocksEndpoint
import bypass.whitelist.tunnel.toServicePayload
import bypass.whitelist.tunnel.SplitTunnelingMode
import bypass.whitelist.tunnel.TunnelVpnService
import bypass.whitelist.tunnel.VpnStatus
import bypass.whitelist.util.Prefs
import org.json.JSONObject
import java.io.IOException
import java.net.InetSocketAddress
import java.net.Socket
import java.util.concurrent.Executor
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean
import javax.net.ssl.SSLSocket
import javax.net.ssl.SSLSocketFactory

/**
 * Owns the Android product connection lifecycle for the Capacitor host.
 *
 * A Go session is transport readiness only. Tunnel mode is reported as
 * connected after the existing [TunnelVpnService] has established its TUN and
 * published the authoritative TUNNEL_ACTIVE status. Every partial connect is
 * rolled back in reverse order before the originating plugin call completes.
 */
class CapacitorVpnCoordinator(
    private val dependencies: CapacitorVpnDependencies,
    private val executor: Executor = Executors.newSingleThreadExecutor { runnable ->
        Thread(runnable, "capacitor-vpn-coordinator").apply { isDaemon = true }
    },
    private val tunnelReadyTimeoutMs: Long = DEFAULT_TUNNEL_READY_TIMEOUT_MS,
) {
    @Volatile
    private var currentStatus = CapacitorVpnStatus.disconnected(dependencies.settings().mode)
    private var pendingConnect: PendingConnect? = null
    private var pendingPermission: PendingPermission? = null
    private var operationGeneration: Long = 0
    private var activeConnectOperationId: Long? = null
    private val stateLock = Any()

    /** Called after each lifecycle transition so the Capacitor plugin can notify React. */
    @Volatile
    var statusListener: ((CapacitorVpnStatus) -> Unit)? = null

    fun status(): CapacitorVpnStatus = synchronized(stateLock) {
        // Poll transport truth for an established VPN without restarting it or
        // overriding a user operation that already owns the lifecycle.
        if (pendingConnect == null && currentStatus.systemVpnConnected &&
            currentStatus.lifecycle in setOf(CapacitorVpnLifecycle.CONNECTED, CapacitorVpnLifecycle.CONNECTING) &&
            dependencies.isTunnelActive()
        ) {
            val transportState = dependencies.transportState()
            val connected = transportState == CapacitorTransportState.CONNECTED
            if (connected || transportState == CapacitorTransportState.RECOVERING) {
                currentStatus = currentStatus.copy(
                    lifecycle = if (connected) CapacitorVpnLifecycle.CONNECTED else CapacitorVpnLifecycle.CONNECTING,
                    transportConnected = connected,
                )
            }
        }
        currentStatus
    }

    /** Whether an installed debug launch may connect without showing consent UI. */
    internal fun hasVpnConsentForNonInteractiveConnect(): Boolean = dependencies.hasVpnConsent()

    /** Persist the supported tunnel selection; Android product proxy mode has no route proof. */
    fun setMode(mode: CapacitorVpnMode): CapacitorVpnStatus {
        if (mode == CapacitorVpnMode.PROXY) {
            throw UnsupportedOperationException("Android proxy-only mode is not a product VPN connection")
        }
        val update = synchronized(stateLock) {
            dependencies.settings().mode = mode
            updateStatusLocked(currentStatus.copy(mode = mode))
        }
        dispatchStatus(update)
        return update.status
    }

    /** Persist split rules used by TunnelVpnService.Builder on the next start. */
    fun setSplitRouting(mode: SplitTunnelingMode, packages: Set<String>): CapacitorVpnSplitSettings {
        val normalized = validateCapacitorSplitPackages(mode, packages, dependencies.appPackageName())
        val settings = dependencies.settings()
        settings.splitMode = mode
        settings.packages = normalized
        return CapacitorVpnSplitSettings(mode, normalized)
    }

    fun getSplitRouting(): CapacitorVpnSplitSettings {
        val settings = dependencies.settings()
        return CapacitorVpnSplitSettings(settings.splitMode, settings.packages)
    }

    /** Request Android consent without starting a transport session. */
    fun requestVpnPermission(callback: (Result<Boolean>) -> Unit) {
        if (dependencies.hasVpnConsent()) {
            callback(Result.success(true))
            return
        }
        val pending: PendingPermission
        val permissionUpdate = synchronized(stateLock) {
            if (pendingPermission != null || pendingConnect != null) {
                callback(Result.failure(IllegalStateException("VPN permission request is already in progress")))
                return
            }
            operationGeneration += 1
            pending = PendingPermission(operationGeneration, callback)
            pendingPermission = pending
            updateStatusLocked(CapacitorVpnStatus.permissionRequired(CapacitorVpnMode.TUNNEL))
        }
        dispatchStatus(permissionUpdate)
        try {
            dependencies.requestVpnConsent(pending.operationToken)
        } catch (error: Throwable) {
            val failureUpdate = synchronized(stateLock) {
                if (pendingPermission !== pending) return@synchronized null
                pendingPermission = null
                updateStatusLocked(
                    currentStatus.copy(
                        lifecycle = CapacitorVpnLifecycle.ERROR,
                        error = error.message ?: "VPN consent launch failed",
                    ),
                )
            }
            failureUpdate?.let(::dispatchStatus)
            pending.complete(Result.failure(error))
        }
    }

    /**
     * Start a product connection. If Android consent is needed, the callback is
     * retained until [onVpnPermissionResult] receives the Activity result.
     */
    fun connect(
        nodeId: String? = null,
        configJson: String? = null,
        callback: (Result<CapacitorVpnStatus>) -> Unit,
    ) {
        val mode = dependencies.settings().mode
        if (mode != CapacitorVpnMode.TUNNEL) {
            val message = if (mode == CapacitorVpnMode.PROXY) {
                "Android proxy-only mode is unsupported without product route proof"
            } else {
                "connection mode is off"
            }
            callback(Result.failure(UnsupportedOperationException(message)))
            return
        }

        val request: PendingConnect
        var consentRequired = false
        val statusUpdate = synchronized(stateLock) {
            if (pendingConnect != null || pendingPermission != null) {
                callback(Result.failure(IllegalStateException("connection is already in progress")))
                return
            }
            operationGeneration += 1
            request = PendingConnect(operationGeneration, mode, nodeId, configJson, callback)
            activeConnectOperationId = request.operationId
            pendingConnect = request
            consentRequired = !dependencies.hasVpnConsent()
            if (consentRequired) {
                updateStatusLocked(CapacitorVpnStatus.permissionRequired(mode))
            } else {
                updateStatusLocked(CapacitorVpnStatus.connecting(mode))
            }
        }
        dispatchStatus(statusUpdate)
        if (consentRequired) {
            launchConsentForConnect(request)
        } else {
            executor.execute { executeConnect(request) }
        }
    }

    /** Deliver the final result of the ActivityResultLauncher consent flow. */
    fun onVpnPermissionResult(operationToken: Long, granted: Boolean) {
        val permissionOnly = synchronized(stateLock) {
            pendingPermission
                ?.takeIf { it.operationToken == operationToken }
                ?.also { pendingPermission = null }
        }
        if (permissionOnly != null) {
            if (granted && dependencies.hasVpnConsent()) {
                val restored = currentStatusAfterPermissionGrant()
                val restoredUpdate = synchronized(stateLock) { updateStatusLocked(restored) }
                dispatchStatus(restoredUpdate)
                permissionOnly.complete(Result.success(true))
            } else {
                permissionOnly.complete(Result.failure(VpnPermissionRequiredException()))
            }
            return
        }
        var rejection: PendingConnect? = null
        var statusUpdate: StatusUpdate? = null
        val request = synchronized(stateLock) {
            val value = pendingConnect ?: return
            if (value.operationId != operationToken) return
            if (!granted || !dependencies.hasVpnConsent()) {
                pendingConnect = null
                activeConnectOperationId = null
                operationGeneration += 1
                statusUpdate = updateStatusLocked(CapacitorVpnStatus.permissionRequired(dependencies.settings().mode))
                rejection = value
                null
            } else {
                statusUpdate = updateStatusLocked(CapacitorVpnStatus.connecting(value.mode))
                value
            }
        }
        statusUpdate?.let(::dispatchStatus)
        rejection?.complete(Result.failure(VpnPermissionRequiredException()))
        if (request != null) executor.execute { executeConnect(request) }
    }

    fun disconnect(callback: (Result<CapacitorVpnStatus>) -> Unit) {
        lateinit var disconnectingUpdate: StatusUpdate
        val cancelled = synchronized(stateLock) {
            operationGeneration += 1
            activeConnectOperationId = null
            val connect = pendingConnect
            val permission = pendingPermission
            pendingConnect = null
            pendingPermission = null
            disconnectingUpdate = updateStatusLocked(CapacitorVpnStatus.disconnecting(dependencies.settings().mode))
            connect to permission
        }
        dispatchStatus(disconnectingUpdate)
        cancelled.first?.complete(Result.failure(OperationCancelledException()))
        cancelled.second?.complete(Result.failure(IllegalStateException("VPN permission request cancelled")))
        executor.execute {
            val failures = cleanupConnectionArtifacts(stopTunnel = shouldStopTunnel())
            if (failures.isEmpty()) {
                val update = synchronized(stateLock) {
                    updateStatusLocked(CapacitorVpnStatus.disconnected(supportedPersistedMode()))
                }
                dispatchStatus(update)
                val disconnected = update.status
                callback(Result.success(disconnected))
            } else {
                val error = CleanupException(failures)
                val update = synchronized(stateLock) {
                    updateStatusLocked(currentStatus.copy(
                        lifecycle = CapacitorVpnLifecycle.ERROR,
                        transportConnected = dependencies.isTransportConnected(),
                        systemVpnConnected = dependencies.isTunnelActive(),
                        error = error.message,
                    ))
                }
                dispatchStatus(update)
                val failed = update.status
                callback(Result.failure(errorWithStatus(error, failed)))
            }
        }
    }

    /** Rebuild lifecycle state away from the Activity main thread after recreation. */
    fun reconcileAfterRestart(callback: (Result<CapacitorVpnStatus>) -> Unit = {}) {
        val reconciliationId = synchronized(stateLock) {
            operationGeneration += 1
            operationGeneration
        }
        executor.execute {
            callback(runCatching { executeRestartReconciliation(reconciliationId) })
        }
    }

    /** Drop Activity-owned callbacks and cancel only work that cannot survive recreation. */
    fun detachUi() {
        statusListener = null
        val abandoned = synchronized(stateLock) {
            operationGeneration += 1
            activeConnectOperationId = null
            val connect = pendingConnect
            val permission = pendingPermission
            pendingConnect = null
            pendingPermission = null
            connect to permission
        }
        abandoned.first?.abandon()
        abandoned.second?.abandon()
        if (abandoned.first != null) executor.execute { cleanupConnectionArtifacts(stopTunnel = true) }
    }

    fun statusJson(): JSONObject = status().toJson()

    /** Reconcile asynchronous service callbacks with the React-facing state. */
    fun onTunnelStatus(status: VpnStatus) {
        when (status) {
            VpnStatus.TUNNEL_ACTIVE -> {
                val update = synchronized(stateLock) {
                    if (currentStatus.lifecycle == CapacitorVpnLifecycle.CONNECTING && currentStatus.transportConnected) {
                        updateStatusLocked(CapacitorVpnStatus.connected(
                            CapacitorVpnMode.TUNNEL,
                            transport = true,
                            systemVpn = true,
                            detail = currentStatus.detail,
                            selectedNodeId = currentStatus.selectedNodeId,
                        ))
                    } else null
                }
                update?.let(::dispatchStatus)
            }
            VpnStatus.TUNNEL_LOST, VpnStatus.CALL_FAILED, VpnStatus.CALL_DISCONNECTED -> {
                var rejectedConnect: PendingConnect? = null
                var statusUpdate: StatusUpdate? = null
                val shouldDisconnect = synchronized(stateLock) {
                    if (currentStatus.lifecycle == CapacitorVpnLifecycle.CONNECTING) {
                        rejectedConnect = pendingConnect
                        operationGeneration += 1
                        activeConnectOperationId = null
                        pendingConnect = null
                        statusUpdate = updateStatusLocked(currentStatus.copy(
                            lifecycle = CapacitorVpnLifecycle.ERROR,
                            transportConnected = false,
                            systemVpnConnected = false,
                            error = "VPN service failed before becoming active",
                        ))
                        return@synchronized true
                    }
                    if (currentStatus.lifecycle != CapacitorVpnLifecycle.CONNECTED) return@synchronized false
                    statusUpdate = updateStatusLocked(currentStatus.copy(
                        lifecycle = if (status == VpnStatus.CALL_DISCONNECTED) CapacitorVpnLifecycle.DISCONNECTED else CapacitorVpnLifecycle.ERROR,
                        transportConnected = false,
                        systemVpnConnected = false,
                        error = if (status == VpnStatus.CALL_DISCONNECTED) null else "VPN tunnel lost",
                    ))
                    true
                }
                statusUpdate?.let(::dispatchStatus)
                rejectedConnect?.complete(Result.failure(IllegalStateException("VPN service failed before becoming active")))
                if (shouldDisconnect) executor.execute { cleanupConnectionArtifacts(stopTunnel = true) }
            }
            else -> Unit
        }
    }

    private fun executeConnect(request: PendingConnect) {
        var tunnelStartAttempted = false
        try {
            ensureOperationActive(request)
            publishForActiveOperation(request, CapacitorVpnStatus.connecting(request.mode))
            val transportConnection = dependencies.connectTransport(request.nodeId, request.configJson)
            ensureOperationActive(request)
            require(transportConnection.selectedNodeId.isNotBlank()) { "connected transport did not confirm a selected node" }
            require(request.nodeId.isNullOrBlank() || transportConnection.selectedNodeId == request.nodeId) {
                "selected node mismatch between request and connected transport"
            }
            publishForActiveOperation(
                request,
                currentStatus.copy(
                    mode = request.mode,
                    transportConnected = true,
                    detail = transportConnection.status,
                    selectedNodeId = transportConnection.selectedNodeId,
                ),
            )
            check(dependencies.verifySocksPayload(transportConnection.endpoint)) { "SOCKS payload verification failed" }
            ensureOperationActive(request)

            tunnelStartAttempted = true
            val split = getSplitRouting()
            check(dependencies.startTunnel(split, transportConnection.endpoint)) { "TunnelVpnService failed to start" }
            ensureOperationActive(request)
            check(dependencies.awaitTunnelActive(tunnelReadyTimeoutMs)) {
                "TunnelVpnService did not publish TUNNEL_ACTIVE"
            }
            ensureOperationActive(request)
            val connected = CapacitorVpnStatus.connected(
                request.mode,
                transport = true,
                systemVpn = true,
                detail = transportConnection.status,
                selectedNodeId = transportConnection.selectedNodeId,
            )
            publishForActiveOperation(request, connected)
            request.complete(Result.success(connected))
            synchronized(stateLock) {
                if (isOperationActiveLocked(request)) {
                    pendingConnect = null
                    activeConnectOperationId = null
                }
            }
        } catch (error: Throwable) {
            if (tunnelStartAttempted) {
                runCatching { dependencies.stopTunnel() }
            }
            runCatching { dependencies.disconnectTransport() }
            val failureUpdate = synchronized(stateLock) {
                if (!isOperationActiveLocked(request)) return@synchronized null
                pendingConnect = null
                activeConnectOperationId = null
                updateStatusLocked(currentStatus.copy(
                    lifecycle = CapacitorVpnLifecycle.ERROR,
                    mode = request.mode,
                    transportConnected = false,
                    systemVpnConnected = false,
                    error = error.message ?: "connect failed",
                ))
            }
            failureUpdate?.let(::dispatchStatus)
            val failed = failureUpdate?.status
            request.complete(Result.failure(if (failed == null) error else errorWithStatus(error, failed)))
        }
    }

    private fun launchConsentForConnect(request: PendingConnect) {
        synchronized(stateLock) {
            if (!isOperationActiveLocked(request)) return
        }
        try {
            dependencies.requestVpnConsent(request.operationId)
        } catch (error: Throwable) {
            val update = synchronized(stateLock) {
                if (!isOperationActiveLocked(request)) return@synchronized null
                pendingConnect = null
                activeConnectOperationId = null
                operationGeneration += 1
                updateStatusLocked(
                    currentStatus.copy(
                        lifecycle = CapacitorVpnLifecycle.ERROR,
                        error = error.message ?: "VPN consent launch failed",
                    ),
                )
            }
            update?.let(::dispatchStatus)
            request.complete(Result.failure(error))
        }
    }

    private fun executeRestartReconciliation(reconciliationId: Long): CapacitorVpnStatus {
        ensureGenerationCurrent(reconciliationId)
        val persistedMode = dependencies.settings().mode
        val transportState = dependencies.transportState()
        val transport = transportState == CapacitorTransportState.CONNECTED
        val tunnel = dependencies.isTunnelActive()
        val tunnelStarted = dependencies.isTunnelStartedOrInProgress()
        ensureGenerationCurrent(reconciliationId)
        if (transport && tunnel) {
            dependencies.settings().mode = CapacitorVpnMode.TUNNEL
            return publishForGeneration(
                reconciliationId,
                CapacitorVpnStatus.connected(CapacitorVpnMode.TUNNEL, transport = true, systemVpn = true),
            )
        }

        if (tunnel && transportState == CapacitorTransportState.RECOVERING) {
            dependencies.settings().mode = CapacitorVpnMode.TUNNEL
            return publishForGeneration(
                reconciliationId,
                CapacitorVpnStatus.connecting(CapacitorVpnMode.TUNNEL).copy(systemVpnConnected = true),
            )
        }

        if (tunnelStarted && !transport) {
            ensureGenerationCurrent(reconciliationId)
            val failures = cleanupConnectionArtifacts(stopTunnel = true)
            if (failures.isNotEmpty()) throw CleanupException(failures)
        } else if (transport) {
            ensureGenerationCurrent(reconciliationId)
            val failures = cleanupConnectionArtifacts(stopTunnel = tunnelStarted)
            if (failures.isNotEmpty()) throw CleanupException(failures)
        }

        ensureGenerationCurrent(reconciliationId)
        val normalizedMode = supportedPersistedMode()
        val status = if (persistedMode == CapacitorVpnMode.PROXY) {
            CapacitorVpnStatus.disconnected(normalizedMode).copy(
                lifecycle = CapacitorVpnLifecycle.ERROR,
                error = "Persisted Android proxy-only mode is unsupported and was reset",
            )
        } else {
            CapacitorVpnStatus.disconnected(normalizedMode)
        }
        return publishForGeneration(reconciliationId, status)
    }

    private fun cleanupConnectionArtifacts(stopTunnel: Boolean): List<Throwable> {
        val failures = mutableListOf<Throwable>()
        if (stopTunnel) {
            runCatching { dependencies.stopTunnel() }.exceptionOrNull()?.let(failures::add)
        }
        runCatching { dependencies.disconnectTransport() }.exceptionOrNull()?.let(failures::add)
        return failures
    }

    private fun shouldStopTunnel(): Boolean = runCatching {
        dependencies.isTunnelStartedOrInProgress() || currentStatus.systemVpnConnected || currentStatus.mode == CapacitorVpnMode.TUNNEL
    }.getOrDefault(true)

    /** Restore live connection truth after permission-only UI completes. */
    private fun currentStatusAfterPermissionGrant(): CapacitorVpnStatus {
        val transportConnected = dependencies.isTransportConnected()
        val tunnelConnected = dependencies.isTunnelActive()
        return if (transportConnected && tunnelConnected) {
            CapacitorVpnStatus.connected(CapacitorVpnMode.TUNNEL, transport = true, systemVpn = true)
        } else {
            CapacitorVpnStatus.disconnected(supportedPersistedMode())
        }
    }

    private fun supportedPersistedMode(): CapacitorVpnMode {
        val persisted = dependencies.settings().mode
        if (persisted == CapacitorVpnMode.PROXY) {
            dependencies.settings().mode = CapacitorVpnMode.TUNNEL
            return CapacitorVpnMode.TUNNEL
        }
        return persisted
    }

    private fun ensureOperationActive(request: PendingConnect) {
        synchronized(stateLock) {
            if (!isOperationActiveLocked(request)) throw OperationCancelledException()
        }
    }

    private fun ensureGenerationCurrent(operationId: Long) {
        synchronized(stateLock) {
            if (operationGeneration != operationId) throw OperationCancelledException()
        }
    }

    private fun publishForGeneration(operationId: Long, status: CapacitorVpnStatus): CapacitorVpnStatus {
        val update = synchronized(stateLock) {
            if (operationGeneration != operationId) throw OperationCancelledException()
            updateStatusLocked(status)
        }
        dispatchStatus(update)
        return update.status
    }

    private fun publishForActiveOperation(request: PendingConnect, status: CapacitorVpnStatus) {
        val update = synchronized(stateLock) {
            if (!isOperationActiveLocked(request)) throw OperationCancelledException()
            updateStatusLocked(status)
        }
        dispatchStatus(update)
    }

    private fun isOperationActiveLocked(request: PendingConnect): Boolean =
        pendingConnect === request &&
            activeConnectOperationId == request.operationId &&
            operationGeneration == request.operationId

    private fun updateStatusLocked(status: CapacitorVpnStatus): StatusUpdate {
        currentStatus = status
        return StatusUpdate(status, statusListener)
    }

    private fun dispatchStatus(update: StatusUpdate) {
        runCatching { update.listener?.invoke(update.status) }
    }

    internal fun isStateLockHeldByCurrentThread(): Boolean = Thread.holdsLock(stateLock)

    private data class StatusUpdate(
        val status: CapacitorVpnStatus,
        val listener: ((CapacitorVpnStatus) -> Unit)?,
    )

    private fun errorWithStatus(error: Throwable, status: CapacitorVpnStatus): Throwable {
        // Keep the callback failure platform-neutral. The plugin publishes the
        // structured status separately, and JVM tests must not depend on the
        // Android framework's non-mocked JSONObject implementation.
        return error
    }

    private class PendingConnect(
        val operationId: Long,
        val mode: CapacitorVpnMode,
        val nodeId: String?,
        val configJson: String?,
        private val callback: (Result<CapacitorVpnStatus>) -> Unit,
    ) {
        private val completed = AtomicBoolean(false)

        fun complete(result: Result<CapacitorVpnStatus>) {
            if (completed.compareAndSet(false, true)) callback(result)
        }

        fun abandon() {
            completed.set(true)
        }
    }

    private class PendingPermission(
        val operationToken: Long,
        private val callback: (Result<Boolean>) -> Unit,
    ) {
        private val completed = AtomicBoolean(false)

        fun complete(result: Result<Boolean>) {
            if (completed.compareAndSet(false, true)) callback(result)
        }

        fun abandon() {
            completed.set(true)
        }
    }

    companion object {
        const val DEFAULT_TUNNEL_READY_TIMEOUT_MS = 10_000L
    }
}

enum class CapacitorVpnMode {
    OFF,
    PROXY,
    TUNNEL;

    companion object {
        fun parse(raw: String?): CapacitorVpnMode = when (raw?.trim()?.lowercase()) {
            "proxy", "socks" -> PROXY
            "tunnel", "vpn" -> TUNNEL
            else -> OFF
        }
    }
}

enum class CapacitorVpnLifecycle {
    DISCONNECTED,
    PERMISSION_REQUIRED,
    CONNECTING,
    CONNECTED,
    DISCONNECTING,
    ERROR,
}

data class CapacitorVpnStatus(
    val lifecycle: CapacitorVpnLifecycle,
    val mode: CapacitorVpnMode,
    val transportConnected: Boolean,
    val systemVpnConnected: Boolean,
    val error: String? = null,
    val detail: JSONObject? = null,
    val selectedNodeId: String? = null,
) {
    fun toJson(): JSONObject = JSONObject()
        .put("lifecycle", lifecycle.name.lowercase())
        .put("state", lifecycle.name.lowercase())
        .put("status", lifecycle.name.lowercase())
        .put("mode", mode.name.lowercase())
        .put("active", lifecycle == CapacitorVpnLifecycle.CONNECTED)
        .put("connected", lifecycle == CapacitorVpnLifecycle.CONNECTED)
        .put("transport_connected", transportConnected)
        .put("system_vpn_connected", systemVpnConnected)
        .put("system_vpn_state", if (systemVpnConnected) "connected" else if (lifecycle == CapacitorVpnLifecycle.PERMISSION_REQUIRED) "permission_required" else "disconnected")
        .apply {
            if (!error.isNullOrBlank()) put("error", error)
            if (detail != null) put("transport", detail)
            if (!selectedNodeId.isNullOrBlank()) put("selected_node_id", selectedNodeId)
        }

    companion object {
        fun disconnected(mode: CapacitorVpnMode) = CapacitorVpnStatus(CapacitorVpnLifecycle.DISCONNECTED, mode, false, false)
        fun permissionRequired(mode: CapacitorVpnMode) = CapacitorVpnStatus(CapacitorVpnLifecycle.PERMISSION_REQUIRED, mode, false, false)
        fun connecting(mode: CapacitorVpnMode) = CapacitorVpnStatus(CapacitorVpnLifecycle.CONNECTING, mode, false, false)
        fun disconnecting(mode: CapacitorVpnMode) = CapacitorVpnStatus(CapacitorVpnLifecycle.DISCONNECTING, mode, false, false)
        fun connected(
            mode: CapacitorVpnMode,
            transport: Boolean,
            systemVpn: Boolean,
            detail: JSONObject? = null,
            selectedNodeId: String? = null,
        ) = CapacitorVpnStatus(
            CapacitorVpnLifecycle.CONNECTED,
            mode,
            transport,
            systemVpn,
            detail = detail,
            selectedNodeId = selectedNodeId,
        )
    }
}

data class CapacitorVpnSplitSettings(val mode: SplitTunnelingMode, val packages: Set<String>)

data class CapacitorTransportConnection(
    val status: JSONObject,
    val endpoint: LoopbackSocksEndpoint,
    val selectedNodeId: String,
)

fun parseCapacitorSplitMode(raw: String?): SplitTunnelingMode = when (raw?.trim()?.lowercase()) {
    "none" -> SplitTunnelingMode.NONE
    "bypass" -> SplitTunnelingMode.BYPASS
    "only" -> SplitTunnelingMode.ONLY
    else -> throw IllegalArgumentException("unsupported split-routing mode: ${raw.orEmpty()}")
}

private fun validateCapacitorSplitPackages(mode: SplitTunnelingMode, packages: Set<String>, appPackageName: String): Set<String> {
    val normalized = packages.map(String::trim).filter(String::isNotEmpty).toSet()
    require(mode != SplitTunnelingMode.ONLY || normalized.isNotEmpty()) {
        "only split-routing mode requires at least one package"
    }
    val invalid = normalized.firstOrNull { !ANDROID_PACKAGE_NAME.matches(it) }
    require(invalid == null) { "invalid Android package name: $invalid" }
    require(mode != SplitTunnelingMode.ONLY || appPackageName !in normalized) {
        "only split-routing mode cannot include the VPN app package"
    }
    return if (mode == SplitTunnelingMode.NONE) emptySet() else normalized
}

private val ANDROID_PACKAGE_NAME = Regex("^[A-Za-z][A-Za-z0-9_]*(?:\\.[A-Za-z][A-Za-z0-9_]*)+$")

interface CapacitorVpnSettings {
    var mode: CapacitorVpnMode
    var splitMode: SplitTunnelingMode
    var packages: Set<String>
}

enum class CapacitorTransportState { CONNECTED, RECOVERING, DISCONNECTED }

internal fun capacitorTransportState(state: String): CapacitorTransportState = when (state) {
    "connected" -> CapacitorTransportState.CONNECTED
    "connecting", "reconnecting", "degraded" -> CapacitorTransportState.RECOVERING
    else -> CapacitorTransportState.DISCONNECTED
}

/** Platform-independent seams used by the JVM coordinator tests. */
interface CapacitorVpnDependencies {
    fun hasVpnConsent(): Boolean
    fun requestVpnConsent(operationToken: Long)
    fun connectTransport(nodeId: String?, configJson: String?): CapacitorTransportConnection
    fun verifySocksPayload(endpoint: LoopbackSocksEndpoint): Boolean
    fun startTunnel(settings: CapacitorVpnSplitSettings, endpoint: LoopbackSocksEndpoint): Boolean
    fun awaitTunnelActive(timeoutMs: Long): Boolean
    fun isTunnelActive(): Boolean
    fun isTunnelStartedOrInProgress(): Boolean
    fun stopTunnel()
    fun isTransportConnected(): Boolean
    fun transportState(): CapacitorTransportState
    fun disconnectTransport()
    fun appPackageName(): String
    fun settings(): CapacitorVpnSettings
}

class VpnPermissionRequiredException : IllegalStateException("VPN permission is required")

private class OperationCancelledException : IllegalStateException("connection cancelled")

private class CleanupException(failures: List<Throwable>) : IllegalStateException(
    failures.joinToString(prefix = "connection cleanup failed: ", separator = "; ") { it.message ?: it::class.java.simpleName },
)

/** Android implementation that reuses the existing Go controller and VpnService. */
class AndroidCapacitorVpnDependencies(private val context: Context) : CapacitorVpnDependencies {
    private val logTag = "CapacitorVpnCoordinator"

    @Volatile
    var launchVpnConsent: ((Long) -> Unit)? = null

    override fun hasVpnConsent(): Boolean = VpnService.prepare(context) == null

    override fun requestVpnConsent(operationToken: Long) {
        launchVpnConsent?.invoke(operationToken) ?: throw IllegalStateException("VPN consent launcher is not registered")
    }

    override fun connectTransport(nodeId: String?, configJson: String?): CapacitorTransportConnection {
        val status = GoRuntimeController.connect(context, nodeId, configJson)
        val selectedNodeId = bypass.whitelist.planner.requireConfirmedTransportNode(
            nodeId,
            status.optString("active_node_id").trim(),
        )
        return CapacitorTransportConnection(
            status = status,
            endpoint = GoRuntimeController.runtimeSocksEndpoint(status, configJson),
            selectedNodeId = selectedNodeId,
        )
    }

    override fun verifySocksPayload(endpoint: LoopbackSocksEndpoint): Boolean {
        var lastError: Throwable? = null
        val verified = retrySocksPayloadProbe(
            attempts = SOCKS_PAYLOAD_PROBE_ATTEMPTS,
            retryDelayMs = SOCKS_PAYLOAD_PROBE_RETRY_DELAY_MS,
        ) {
            runCatching {
                SocksPayloadProbe.verify(
                    host = endpoint.host,
                    port = endpoint.port,
                    user = endpoint.username,
                    pass = endpoint.password,
                )
            }.onFailure { lastError = it }.getOrDefault(false)
        }
        if (!verified) {
            val detail = lastError?.message ?: lastError?.javaClass?.simpleName ?: "probe returned false"
            Log.e(logTag, "SOCKS payload probe failed endpoint=${endpoint.host}:${endpoint.port} error=$detail")
        }
        return verified
    }

    override fun startTunnel(settings: CapacitorVpnSplitSettings, endpoint: LoopbackSocksEndpoint): Boolean {
        // Prefs is the existing TunnelVpnService configuration source. The
        // coordinator persists it before this call, so Builder applies the
        // same allowed/disallowed application rules as the legacy UI.
        val endpointPayload = endpoint.toServicePayload()
        val intent = Intent(context, TunnelVpnService::class.java).apply {
            putExtra(TunnelVpnService.EXTRA_RUNTIME_SOCKS_HOST, endpointPayload.host)
            putExtra(TunnelVpnService.EXTRA_RUNTIME_SOCKS_PORT, endpointPayload.port)
        }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            ContextCompat.startForegroundService(context, intent)
        } else {
            context.startService(intent)
        }
        return true
    }

    override fun awaitTunnelActive(timeoutMs: Long): Boolean {
        val deadline = System.currentTimeMillis() + timeoutMs
        while (System.currentTimeMillis() < deadline) {
            val service = TunnelVpnService.instance
            if (service?.isAuthoritativelyActive() == true) return true
            Thread.sleep(25)
        }
        return TunnelVpnService.instance?.isAuthoritativelyActive() == true
    }

    override fun isTunnelActive(): Boolean = TunnelVpnService.instance?.isAuthoritativelyActive() == true

    override fun isTunnelStartedOrInProgress(): Boolean = TunnelVpnService.instance?.let {
        it.isRunning || it.startInProgress || it.stopInProgress
    } == true

    override fun stopTunnel() {
        val stoppingService = TunnelVpnService.instance
        TunnelVpnService.requestStop(context)
        awaitTunnelStopped(
            timeoutMs = 2_000L,
            isStopped = {
                val service = TunnelVpnService.instance
                service == null || (!service.isRunning && !service.startInProgress && !service.stopInProgress)
            },
            sleep = { Thread.sleep(25) },
        )
        requireTunnelStopSucceeded(stoppingService?.stopFailure)
    }

    override fun isTransportConnected(): Boolean = runCatching {
        GoRuntimeController.status(context).optString("state") == "connected"
    }.getOrDefault(false)

    override fun transportState(): CapacitorTransportState = runCatching {
        capacitorTransportState(GoRuntimeController.status(context).optString("state"))
    }.getOrDefault(CapacitorTransportState.DISCONNECTED)

    override fun disconnectTransport() {
        GoRuntimeController.disconnect(context)
    }

    override fun appPackageName(): String = context.packageName

    override fun settings(): CapacitorVpnSettings = AndroidCapacitorVpnSettings()

    private class AndroidCapacitorVpnSettings : CapacitorVpnSettings {
        override var mode: CapacitorVpnMode
            get() = CapacitorVpnMode.parse(Prefs.capacitorVpnMode)
            set(value) { Prefs.capacitorVpnMode = value.name.lowercase() }
        override var splitMode: SplitTunnelingMode
            get() = Prefs.splitTunnelingMode
            set(value) { Prefs.splitTunnelingMode = value }
        override var packages: Set<String>
            get() = Prefs.splitTunnelingPackages
            set(value) { Prefs.splitTunnelingPackages = value }
    }
}

internal fun awaitTunnelStopped(
    timeoutMs: Long,
    isStopped: () -> Boolean,
    sleep: () -> Unit,
) {
    val deadlineNanos = System.nanoTime() + timeoutMs * 1_000_000L
    while (System.nanoTime() < deadlineNanos) {
        if (isStopped()) return
        sleep()
    }
    if (!isStopped()) throw IllegalStateException("TunnelVpnService stop timed out after ${timeoutMs}ms")
}

internal fun requireTunnelStopSucceeded(failure: Throwable?) {
    if (failure != null) throw IllegalStateException("TunnelVpnService stop failed", failure)
}

private const val SOCKS_PAYLOAD_PROBE_ATTEMPTS = 3
private const val SOCKS_PAYLOAD_PROBE_RETRY_DELAY_MS = 1_000L
internal const val SOCKS_PAYLOAD_PROBE_HOST = "api.ipify.org"
internal const val SOCKS_PAYLOAD_PROBE_PORT = 443
internal const val SOCKS_PAYLOAD_PROBE_PATH = "/?format=text"
internal const val SOCKS_PAYLOAD_PROBE_TLS = true

/** Retry the real SOCKS payload probe while a freshly negotiated carrier warms up. */
internal fun retrySocksPayloadProbe(
    attempts: Int,
    retryDelayMs: Long,
    sleep: (Long) -> Unit = Thread::sleep,
    probe: () -> Boolean,
): Boolean {
    require(attempts > 0) { "SOCKS payload probe attempts must be positive" }
    require(retryDelayMs >= 0) { "SOCKS payload probe delay cannot be negative" }
    repeat(attempts) { attempt ->
        if (runCatching(probe).getOrDefault(false)) return true
        if (attempt + 1 < attempts && retryDelayMs > 0) sleep(retryDelayMs)
    }
    return false
}

/** A real SOCKS5 handshake plus payload request; no direct-network fallback. */
private object SocksPayloadProbe {
    fun verify(host: String, port: Int, user: String, pass: String): Boolean {
        Socket().use { socket ->
            socket.connect(InetSocketAddress(host, port), 5_000)
            socket.soTimeout = 10_000
            val input = socket.getInputStream()
            val output = socket.getOutputStream()
            val authRequired = user.isNotEmpty()
            output.write(if (authRequired) byteArrayOf(0x05, 0x02, 0x00, 0x02) else byteArrayOf(0x05, 0x01, 0x00))
            output.flush()
            check(input.read() == 0x05) { "SOCKS version mismatch" }
            when (input.read()) {
                0x00 -> Unit
                0x02 -> {
                    val userBytes = user.toByteArray(Charsets.US_ASCII)
                    val passBytes = pass.toByteArray(Charsets.US_ASCII)
                    output.write(byteArrayOf(0x01, userBytes.size.toByte()))
                    output.write(userBytes)
                    output.write(byteArrayOf(passBytes.size.toByte()))
                    output.write(passBytes)
                    output.flush()
                    check(input.read() == 0x01 && input.read() == 0x00) { "SOCKS authentication failed" }
                }
                else -> error("SOCKS authentication method rejected")
            }
            val target = SOCKS_PAYLOAD_PROBE_HOST.toByteArray(Charsets.US_ASCII)
            output.write(byteArrayOf(0x05, 0x01, 0x00, 0x03, target.size.toByte()))
            output.write(target)
            output.write(byteArrayOf(
                (SOCKS_PAYLOAD_PROBE_PORT shr 8).toByte(),
                (SOCKS_PAYLOAD_PROBE_PORT and 0xff).toByte(),
            ))
            output.flush()
            val reply = ByteArray(4)
            input.readFully(reply)
            check(reply[0].toInt() == 0x05 && reply[1].toInt() == 0x00) { "SOCKS connect failed" }
            when (reply[3].toInt()) {
                0x01 -> input.skipFully(4)
                0x03 -> input.skipFully(input.read().toLong())
                0x04 -> input.skipFully(16)
                else -> error("SOCKS reply address type rejected")
            }
            input.skipFully(2)
            val secureSocket = if (SOCKS_PAYLOAD_PROBE_TLS) {
                (SSLSocketFactory.getDefault() as SSLSocketFactory)
                    .createSocket(socket, SOCKS_PAYLOAD_PROBE_HOST, SOCKS_PAYLOAD_PROBE_PORT, false)
                    .also { (it as SSLSocket).startHandshake() }
            } else {
                null
            }
            try {
                val payloadInput = (secureSocket ?: socket).getInputStream()
                val payloadOutput = (secureSocket ?: socket).getOutputStream()
                payloadOutput.write("GET $SOCKS_PAYLOAD_PROBE_PATH HTTP/1.1\r\nHost: $SOCKS_PAYLOAD_PROBE_HOST\r\nConnection: close\r\n\r\n".toByteArray(Charsets.US_ASCII))
                payloadOutput.flush()
                val body = payloadInput.bufferedReader(Charsets.US_ASCII).readText().substringAfter("\r\n\r\n", "").trim()
                return Regex("^[0-9a-fA-F:.]{3,64}$").matches(body)
            } finally {
                secureSocket?.close()
            }
        }
    }

    private fun java.io.InputStream.readFully(buffer: ByteArray) {
        var offset = 0
        while (offset < buffer.size) {
            val count = read(buffer, offset, buffer.size - offset)
            if (count < 0) throw IOException("SOCKS reply truncated")
            offset += count
        }
    }

    private fun java.io.InputStream.skipFully(bytes: Long) {
        var remaining = bytes
        while (remaining > 0) {
            val skipped = skip(remaining)
            if (skipped > 0) remaining -= skipped else if (read() < 0) throw IOException("SOCKS reply truncated") else remaining--
        }
    }
}
