package bypass.whitelist.tunnel

import android.app.ActivityManager
import android.content.ComponentName
import android.content.Context
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.os.Build
import android.service.quicksettings.TileService
import android.util.Log
import bypass.whitelist.util.ParamCallback

object TunnelServiceState {
    private const val TAG = "TunnelServiceState"
    private val vpnStatusCallbacks = LinkedHashMap<Any, ParamCallback<VpnStatus>>()

    @Volatile
    var logCallback: ParamCallback<String>? = null

    /** Add or replace one owner's callback without displacing other live UIs. */
    @Synchronized
    fun attachVpnStatusCallback(owner: Any, callback: ParamCallback<VpnStatus>) {
        vpnStatusCallbacks[owner] = callback
    }

    /** Detach only the callback installed by this exact owner instance. */
    @Synchronized
    fun detachVpnStatusCallback(owner: Any) {
        vpnStatusCallbacks.remove(owner)
    }

    /** Publish to a stable snapshot so callbacks may attach or detach safely. */
    fun publishVpnStatus(status: VpnStatus) {
        val callbacks = synchronized(this) { vpnStatusCallbacks.values.toList() }
        publishVpnStatusTo(callbacks, status) { error ->
            Log.w(TAG, "VPN status subscriber failed: ${error.message}")
        }
    }

    @Synchronized
    internal fun vpnStatusSubscriberCount(): Int = vpnStatusCallbacks.size

    fun isTunnelActive(context: Context): Boolean {
        val vpnActive = TunnelVpnService.instance?.let { it.isRunning || it.startInProgress || it.stopInProgress } == true
        val proxyActive = ProxyService.instance?.let { it.isRunning || it.stopInProgress } == true
        return vpnActive || proxyActive
    }

    fun isHeadlessSessionRunning(context: Context): Boolean {
        return HeadlessSessionService.hasLiveSession()
    }

    fun isAnyTunnelComponentRunning(context: Context): Boolean {
        return isTunnelActive(context) || isHeadlessSessionRunning(context)
    }

    fun hasForeignVpn(context: Context): Boolean {
        if (isTunnelActive(context)) return false
        val connectivityManager = context.getSystemService(ConnectivityManager::class.java) ?: return false
        val activeNetwork = connectivityManager.activeNetwork ?: return false
        val capabilities = connectivityManager.getNetworkCapabilities(activeNetwork) ?: return false
        return capabilities.hasTransport(NetworkCapabilities.TRANSPORT_VPN)
    }

    fun requestTileRefresh(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.N) return
        TileService.requestListeningState(context, ComponentName(context, VpnTileService::class.java))
    }
}

/** Deliver one status to every subscriber while isolating owner failures. */
internal fun publishVpnStatusTo(
    callbacks: List<ParamCallback<VpnStatus>>,
    status: VpnStatus,
    onFailure: (Throwable) -> Unit,
) {
    callbacks.forEach { callback -> runCatching { callback(status) }.onFailure(onFailure) }
}
