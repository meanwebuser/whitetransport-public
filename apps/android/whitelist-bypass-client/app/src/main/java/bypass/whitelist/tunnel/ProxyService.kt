package bypass.whitelist.tunnel

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.IBinder
import android.util.Log
import bypass.whitelist.MainActivity
import bypass.whitelist.util.Callback
import bypass.whitelist.R
import bypass.whitelist.util.Prefs

class ProxyService : Service() {

    companion object {
        const val CHANNEL_ID = "proxy_channel"
        const val NOTIFICATION_ID = 2
        const val ACTION_STOP = "bypass.whitelist.STOP_PROXY"
        @Volatile var instance: ProxyService? = null
        @Volatile var onDisconnect: Callback? = null

        fun requestStop(context: Context) {
            val running = instance?.let { it.isRunning || it.stopInProgress } == true
            val intent = Intent(context, ProxyService::class.java)
            try {
                if (running) {
                    context.startService(intent.apply { action = ACTION_STOP })
                } else {
                    context.stopService(intent)
                    TunnelServiceState.requestTileRefresh(context)
                }
            } catch (_: Exception) {
            }
        }
    }

    @Volatile var isRunning = false
        private set
    @Volatile internal var stopInProgress = false

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        instance = this
        TunnelServiceState.requestTileRefresh(this)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            if (!isRunning && !stopInProgress) {
                safeStopSelf()
                return START_NOT_STICKY
            }
            stop()
            return START_NOT_STICKY
        }
        start()
        return START_STICKY
    }

    override fun onDestroy() {
        if (isRunning && !stopInProgress) {
            stop()
        }
        if (instance === this) {
            instance = null
        }
        onDisconnect = null
        stopInProgress = false
        TunnelServiceState.requestTileRefresh(this)
        super.onDestroy()
    }

    fun updateStatus(status: VpnStatus) {
        val nm = getSystemService(NotificationManager::class.java)
        nm.notify(NOTIFICATION_ID, buildNotification(getString(status.labelRes)))
    }

    @Synchronized
    fun stop() {
        if (stopInProgress) return
        if (!isRunning) {
            safeStopSelf()
            return
        }
        isRunning = false
        stopInProgress = true
        val disconnectCallback = onDisconnect
        try {
            @Suppress("DEPRECATION")
            stopForeground(true)
            HeadlessSessionService.requestDependentStop(this)
            disconnectCallback?.invoke()
            TunnelServiceState.requestTileRefresh(this)
        } catch (t: Throwable) {
            Log.e("ProxyService", "Crash during Proxy stop: ${t.message}", t)
        } finally {
            stopSelf()
        }
    }

    private fun safeStopSelf() {
        isRunning = false
        stopInProgress = false
        runCatching {
            @Suppress("DEPRECATION")
            stopForeground(true)
        }
        TunnelServiceState.requestTileRefresh(this)
        stopSelf()
    }

    private fun start() {
        if (isRunning) return
        isRunning = true
        TunnelServiceState.requestTileRefresh(this)

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                CHANNEL_ID, "Proxy Tunnel", NotificationManager.IMPORTANCE_LOW
            )
            val nm = getSystemService(NotificationManager::class.java)
            nm.createNotificationChannel(channel)
        }

        startForeground(NOTIFICATION_ID, buildNotification(getString(R.string.status_proxy_on)))
    }

    private fun buildNotification(text: String): Notification {
        val openIntent = Intent(this, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP
        }
        val openPending = PendingIntent.getActivity(
            this, 1, openIntent, PendingIntent.FLAG_IMMUTABLE
        )
        val stopIntent = Intent(this, ProxyService::class.java).apply {
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
