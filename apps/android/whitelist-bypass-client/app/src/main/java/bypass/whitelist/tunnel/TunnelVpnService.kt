package bypass.whitelist.tunnel

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.ConnectivityManager
import android.net.VpnService
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.ParcelFileDescriptor
import android.util.Log
import bypass.whitelist.CapacitorMainActivity
import bypass.whitelist.R
import bypass.whitelist.util.Callback
import bypass.whitelist.util.DnsMode
import bypass.whitelist.util.Prefs
import bypass.whitelist.util.SocksAuth
import bypass.whitelist.util.Vpn
import bypass.whitelist.planner.GoRuntimeController
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.TimeoutException
import kotlin.concurrent.thread

class TunnelVpnService : VpnService() {

    companion object {
        const val TAG = "TunnelVPN"
        const val CHANNEL_ID = "vpn_channel"
        const val NOTIFICATION_ID = 1
        const val ACTION_STOP = "bypass.whitelist.STOP_VPN"
        const val EXTRA_RUNTIME_SOCKS_HOST = "bypass.whitelist.RUNTIME_SOCKS_HOST"
        const val EXTRA_RUNTIME_SOCKS_PORT = "bypass.whitelist.RUNTIME_SOCKS_PORT"
        const val EXTRA_EXPLICIT_LEGACY_ENDPOINT = "bypass.whitelist.EXPLICIT_LEGACY_ENDPOINT"
        private const val BRIDGE_STOP_TIMEOUT_MS = 2_000L
        @Volatile var instance: TunnelVpnService? = null
        @Volatile var onDisconnect: Callback? = null

        fun requestStop(context: Context) {
            val running = instance?.let { it.isRunning || it.startInProgress || it.stopInProgress } == true
            val intent = Intent(context, TunnelVpnService::class.java)
            try {
                if (running) {
                    context.startService(intent.apply { action = ACTION_STOP })
                } else {
                    context.stopService(intent)
                    TunnelServiceState.requestTileRefresh(context)
                }
            } catch (t: Throwable) {
                Log.w(TAG, "requestStop failed: ${t.message}")
            }
        }
    }

    @Volatile var isRunning: Boolean = false
    @Volatile internal var startInProgress: Boolean = false
    @Volatile internal var stopInProgress: Boolean = false
    @Volatile private var authoritativeStatus: VpnStatus = VpnStatus.CALL_DISCONNECTED
    @Volatile internal var stopFailure: Throwable? = null
    private var vpnFd: ParcelFileDescriptor? = null
    private var tunFdOwnership: DetachedTunFdOwnership? = null
    private var requestedSocksEndpoint: LoopbackSocksEndpoint? = null
    private var tun2socksThread: Thread? = null
    private var tun2socksLifecycle: Tun2SocksHandoffLifecycle? = null
    private var tun2socksStartReservation: Tun2SocksStartReservation? = null
    @Volatile private var tunGeneration: Long = 0

    override fun onCreate() {
        super.onCreate()
        instance = this
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            if (!isRunning && !startInProgress && !stopInProgress) {
                safeStopSelf()
                return START_NOT_STICKY
            }
            stop()
            return START_NOT_STICKY
        }
        val request = resolveTunnelStartRequest(
            runtimeHostPresent = intent?.hasExtra(EXTRA_RUNTIME_SOCKS_HOST) == true,
            runtimeHost = intent?.getStringExtra(EXTRA_RUNTIME_SOCKS_HOST),
            runtimePortPresent = intent?.hasExtra(EXTRA_RUNTIME_SOCKS_PORT) == true,
            runtimePort = intent?.getIntExtra(EXTRA_RUNTIME_SOCKS_PORT, 0) ?: 0,
            explicitLegacy = intent?.getBooleanExtra(EXTRA_EXPLICIT_LEGACY_ENDPOINT, false) == true,
        )
        requestedSocksEndpoint = when (request) {
            is TunnelStartRequest.Runtime -> request.endpoint
            TunnelStartRequest.Legacy -> LoopbackSocksEndpoint.legacy(
                host = Prefs.socksHost,
                port = Prefs.socksPort.toInt(),
                username = SocksAuth.user,
                password = SocksAuth.pass,
            )
            is TunnelStartRequest.Rejected -> {
                failStartup("Rejected VPN service start: ${request.reason}", IllegalArgumentException(request.reason))
                return START_NOT_STICKY
            }
        }
        start()
        return START_NOT_STICKY
    }

    override fun onDestroy() {
        if ((isRunning || startInProgress) && !stopInProgress) {
            stop()
        }
        if (instance === this) {
            instance = null
        }
        authoritativeStatus = VpnStatus.CALL_DISCONNECTED
        stopInProgress = false
        tunFdOwnership?.closeBeforeNativeHandoff()
        tunFdOwnership = null
        releaseTun2SocksStartReservation()
        onDisconnect = null
        super.onDestroy()
    }

    fun updateStatus(status: VpnStatus) {
        authoritativeStatus = status
        val nm = getSystemService(NotificationManager::class.java)
        nm.notify(NOTIFICATION_ID, buildNotification(getString(status.labelRes)))
        TunnelServiceState.publishVpnStatus(status)
        TunnelServiceState.requestTileRefresh(this)
    }

    /**
     * Returns true only after this service has published TUNNEL_ACTIVE. A
     * foreground service process or an established-but-unannounced TUN is not
     * enough to claim system VPN readiness.
     */
    fun isAuthoritativelyActive(): Boolean = isRunning && authoritativeStatus == VpnStatus.TUNNEL_ACTIVE

    @Synchronized
    fun stop() {
        if (stopInProgress) return
        if (!isRunning && !startInProgress) {
            safeStopSelf()
            return
        }
        isRunning = false
        startInProgress = false
        stopInProgress = true
        stopFailure = null
        authoritativeStatus = VpnStatus.STOPPING
        bumpTunGeneration()
        val disconnectCallback = onDisconnect
        val bridgeLifecycle = tun2socksLifecycle

        thread(name = "vpn-stop") {
            val bridgeStopError = stopTun2SocksWithTimeout(bridgeLifecycle)

            try {
                tun2socksThread?.join(1000)
            } catch (e: Exception) {}

            Handler(Looper.getMainLooper()).post {
                try {
                    tun2socksThread = null
                    tun2socksLifecycle = null
                    vpnFd = null
                    tunFdOwnership = null
                    requestedSocksEndpoint = null
                    @Suppress("DEPRECATION")
                    stopForeground(true)
                    stopFailure = bridgeStopError
                    authoritativeStatus = vpnStatusAfterBridgeStop(bridgeStopError)
                    TunnelServiceState.publishVpnStatus(authoritativeStatus)
                    disconnectCallback?.invoke()
                    TunnelServiceState.requestTileRefresh(this@TunnelVpnService)
                    stopSelf()
                } catch (t: Throwable) {
                    stopInProgress = false
                    Log.e(TAG, "Crash during VPN stop: ${t.message}", t)
                }
            }
        }
    }

    private fun stopTun2SocksWithTimeout(lifecycle: Tun2SocksHandoffLifecycle?): Throwable? {
        if (lifecycle == null) {
            tunFdOwnership?.closeBeforeNativeHandoff()
            return IllegalStateException("tun2socks lifecycle is unavailable during stop")
        }
        return lifecycle.requestStopAndAwait(
            stop = ::invokeNativeStopWithTimeout,
            timeoutMillis = BRIDGE_STOP_TIMEOUT_MS,
        )
    }

    private fun invokeNativeStopWithTimeout(): Throwable? {
        val stopDone = CountDownLatch(1)
        var stopError: Throwable? = null
        val pendingStop = Tun2SocksStopPending(
            "tun2socks stop timed out after ${BRIDGE_STOP_TIMEOUT_MS}ms",
        )
        thread(name = "tun2socks-stop") {
            try {
                GoRuntimeController.stopTun2Socks()
            } catch (e: Throwable) {
                stopError = e
                Log.e(TAG, "tun2socks stop error: ${e.message}")
            } finally {
                pendingStop.complete(stopError)
                stopDone.countDown()
            }
        }
        if (!stopDone.await(BRIDGE_STOP_TIMEOUT_MS, TimeUnit.MILLISECONDS)) {
            return pendingStop
        }
        return stopError
    }

    private fun start() {
        if (isRunning || startInProgress) return
        val startReservation = Tun2SocksProcessBarrier.reserveStart()
        if (startReservation == null) {
            failStartup("Rejected VPN start while previous tun2socks stop is still pending", IllegalStateException("native tun2socks stop barrier is active"))
            return
        }
        tun2socksStartReservation = startReservation
        startInProgress = true

        startForegroundNotification()

        val builder = Builder()
            .setSession(Vpn.SESSION_NAME)
            .addAddress(Vpn.ADDRESS, Vpn.PREFIX_LENGTH)
            .addAddress(Vpn.ADDRESS_V6, Vpn.PREFIX_LENGTH_V6)
            .addRoute(Vpn.ROUTE, 0)
            .addRoute(Vpn.ROUTE_V6, 0)
            .setMtu(Vpn.MTU)
        TunnelServiceState.logCallback?.invoke("VPN builder: full IPv4+IPv6 route via TUN")

        when (Prefs.dnsMode) {
            DnsMode.SYSTEM -> {
                val systemDns = getSystemDnsServers()
                if (systemDns.isNotEmpty()) {
                    for (dns in systemDns) builder.addDnsServer(dns)
                } else {
                    builder.addDnsServer(Vpn.DNS_PRIMARY)
                    builder.addDnsServer(Vpn.DNS_SECONDARY)
                }
            }
            DnsMode.CUSTOM -> {
                val primary = Prefs.dnsPrimary.trim()
                val secondary = Prefs.dnsSecondary.trim()
                if (primary.isNotEmpty()) builder.addDnsServer(primary)
                if (secondary.isNotEmpty()) builder.addDnsServer(secondary)
                if (primary.isEmpty() && secondary.isEmpty()) {
                    builder.addDnsServer(Vpn.DNS_PRIMARY)
                    builder.addDnsServer(Vpn.DNS_SECONDARY)
                }
            }
        }

        try {
            applySplitTunnelingRules(builder)
        } catch (error: Throwable) {
            failStartup("Split tunneling configuration failed", error)
            return
        }

        vpnFd = try {
            builder.establish()
        } catch (error: Throwable) {
            failStartup("Failed to establish VPN interface", error)
            return
        }
        if (vpnFd == null) {
            Log.e(TAG, "Failed to establish VPN")
            startInProgress = false
            releaseTun2SocksStartReservation()
            TunnelServiceState.logCallback?.invoke("Failed to establish VPN")
            TunnelServiceState.publishVpnStatus(VpnStatus.CALL_FAILED)
            stopSelf()
            return
        }

        val ownedVpnFd = checkNotNull(vpnFd)
        val fd = try {
            detachOwnedTunDescriptor(
                detach = ownedVpnFd::detachFd,
                closeOwned = { runCatching(ownedVpnFd::close) },
            )
        } catch (error: Throwable) {
            vpnFd = null
            failStartup("Failed to detach VPN interface", error)
            return
        }
        vpnFd = null
        val ownership = DetachedTunFdOwnership(fd, ::closeRawFd)
        tunFdOwnership = ownership
        val bridgeEndpoint = checkNotNull(requestedSocksEndpoint) { "typed SOCKS endpoint was not resolved" }
        // Do not put SOCKS credentials or endpoint details in logcat or the
        // persistent UI log stream. The coordinator only needs the TUN state.
        Log.i(TAG, "VPN established; starting packet bridge")
        TunnelServiceState.logCallback?.invoke("VPN established; starting tun2socks")
        val startGeneration = bumpTunGeneration()
        val startupContract = TunnelBridgeStartupContract()
        val bridgeLifecycle = Tun2SocksHandoffLifecycle(ownership) { lifecycle, error ->
            Tun2SocksProcessBarrier.observeStop(lifecycle, error)
        }
        tun2socksLifecycle = bridgeLifecycle
        Tun2SocksProcessBarrier.attach(startReservation, bridgeLifecycle, ownership)
        tun2socksStartReservation = null

        tun2socksThread = try {
            Thread {
                if (!startInProgress || stopInProgress || !isTunGenerationCurrent(startGeneration)) {
                    bridgeLifecycle.cancelBeforeNativeStart()
                    Tun2SocksProcessBarrier.abort(bridgeLifecycle)
                    dispatchBridgeAction(startupContract.cancelledBeforeNativeStart(), startGeneration)
                    return@Thread
                }
                TunnelServiceState.logCallback?.invoke("tun2socks starting")
                val error = bridgeLifecycle.runStart(
                    start = {
                        invokeTun2SocksBridge(fd, Vpn.MTU, bridgeEndpoint) { args ->
                            GoRuntimeController.startTun2Socks(
                                args.fd,
                                args.mtu,
                                args.socksPort,
                                args.username,
                                args.password,
                            )
                        }
                    },
                    stop = ::invokeNativeStopWithTimeout,
                )
                if (error != null) Tun2SocksProcessBarrier.abort(bridgeLifecycle)
                dispatchBridgeAction(startupContract.nativeStartReturned(error), startGeneration)
            }.also(Thread::start)
        } catch (error: Throwable) {
            ownership.closeBeforeNativeHandoff()
            Tun2SocksProcessBarrier.abort(bridgeLifecycle)
            failStartup("Failed to start packet bridge thread", error)
            return
        }
    }

    private fun applySplitTunnelingRules(builder: Builder) {
        val policy = buildTunnelAppRoutingPolicy(
            mode = Prefs.splitTunnelingMode,
            packages = Prefs.splitTunnelingPackages,
            appPackageName = packageName,
        )
        policy.disallowedPackages.forEach(builder::addDisallowedApplication)
        policy.allowedPackages.forEach(builder::addAllowedApplication)
    }

    private fun dispatchBridgeAction(action: TunnelBridgeAction, generation: Long) {
        if (action == TunnelBridgeAction()) return
        Handler(Looper.getMainLooper()).post {
            if (action.publishActive) {
                if (!startInProgress || stopInProgress || !isTunGenerationCurrent(generation)) return@post
                isRunning = true
                startInProgress = false
                updateStatus(VpnStatus.TUNNEL_ACTIVE)
                return@post
            }
            if (action.status != null && isTunGenerationCurrent(generation) && !stopInProgress) {
                Log.e(TAG, "tun2socks bridge failed during ${if (isRunning) "runtime" else "startup"}")
                TunnelServiceState.logCallback?.invoke("tun2socks bridge failed")
                isRunning = false
                startInProgress = false
                authoritativeStatus = action.status
                TunnelServiceState.publishVpnStatus(action.status)
            }
            if (action.stopService && !stopInProgress && isTunGenerationCurrent(generation)) safeStopSelf()
        }
    }

    private fun failStartup(message: String, error: Throwable) {
        Log.e(TAG, message, error)
        TunnelServiceState.logCallback?.invoke(message)
        isRunning = false
        startInProgress = false
        releaseTun2SocksStartReservation()
        stopInProgress = false
        authoritativeStatus = VpnStatus.CALL_FAILED
        TunnelServiceState.publishVpnStatus(VpnStatus.CALL_FAILED)
        safeStopSelf()
    }

    private fun releaseTun2SocksStartReservation() {
        tun2socksStartReservation?.let(Tun2SocksProcessBarrier::releaseReservation)
        tun2socksStartReservation = null
    }

    private fun bumpTunGeneration(): Long = synchronized(this) {
        tunGeneration += 1
        tunGeneration
    }

    private fun isTunGenerationCurrent(generation: Long): Boolean = synchronized(this) {
        tunGeneration == generation
    }

    private fun closeRawFd(fd: Int) {
        runCatching { ParcelFileDescriptor.adoptFd(fd).close() }
            .onFailure { Log.w(TAG, "Failed to close stale tun fd=$fd: ${it.message}") }
    }

    private fun safeStopSelf() {
        stopInProgress = false
        isRunning = false
        startInProgress = false
        authoritativeStatus = VpnStatus.CALL_DISCONNECTED
        runCatching {
            @Suppress("DEPRECATION")
            stopForeground(true)
        }
        TunnelServiceState.requestTileRefresh(this)
        stopSelf()
    }

    private fun getSystemDnsServers(): List<String> {
        val connectivityManager = getSystemService(ConnectivityManager::class.java) ?: return emptyList()
        val network = connectivityManager.activeNetwork ?: return emptyList()
        val linkProperties = connectivityManager.getLinkProperties(network) ?: return emptyList()
        return linkProperties.dnsServers.mapNotNull { it.hostAddress }
    }

    private fun startForegroundNotification() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                CHANNEL_ID, "VPN Tunnel", NotificationManager.IMPORTANCE_LOW
            )
            val nm = getSystemService(NotificationManager::class.java)
            nm.createNotificationChannel(channel)
        }

        startForeground(NOTIFICATION_ID, buildNotification(getString(VpnStatus.STARTING.labelRes)))
    }

    private fun buildNotification(text: String): Notification {
        val openIntent = Intent(this, CapacitorMainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP
        }
        val openPending = PendingIntent.getActivity(
            this, 1, openIntent, PendingIntent.FLAG_IMMUTABLE
        )
        val stopIntent = Intent(this, TunnelVpnService::class.java).apply {
            action = ACTION_STOP
        }
        val stopPending = PendingIntent.getService(
            this, 0, stopIntent, PendingIntent.FLAG_IMMUTABLE
        )
        @Suppress("DEPRECATION")
        val builder = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(this, CHANNEL_ID)
        } else {
            Notification.Builder(this)
        }
        return builder
            .setContentTitle(getString(R.string.notification_title))
            .setContentText(if (Prefs.showNotificationStatusText) text else "")
            .setSmallIcon(android.R.drawable.ic_lock_lock)
            .setOngoing(true)
            .setContentIntent(openPending)
            .addAction(Notification.Action.Builder(null, getString(R.string.notification_disconnect), stopPending).build())
            .build()
    }
}
