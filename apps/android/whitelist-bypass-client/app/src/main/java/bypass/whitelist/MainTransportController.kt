package bypass.whitelist

import android.app.Activity
import android.content.Intent
import android.os.Build
import android.os.Handler
import android.os.Looper
import androidx.appcompat.app.AppCompatActivity
import androidx.fragment.app.Fragment
import bypass.whitelist.tunnel.CallConfig
import bypass.whitelist.tunnel.CallPlatform
import bypass.whitelist.tunnel.HeadlessJoinController
import bypass.whitelist.tunnel.HeadlessSessionService
import bypass.whitelist.tunnel.PortGuard
import bypass.whitelist.tunnel.ProxyService
import bypass.whitelist.tunnel.TunnelMode
import bypass.whitelist.tunnel.TunnelServiceState
import bypass.whitelist.tunnel.TunnelVpnService
import bypass.whitelist.tunnel.VpnStatus
import bypass.whitelist.ui.HeadlessVkFragment
import bypass.whitelist.ui.JoinFragmentHost
import bypass.whitelist.ui.JoinSessionShutdown
import bypass.whitelist.ui.JsHookJoinFragment
import bypass.whitelist.util.Net
import bypass.whitelist.util.Prefs
import bypass.whitelist.util.SocksAuth
import bypass.whitelist.util.maskUrl
import java.io.ByteArrayOutputStream
import java.io.EOFException
import java.io.IOException
import java.io.InputStream
import java.net.Inet4Address
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.Socket
import javax.net.ssl.SSLSocket
import javax.net.ssl.SSLSocketFactory
import kotlin.concurrent.thread

/**
 * Handles session lifecycle, connectivity monitoring, SOCKS5 networking,
 * VPN management, and reset orchestration for [MainActivity].
 */
internal class MainTransportController(private val host: ActivityHost) {
    private val vpnStatusOwner = Any()

    internal interface ActivityHost {
        val activity: AppCompatActivity
        val joinFragmentHost: JoinFragmentHost
        val startupCallLink: String

        fun appendLog(message: String)
        fun getMainFragmentBridge(): MainFragmentBridge?
        fun setJoinOverlayVisibleInternal(visible: Boolean)
        fun removeJoinFragmentInternal()
        fun requestVpnPermission()
        fun startProxyServiceDirect()
        fun isResetCurrentInternal(resetId: Long): Boolean
        fun getApplicationNativeLibraryDir(): String
        fun getStringResource(resId: Int): String
        fun scheduleBadRoomRecovery(reason: String)
        fun onResetCompleted()
    }

    internal interface MainFragmentBridge {
        fun onStatusChanged(status: VpnStatus)
        fun onStatusTextChanged(text: String)
        fun onConnectedChanged(connected: Boolean)
        fun onDestinationsChanged()
    }

    @Volatile var resetInProgress: Boolean = false
        private set
    @Volatile var resetGeneration: Long = 0L
        private set
    var pendingConnectConfig: CallConfig? = null
    var connected: Boolean = false
    var lastStatus: VpnStatus? = null
    var activeJoinUrl: String = ""
    var activeHeadlessController: HeadlessJoinController? = null
    private var connectivityValidationGeneration: Int = 0
    private val watchdogHandler = Handler(Looper.getMainLooper())
    private var watchdogGeneration: Int = 0
    private var watchdogStartedAtMs: Long = 0L
    private var watchdogConsecutiveFailures: Int = 0
    private var watchdogProbeRunning: Boolean = false
    private val vpnLauncher = host.activity.registerForActivityResult(
        androidx.activity.result.contract.ActivityResultContracts.StartActivityForResult()
    ) { result ->
        if (result.resultCode == Activity.RESULT_OK) startVpnService()
        else host.appendLog("VPN permission denied")
    }

    fun handleStartup() {
        val activity = host.activity
        val callLink = host.startupCallLink
        if (callLink.isNotEmpty() && !TunnelServiceState.isAnyTunnelComponentRunning(activity)) {
            startJoinFor(CallConfig.newWith(name = CallConfig.suggestNameFor(callLink), url = callLink))
        } else if (Prefs.connectOnStart && !TunnelServiceState.isAnyTunnelComponentRunning(activity)) {
            Prefs.activeDestination?.let(::startJoinFor)
        }
    }

    fun installDisconnectCallbacks() {
        TunnelVpnService.onDisconnect = { host.activity.runOnUiThread { onDisconnectFromService() } }
        ProxyService.onDisconnect = { host.activity.runOnUiThread { onDisconnectFromService() } }
    }

    fun handleConnectPressed(config: CallConfig) {
        if (resetInProgress) {
            pendingConnectConfig = config
            host.appendLog("Queued connect after previous session stops")
            host.getMainFragmentBridge()?.onStatusTextChanged("Stopping previous session...")
            return
        }
        val activity = host.activity
        if (TunnelServiceState.isAnyTunnelComponentRunning(activity) || !PortGuard.isPortAvailable(Prefs.socksPort)) {
            pendingConnectConfig = config
            host.appendLog("Waiting for previous local tunnel to stop")
            fullReset()
            return
        }
        startJoinFor(config)
    }

    fun handleDisconnectPressed() {
        pendingConnectConfig = null
        if (resetInProgress) { forceUnlockReset("Stopped waiting for previous session"); return }
        fullReset()
    }

    fun handleJoinStatus(status: VpnStatus) {
        if (resetInProgress && status != VpnStatus.CALL_FAILED) return
        TunnelVpnService.instance?.updateStatus(status)
        ProxyService.instance?.updateStatus(status)
        lastStatus = status
        host.activity.runOnUiThread {
            if (status == VpnStatus.CALL_FAILED) {
                fullReset()
                lastStatus = VpnStatus.CALL_FAILED
                host.getMainFragmentBridge()?.onStatusChanged(VpnStatus.CALL_FAILED)
                return@runOnUiThread
            }
            host.getMainFragmentBridge()?.onStatusChanged(status)
            if (status == VpnStatus.TUNNEL_ACTIVE) {
                connected = false
                host.getMainFragmentBridge()?.onConnectedChanged(false)
                host.getMainFragmentBridge()?.onStatusTextChanged("Туннель поднят, проверяю связь…")
                beginConnectivityValidation("join-status")
            }
        }
    }

    fun installStatusCallbacks() {
        val activity = host.activity
        TunnelServiceState.attachVpnStatusCallback(vpnStatusOwner) { status ->
            activity.runOnUiThread {
                if (resetInProgress) {
                    host.getMainFragmentBridge()?.onStatusChanged(VpnStatus.STOPPING)
                    host.getMainFragmentBridge()?.onStatusTextChanged("Stopping previous session...")
                    return@runOnUiThread
                }
                lastStatus = status
                host.getMainFragmentBridge()?.onStatusChanged(status)
                if (status == VpnStatus.TUNNEL_ACTIVE) {
                    beginConnectivityValidation("service-status")
                } else if (status == VpnStatus.CALL_FAILED || status == VpnStatus.CALL_DISCONNECTED || status == VpnStatus.TUNNEL_LOST) {
                    if (connected) { connected = false; host.getMainFragmentBridge()?.onConnectedChanged(false) }
                }
            }
        }
        TunnelServiceState.logCallback = { message -> activity.runOnUiThread { host.appendLog(message) } }
    }

    fun clearStatusCallbacks() {
        TunnelServiceState.detachVpnStatusCallback(vpnStatusOwner)
        TunnelServiceState.logCallback = null
    }

    fun syncResumeState() {
        val activity = host.activity
        when {
            resetInProgress -> {
                connected = false; lastStatus = VpnStatus.STOPPING
                host.getMainFragmentBridge()?.onConnectedChanged(false)
                host.getMainFragmentBridge()?.onStatusChanged(VpnStatus.STOPPING)
                host.getMainFragmentBridge()?.onStatusTextChanged("Stopping previous session...")
            }
            TunnelServiceState.isTunnelActive(activity) -> {
                if (!connected || lastStatus != VpnStatus.TUNNEL_ACTIVE) {
                    connected = false; lastStatus = VpnStatus.CONNECTING
                    host.getMainFragmentBridge()?.onStatusChanged(VpnStatus.CONNECTING)
                    host.getMainFragmentBridge()?.onConnectedChanged(false)
                    host.getMainFragmentBridge()?.onStatusTextChanged("Туннель поднят, проверяю связь…")
                    beginConnectivityValidation("resume-active")
                }
            }
            TunnelServiceState.isHeadlessSessionRunning(activity) -> {
                connected = false; lastStatus = VpnStatus.CONNECTING
                host.getMainFragmentBridge()?.onConnectedChanged(false)
                host.getMainFragmentBridge()?.onStatusChanged(VpnStatus.CONNECTING)
            }
            connected && lastStatus == VpnStatus.TUNNEL_ACTIVE -> onDisconnectFromService()
        }
    }

    fun canAutoStart(): Boolean {
        val activity = host.activity
        return !connected && !TunnelServiceState.isAnyTunnelComponentRunning(activity)
    }

    fun beginConnectivityValidation(reason: String) {
        val validationId = ++connectivityValidationGeneration
        host.appendLog("Connectivity validation started: $reason")
        thread {
            val result = try {
                runTunnelDiagnostics { text ->
                    host.activity.runOnUiThread { host.getMainFragmentBridge()?.onStatusTextChanged(text) }
                }
            } catch (e: Exception) {
                "connectivity validation failed: ${e.message ?: "unknown"}" to false
            }
            host.activity.runOnUiThread {
                if (validationId != connectivityValidationGeneration || resetInProgress) return@runOnUiThread
                if (result.second) {
                    connected = true; lastStatus = VpnStatus.TUNNEL_ACTIVE
                    host.getMainFragmentBridge()?.onStatusChanged(VpnStatus.TUNNEL_ACTIVE)
                    host.getMainFragmentBridge()?.onConnectedChanged(true)
                    host.getMainFragmentBridge()?.onStatusTextChanged(result.first)
                    host.appendLog("Connectivity validation OK: ${result.first}")
                    startConnectivityWatchdog("validated")
                } else {
                    connected = false; lastStatus = VpnStatus.TUNNEL_LOST
                    host.getMainFragmentBridge()?.onStatusChanged(VpnStatus.TUNNEL_LOST)
                    host.getMainFragmentBridge()?.onConnectedChanged(false)
                    host.getMainFragmentBridge()?.onStatusTextChanged("Связи нет: ${result.first}")
                    host.appendLog("Connectivity validation failed: ${result.first}")
                    stopConnectivityWatchdog("validation_failed")
                    host.scheduleBadRoomRecovery("connectivity_validation_failed")
                }
            }
        }
    }

    private fun startConnectivityWatchdog(reason: String) {
        watchdogGeneration++; watchdogStartedAtMs = System.currentTimeMillis()
        watchdogConsecutiveFailures = 0; watchdogProbeRunning = false
        host.appendLog("Connectivity watchdog started: $reason")
        scheduleWatchdogTick(initial = true)
    }

    fun stopConnectivityWatchdog(reason: String) {
        watchdogGeneration++; watchdogProbeRunning = false
        watchdogHandler.removeCallbacksAndMessages(null)
        host.appendLog("Connectivity watchdog stopped: $reason")
    }

    private fun scheduleWatchdogTick(initial: Boolean = false) {
        val gen = watchdogGeneration
        val ageMs = System.currentTimeMillis() - watchdogStartedAtMs
        val delay = when { initial -> 15_000L; ageMs < 120_000L -> 15_000L; else -> 60_000L }
        watchdogHandler.postDelayed({ if (gen == watchdogGeneration) runWatchdogProbe(gen) }, delay)
    }

    private fun runWatchdogProbe(generation: Int) {
        if (!connected || resetInProgress || watchdogProbeRunning) return
        watchdogProbeRunning = true
        thread(name = "wt-connectivity-watchdog") {
            val result = try { runTunnelPingOnly() }
            catch (e: Exception) { "watchdog ping failed: ${e.message ?: "unknown"}" to false }
            host.activity.runOnUiThread {
                watchdogProbeRunning = false
                if (generation != watchdogGeneration || resetInProgress || !connected) return@runOnUiThread
                if (result.second) {
                    watchdogConsecutiveFailures = 0
                    host.appendLog("Connectivity watchdog OK: ${result.first}")
                    scheduleWatchdogTick()
                } else {
                    watchdogConsecutiveFailures += 1
                    host.appendLog("Connectivity watchdog failed #$watchdogConsecutiveFailures: ${result.first}")
                    host.getMainFragmentBridge()?.onStatusTextChanged("Проверяю связь… ${result.first}")
                    if (watchdogConsecutiveFailures >= 2) {
                        connected = false; lastStatus = VpnStatus.TUNNEL_LOST
                        host.getMainFragmentBridge()?.onStatusChanged(VpnStatus.TUNNEL_LOST)
                        host.getMainFragmentBridge()?.onConnectedChanged(false)
                        host.getMainFragmentBridge()?.onStatusTextChanged("Связь потеряна, переподключаюсь…")
                        stopConnectivityWatchdog("lost")
                        host.scheduleBadRoomRecovery("watchdog_lost_connectivity")
                    } else scheduleWatchdogTick()
                }
            }
        }
    }

    fun handlePing(callback: (Boolean, Int) -> Unit) {
        thread {
            val started = System.nanoTime()
            val ok = try { probeHttpsViaSocks5("t.me", "/Kuplinov_Telegram/1032") } catch (_: Exception) { false }
            val rtt = ((System.nanoTime() - started) / 1_000_000).toInt()
            host.activity.runOnUiThread { callback(ok, rtt) }
        }
    }

    fun handleDiagnostics(callback: (String, Boolean) -> Unit, progress: (String) -> Unit) {
        thread {
            val result = try {
                runTunnelDiagnostics { text -> host.activity.runOnUiThread { progress(text) } }
            } catch (e: Exception) {
                host.appendLog("Tunnel diagnostics failed: ${e.message}")
                "diagnostics failed: ${e.message ?: "unknown"}" to false
            }
            host.activity.runOnUiThread {
                callback(result.first, result.second)
                Handler(Looper.getMainLooper()).postDelayed({
                    host.getMainFragmentBridge()?.onStatusTextChanged(result.first)
                }, 5000)
            }
        }
    }

    private fun runTunnelPingOnly(): Pair<String, Boolean> {
        val start = System.nanoTime()
        openSocks5TcpWithRetry("api.ipify.org", 80, 5, 1200).use { it.soTimeout = 5000 }
        return "${((System.nanoTime() - start) / 1_000_000).toInt()} ms" to true
    }

    private fun runTunnelDiagnostics(progress: (String) -> Unit): Pair<String, Boolean> {
        progress(host.getStringResource(R.string.diag_ping))
        val start = System.nanoTime()
        openSocks5TcpWithRetry("api.ipify.org", 80, 5, 1200).use { it.soTimeout = 7000 }
        val ms = ((System.nanoTime() - start) / 1_000_000).toInt()
        progress("/ping: $ms ms · ${host.getStringResource(R.string.diag_external_ip)}")
        val ipResp = socksHttp("api.ipify.org", 80,
            "GET / HTTP/1.1\r\nHost: api.ipify.org\r\nUser-Agent: ${speedUserAgent()}\r\nConnection: close\r\n\r\n"
                .toByteArray(Charsets.US_ASCII), 9000)
        val ip = String(httpBody(ipResp), Charsets.UTF_8).trim().lineSequence().firstOrNull()?.take(64).orEmpty()
        if (ip.isBlank()) return "/ping: $ms ms · external IP failed" to false
        progress("/ping: $ms ms · IP: $ip · ${host.getStringResource(R.string.diag_telegram)}")
        val tgStart = System.nanoTime()
        val tgOk = probeHttpsViaSocks5("t.me", "/Kuplinov_Telegram/1032")
        val tgMs = ((System.nanoTime() - tgStart) / 1_000_000).toInt()
        val text = "$ms ms · $ip · t.me ${if (tgOk) "OK" else "FAIL"} $tgMs ms"
        host.appendLog("Tunnel diagnostics $text")
        return text to tgOk
    }

    @Suppress("unused")
    private fun runTunnelSpeedTest(): Pair<String, Boolean> {
        val sh = "10.255.0.1"; val port = 18080; val start = System.nanoTime()
        val ping = socksHttp(sh, port, "GET /ping HTTP/1.1\r\nHost: $sh\r\nUser-Agent: ${speedUserAgent()}\r\nConnection: close\r\n\r\n".toByteArray(Charsets.US_ASCII))
        if (!String(ping, Charsets.ISO_8859_1).startsWith("HTTP/1.")) return host.getStringResource(R.string.speedtest_failed) to false
        val ms = ((System.nanoTime() - start) / 1_000_000).toInt()
        val dlB = 2 * 1024 * 1024; val dlS = System.nanoTime()
        val dl = socksHttp(sh, port, "GET /download?bytes=$dlB HTTP/1.1\r\nHost: $sh\r\nUser-Agent: ${speedUserAgent()}\r\nConnection: close\r\n\r\n".toByteArray(Charsets.US_ASCII))
        val dlBody = httpBody(dl); val dlSec = (System.nanoTime() - dlS) / 1e9
        val dlMbps = if (dlSec > 0) dlBody.size * 8.0 / dlSec / 1e6 else 0.0
        val ulB = 1024 * 1024; val ulBody = ByteArray(ulB) { 0x5a.toByte() }
        val hdr = "POST /upload HTTP/1.1\r\nHost: $sh\r\nUser-Agent: ${speedUserAgent()}\r\nContent-Length: $ulB\r\nConnection: close\r\n\r\n".toByteArray(Charsets.US_ASCII)
        val ulS = System.nanoTime()
        val upResp = socksHttp(sh, port, hdr + ulBody)
        val ulSec = (System.nanoTime() - ulS) / 1e9
        val ulMbps = if (ulSec > 0) ulB * 8.0 / ulSec / 1e6 else 0.0
        if (!String(upResp, Charsets.ISO_8859_1).startsWith("HTTP/1.")) return host.getStringResource(R.string.speedtest_failed) to false
        val text = "$ms ms · ↓ %.1f Mbps · ↑ %.1f Mbps".format(dlMbps, ulMbps)
        host.appendLog("Speedtest $text")
        return text to true
    }

    private fun speedUserAgent(): String {
        val tail = Prefs.discoveryClientId.takeLast(8)
        return "BEZabotny-NET/${appVersionName()} Android/${Build.VERSION.RELEASE} ${Build.MANUFACTURER}/${Build.MODEL} client=$tail"
    }

    private fun appVersionName(): String = try {
        val a = host.activity
        a.packageManager.getPackageInfo(a.packageName, 0).versionName ?: "unknown"
    } catch (_: Exception) { "unknown" }

    private fun socksHttp(host: String, port: Int, request: ByteArray, readTimeoutMs: Int = 15000): ByteArray {
        openSocks5TcpWithRetry(host, port, 4, 1000).use { socket ->
            socket.soTimeout = readTimeoutMs
            val o = socket.getOutputStream(); val i = socket.getInputStream()
            o.write(request); o.flush()
            val buf = ByteArray(65536); val out = ByteArrayOutputStream()
            while (true) { val n = i.read(buf); if (n < 0) break; out.write(buf, 0, n) }
            return out.toByteArray()
        }
    }

    private fun httpBody(response: ByteArray): ByteArray {
        val needle = byteArrayOf(13, 10, 13, 10)
        for (i in 0..response.size - needle.size) {
            if (needle.indices.all { response[i + it] == needle[it] })
                return response.copyOfRange(i + needle.size, response.size)
        }
        return response
    }

    private fun openSocks5TcpWithRetry(host: String, port: Int, attempts: Int = 4, delayMs: Long = 1000): Socket {
        var last: Exception? = null
        repeat(attempts) { idx ->
            try { return openSocks5Tcp(host, port) }
            catch (e: Exception) {
                last = e
                this.host.appendLog("SOCKS probe retry ${idx + 1}/$attempts for $host:$port failed: ${e.message ?: e.javaClass.simpleName}")
                if (idx + 1 < attempts) Thread.sleep(delayMs)
            }
        }
        throw last ?: IOException("SOCKS connect failed")
    }

    private fun openSocks5Tcp(host: String, port: Int): Socket {
        val socket = Socket()
        socket.connect(InetSocketAddress(Net.LOCALHOST, Prefs.socksPort.toInt()), 3000)
        socket.soTimeout = 3500
        val output = socket.getOutputStream(); val input = socket.getInputStream()
        output.write(byteArrayOf(0x05, 0x01, 0x02)); output.flush()
        if (input.read() != 0x05 || input.read() != 0x02) throw IOException("SOCKS auth method rejected")
        val ub = SocksAuth.user.toByteArray(Charsets.US_ASCII)
        val pb = SocksAuth.pass.toByteArray(Charsets.US_ASCII)
        val ap = ByteArray(3 + ub.size + pb.size)
        ap[0] = 0x01; ap[1] = ub.size.toByte()
        System.arraycopy(ub, 0, ap, 2, ub.size)
        ap[2 + ub.size] = pb.size.toByte()
        System.arraycopy(pb, 0, ap, 3 + ub.size, pb.size)
        output.write(ap); output.flush()
        if (input.read() != 0x01 || input.read() != 0x00) throw IOException("SOCKS auth failed")
        val ipv4 = runCatching { InetAddress.getAllByName(host).firstOrNull { it is Inet4Address }?.address }.getOrNull()
        val req = if (ipv4 != null && ipv4.size == 4) {
            ByteArray(10).also { it[0] = 0x05; it[1] = 0x01; it[2] = 0x00; it[3] = 0x01
                System.arraycopy(ipv4, 0, it, 4, 4)
                it[8] = ((port ushr 8) and 0xff).toByte(); it[9] = (port and 0xff).toByte() }
        } else {
            val hb = host.toByteArray(Charsets.US_ASCII)
            ByteArray(5 + hb.size + 2).also { it[0] = 0x05; it[1] = 0x01; it[2] = 0x00; it[3] = 0x03
                it[4] = hb.size.toByte(); System.arraycopy(hb, 0, it, 5, hb.size)
                it[5 + hb.size] = ((port ushr 8) and 0xff).toByte()
                it[6 + hb.size] = (port and 0xff).toByte() }
        }
        output.write(req); output.flush()
        if (input.read() != 0x05 || input.read() != 0x00) throw IOException("SOCKS connect failed")
        if (input.read() != 0x00) throw IOException("SOCKS reserved byte failed")
        when (input.read()) {
            0x01 -> readFully(input, 4); 0x03 -> readFully(input, input.read())
            0x04 -> readFully(input, 16); else -> throw IOException("SOCKS bad address type")
        }
        readFully(input, 2); return socket
    }

    private fun probeHttpsViaSocks5(host: String, path: String): Boolean {
        openSocks5Tcp(host, 443).use { socket ->
            val ssl = (SSLSocketFactory.getDefault() as SSLSocketFactory)
                .createSocket(socket, host, 443, true) as SSLSocket
            ssl.soTimeout = 8000; ssl.startHandshake()
            val o = ssl.getOutputStream(); val i = ssl.getInputStream()
            o.write("GET $path HTTP/1.1\r\nHost: $host\r\nUser-Agent: BEZabotny-NET ping\r\nAccept: text/html,*/*\r\nConnection: close\r\n\r\n".toByteArray(Charsets.US_ASCII))
            o.flush()
            val status = i.bufferedReader(Charsets.US_ASCII).readLine() ?: return false
            this.host.appendLog("Ping target $host$path -> $status")
            return status.contains(" 2") || status.contains(" 3")
        }
    }

    private fun readFully(input: InputStream, count: Int) {
        var rem = count; val buf = ByteArray(256)
        while (rem > 0) { val n = input.read(buf, 0, minOf(buf.size, rem)); if (n < 0) throw EOFException(); rem -= n }
    }

    fun startJoinFor(config: CallConfig) {
        if (resetInProgress) {
            pendingConnectConfig = config
            host.appendLog("Queued connect after previous session stops")
            host.getMainFragmentBridge()?.onStatusTextChanged("Stopping previous session...")
            return
        }
        val activity = host.activity
        if (TunnelServiceState.isAnyTunnelComponentRunning(activity) || !PortGuard.isPortAvailable(Prefs.socksPort)) {
            pendingConnectConfig = config
            host.appendLog("Waiting for previous local tunnel to stop")
            fullReset(); return
        }
        val url = config.url.trim(); if (url.isEmpty()) return
        if (config.autoDiscovered) { Prefs.autoDestination = config; host.appendLog("Auto room grace refreshed for 60s") }
        if (Prefs.activeTunnelMode == TunnelMode.DC && (config.platform == CallPlatform.TELEMOST || config.platform == CallPlatform.DION)) {
            android.widget.Toast.makeText(activity, R.string.dc_mode_not_supported, android.widget.Toast.LENGTH_SHORT).show()
        }
        if (connected) fullReset()
        activeJoinUrl = url
        host.appendLog("Loading: ${maskUrl(url)}")
        lastStatus = VpnStatus.CONNECTING
        host.getMainFragmentBridge()?.onStatusChanged(VpnStatus.CONNECTING)
        host.getMainFragmentBridge()?.onConnectedChanged(false)
        val headless = Prefs.headless || config.platform == CallPlatform.WBSTREAM || config.platform == CallPlatform.DION
        if (headless && config.platform != CallPlatform.VK) {
            host.setJoinOverlayVisibleInternal(false)
            activeHeadlessController = HeadlessJoinController(
                host.getApplicationNativeLibraryDir(),
                host.joinFragmentHost,
                config.platform,
                url,
            ).also { it.start() }
            return
        }
        val frag = if (headless) HeadlessVkFragment.newInstance(url) else JsHookJoinFragment.newInstance(url)
        host.setJoinOverlayVisibleInternal(!headless)
        activity.supportFragmentManager.beginTransaction().replace(R.id.joinOverlayContainer, frag).commit()
    }

    private fun startVpnService() {
        val a = host.activity
        a.startService(Intent(a, TunnelVpnService::class.java).apply {
            putExtra(TunnelVpnService.EXTRA_EXPLICIT_LEGACY_ENDPOINT, true)
        })
        host.appendLog("VPN start requested")
        handleJoinStatus(VpnStatus.STARTING)
    }

    fun onDisconnectFromService() {
        if (resetInProgress) { maybeFinishReset(); return }
        connected = false; connectivityValidationGeneration++
        stopConnectivityWatchdog("disconnect"); lastStatus = null
        closeActiveHeadlessController()
        host.removeJoinFragmentInternal(); host.setJoinOverlayVisibleInternal(false)
        host.getMainFragmentBridge()?.onConnectedChanged(false)
        host.getMainFragmentBridge()?.onStatusChanged(VpnStatus.CALL_DISCONNECTED)
        Prefs.extendAutoDestinationGrace()
    }

    fun fullReset() {
        if (resetInProgress) return
        resetInProgress = true; connectivityValidationGeneration++
        stopConnectivityWatchdog("full_reset")
        val resetId = ++resetGeneration
        connected = false; lastStatus = VpnStatus.STOPPING
        val ctrl = activeHeadlessController; activeHeadlessController = null; activeJoinUrl = ""
        host.removeJoinFragmentInternal()
        val activity = host.activity
        TunnelVpnService.requestStop(activity); ProxyService.requestStop(activity)
        HeadlessSessionService.requestStop(activity)
        host.setJoinOverlayVisibleInternal(false)
        host.getMainFragmentBridge()?.onConnectedChanged(false)
        host.getMainFragmentBridge()?.onStatusChanged(VpnStatus.STOPPING)
        host.getMainFragmentBridge()?.onStatusTextChanged("Stopping previous session...")
        thread(name = "full-reset-shutdown") {
            ctrl?.close(); var attempts = 0
            while (attempts < 40 && (TunnelServiceState.isAnyTunnelComponentRunning(activity) || !PortGuard.isPortAvailable(Prefs.socksPort))) {
                if (!host.isResetCurrentInternal(resetId)) return@thread; Thread.sleep(100); attempts++
            }
            if (!host.isResetCurrentInternal(resetId)) return@thread
            if (TunnelServiceState.isAnyTunnelComponentRunning(activity) || !PortGuard.isPortAvailable(Prefs.socksPort)) {
                TunnelVpnService.requestStop(activity); ProxyService.requestStop(activity)
                HeadlessSessionService.requestStop(activity); PortGuard.ensurePortFree(Prefs.socksPort)
                Thread.sleep(150)
            }
            if (!host.isResetCurrentInternal(resetId)) return@thread
            if (TunnelServiceState.isAnyTunnelComponentRunning(activity) || !PortGuard.isPortAvailable(Prefs.socksPort)) {
                activity.runOnUiThread { if (host.isResetCurrentInternal(resetId)) forceUnlockReset("Previous session is still shutting down. Try connect again.") }
                return@thread
            }
            Thread.sleep(400)
            activity.runOnUiThread { if (host.isResetCurrentInternal(resetId)) maybeFinishReset(resetId) }
        }
    }

    private fun maybeFinishReset(expectedResetId: Long? = null) {
        if (!resetInProgress) return
        if (expectedResetId != null && expectedResetId != resetGeneration) return
        val activity = host.activity
        if (TunnelServiceState.isAnyTunnelComponentRunning(activity) || !PortGuard.isPortAvailable(Prefs.socksPort)) return
        resetInProgress = false; connected = false; lastStatus = null; activeJoinUrl = ""
        host.removeJoinFragmentInternal(); host.setJoinOverlayVisibleInternal(false)
        host.getMainFragmentBridge()?.onConnectedChanged(false)
        host.getMainFragmentBridge()?.onStatusChanged(VpnStatus.CALL_DISCONNECTED)
        Prefs.extendAutoDestinationGrace()
        host.onResetCompleted()
    }

    fun forceUnlockReset(message: String) {
        resetInProgress = false; pendingConnectConfig = null; connected = false; activeJoinUrl = ""
        lastStatus = if (PortGuard.isPortAvailable(Prefs.socksPort)) VpnStatus.CALL_DISCONNECTED else VpnStatus.PORT_BUSY
        closeActiveHeadlessController()
        host.removeJoinFragmentInternal(); host.setJoinOverlayVisibleInternal(false)
        val a = host.activity
        TunnelVpnService.requestStop(a); ProxyService.requestStop(a); HeadlessSessionService.requestStop(a)
        host.getMainFragmentBridge()?.onConnectedChanged(false)
        host.getMainFragmentBridge()?.onStatusChanged(lastStatus ?: VpnStatus.CALL_DISCONNECTED)
        host.getMainFragmentBridge()?.onStatusTextChanged(message)
        host.appendLog(message)
    }

    fun handleTunnelModeChanged() { fullReset() }
    fun closeActiveHeadlessController() { val c = activeHeadlessController; activeHeadlessController = null; if (c != null) thread(name = "headless-shutdown") { c.close() } }
    fun shutdownJoinFragment() {
        (host.activity.supportFragmentManager.findFragmentById(R.id.joinOverlayContainer) as? JoinSessionShutdown)
            ?.shutdownSession()
    }
}
