package bypass.whitelist

import android.content.Context
import android.content.Intent
import android.util.Log
import org.json.JSONObject
import java.io.File
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Debug-only GUI launch contract used by package smoke tests.
 *
 * A test starts the normal launcher with [GuiTestLaunchRequest.ACTION_CONNECT], a
 * node id and a bounded run id. Production builds ignore the extras entirely.
 */
data class GuiTestLaunchRequest(val nodeId: String?, val runId: String) {
    companion object {
        const val ACTION_CONNECT = "bypass.whitelist.action.GUI_TEST_CONNECT"
        const val EXTRA_AUTO_CONNECT = "wt.test.auto_connect"
        const val EXTRA_NODE_ID = "wt.test.node_id"
        const val EXTRA_RUN_ID = "wt.test.run_id"

        private val runIdPattern = Regex("^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$")
        private val nodeIdPattern = Regex("^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")

        /** Parse only the explicit, debug-safe test launch shape. */
        fun from(action: String?, autoConnect: Boolean, nodeId: String?, runId: String?, debugSafe: Boolean): GuiTestLaunchRequest? {
            if (!debugSafe || action != ACTION_CONNECT || !autoConnect) return null
            val normalizedRunId = runId?.trim()?.takeIf { runIdPattern.matches(it) } ?: return null
            // The launch path receives a selector only, never a URI or credentials.
            val normalizedNodeId = nodeId?.trim()?.takeIf { nodeIdPattern.matches(it) } ?: return null
            return GuiTestLaunchRequest(normalizedNodeId, normalizedRunId)
        }

        fun from(intent: Intent?, debugSafe: Boolean): GuiTestLaunchRequest? = from(
            action = intent?.action,
            autoConnect = intent?.getBooleanExtra(EXTRA_AUTO_CONNECT, false) == true,
            nodeId = intent?.getStringExtra(EXTRA_NODE_ID),
            runId = intent?.getStringExtra(EXTRA_RUN_ID),
            debugSafe = debugSafe,
        )
    }
}

/** The only lifecycle surface that installed debug launch mode may exercise. */
internal interface GuiTestLaunchLifecycle {
    fun hasVpnConsent(): Boolean
    fun connect(nodeId: String?, callback: (Result<CapacitorVpnStatus>) -> Unit)
    fun disconnect(callback: (Result<CapacitorVpnStatus>) -> Unit)
}

internal data class GuiTestLaunchReport(
    val runId: String,
    val nodeId: String?,
    val startedAtMs: Long,
    val finishedAtMs: Long,
    val ok: Boolean,
    val state: String? = null,
    val selectedNodeId: String? = null,
    val tunActive: Boolean = false,
    val errorCode: String? = null,
    val error: String? = null,
    val cleanupOk: Boolean = false,
    val cleanupState: String = "error",
) {
    fun toJson(): JSONObject {
        val result = JSONObject()
        result.put("schemaVersion", 2)
        result.put("runId", runId)
        result.put("nodeId", nodeId ?: JSONObject.NULL)
        result.put("startedAtMs", startedAtMs)
        result.put("finishedAtMs", finishedAtMs)
        result.put("lifecycleOwner", "coordinator")
        result.put("ok", ok)
        state?.let { result.put("state", it) }
        selectedNodeId?.let { result.put("selectedNodeId", it) }
        result.put("tunActive", tunActive)
        errorCode?.let { result.put("errorCode", it) }
        error?.let { result.put("error", it) }
        result.put("cleanupOk", cleanupOk)
        result.put("cleanupState", cleanupState)
        val proofBoundary = JSONObject()
        proofBoundary.put("coordinatorOwnsVpnServiceLifecycle", true)
        proofBoundary.put("installedTunAndSocksPayloadProof", false)
        proofBoundary.put("note", "Installed debug smoke must separately prove TUN establishment and payload transfer")
        result.put("proofBoundary", proofBoundary)
        return result
    }
}

private class CoordinatorGuiTestLaunchLifecycle(
    private val coordinator: CapacitorVpnCoordinator,
) : GuiTestLaunchLifecycle {
    override fun hasVpnConsent(): Boolean = coordinator.hasVpnConsentForNonInteractiveConnect()

    override fun connect(nodeId: String?, callback: (Result<CapacitorVpnStatus>) -> Unit) =
        coordinator.connect(nodeId = nodeId, callback = callback)

    override fun disconnect(callback: (Result<CapacitorVpnStatus>) -> Unit) =
        coordinator.disconnect(callback)
}

/**
 * Execute one bounded test launch through the product coordinator.
 *
 * The helper is platform-independent so the JVM tests prove the ordering and
 * fail-closed consent contract without pretending to prove an installed TUN.
 */
internal fun executeGuiTestLaunch(
    request: GuiTestLaunchRequest,
    lifecycle: GuiTestLaunchLifecycle,
    startedAtMs: Long = System.currentTimeMillis(),
    nowMs: () -> Long = System::currentTimeMillis,
    writeResult: (GuiTestLaunchReport) -> Unit,
) {
    var ok = false
    var state: String? = null
    var selectedNodeId: String? = null
    var tunActive = false
    var errorCode: String? = null
    var errorMessage: String? = null
    val finished = AtomicBoolean(false)

    fun finish(cleanup: Result<CapacitorVpnStatus>) {
        if (!finished.compareAndSet(false, true)) return
        val cleanupStatus = cleanup.getOrNull()
        val cleanupOk = cleanup.isSuccess && cleanupStatus?.lifecycle == CapacitorVpnLifecycle.DISCONNECTED
        if (!cleanupOk && errorCode == null) {
            ok = false
            errorCode = "cleanup_failed"
            errorMessage = redactGuiTestError(cleanup.exceptionOrNull())
        }
        writeResult(
            GuiTestLaunchReport(
                runId = request.runId,
                nodeId = request.nodeId,
                startedAtMs = startedAtMs,
                finishedAtMs = nowMs(),
                ok = ok,
                state = state,
                selectedNodeId = selectedNodeId,
                tunActive = tunActive,
                errorCode = errorCode,
                error = errorMessage,
                cleanupOk = cleanupOk,
                cleanupState = cleanupStatus?.lifecycle?.name?.lowercase() ?: "error",
            ),
        )
    }

    fun cleanup() {
        try {
            lifecycle.disconnect(::finish)
        } catch (error: Throwable) {
            finish(Result.failure(error))
        }
    }

    if (!lifecycle.hasVpnConsent()) {
        ok = false
        errorCode = "vpn_consent_required"
        errorMessage = "VPN consent is unavailable for non-interactive debug launch"
        cleanup()
        return
    }

    try {
        lifecycle.connect(request.nodeId) { outcome ->
            outcome.fold(
                onSuccess = { status ->
                    state = status.lifecycle.name.lowercase()
                    selectedNodeId = status.selectedNodeId
                    tunActive = status.systemVpnConnected
                    val selectionMatches = selectedNodeId != null && (request.nodeId == null || selectedNodeId == request.nodeId)
                    ok = status.lifecycle == CapacitorVpnLifecycle.CONNECTED && selectionMatches
                    if (!selectionMatches) {
                        errorCode = "selected_node_mismatch"
                        errorMessage = "Connected runtime did not confirm the requested node"
                    }
                },
                onFailure = { failure ->
                    ok = false
                    errorCode = "connect_failed"
                    errorMessage = redactGuiTestError(failure)
                },
            )
            cleanup()
        }
    } catch (error: Throwable) {
        ok = false
        errorCode = "connect_failed"
        errorMessage = redactGuiTestError(error)
        cleanup()
    }
}

private fun redactGuiTestError(error: Throwable?): String = error?.message
    ?.replace(Regex("vk1\\.[A-Za-z0-9._-]+"), "[REDACTED_VK_TOKEN]")
    ?.replace(Regex("eyJ[A-Za-z0-9._-]{20,}"), "[REDACTED_JWT]")
    ?.replace(Regex("(?i)(token|cookie|authorization)=\\S+"), "$1=[REDACTED]")
    ?.take(500)
    ?: error?.javaClass?.simpleName
    ?: "unknown error"

/** Runs a test launch off the UI thread and persists one machine-readable result. */
object GuiTestLaunchRunner {
    private const val tag = "WT-GuiTestLaunch"
    private val executor = Executors.newSingleThreadExecutor { runnable ->
        Thread(runnable, "wt-gui-test-launch").apply { isDaemon = true }
    }

    fun maybeRun(context: Context, intent: Intent?, coordinator: CapacitorVpnCoordinator? = null) {
        val request = GuiTestLaunchRequest.from(intent, BuildConfig.DEBUG) ?: return
        val appContext = context.applicationContext
        executor.execute {
            val lifecycle = coordinator?.let(::CoordinatorGuiTestLaunchLifecycle)
            if (lifecycle == null) {
                val now = System.currentTimeMillis()
                writeResult(
                    appContext,
                    request.runId,
                    GuiTestLaunchReport(
                        runId = request.runId,
                        nodeId = request.nodeId,
                        startedAtMs = now,
                        finishedAtMs = now,
                        ok = false,
                        errorCode = "coordinator_unavailable",
                        error = "Capacitor VPN coordinator is unavailable",
                    ).toJson(),
                )
                return@execute
            }
            executeGuiTestLaunch(
                request = request,
                lifecycle = lifecycle,
                writeResult = { report -> writeResult(appContext, request.runId, report.toJson()) },
            )
        }
    }

    private fun writeResult(context: Context, runId: String, result: JSONObject) {
        val directory = File(context.filesDir, "white-transport-test-results")
        if (!directory.exists() && !directory.mkdirs()) {
            Log.e(tag, "cannot create GUI test result directory")
            return
        }
        val destination = File(directory, "$runId.json")
        val temporary = File(directory, ".$runId.json.tmp")
        temporary.writeText(result.toString() + "\n")
        if (!temporary.renameTo(destination)) {
            temporary.copyTo(destination, overwrite = true)
            temporary.delete()
        }
        Log.i(tag, "GUI_TEST_RESULT run_id=$runId path=${destination.absolutePath} ok=${result.optBoolean("ok")}")
    }
}
