package bypass.whitelist.tunnel

import android.os.Build
import android.util.Log
import bypass.whitelist.util.ParamCallback
import bypass.whitelist.util.Ports
import bypass.whitelist.util.Prefs
import bypass.whitelist.util.SocksAuth
import androidbind.LogCallback
import androidbind.Androidbind
import java.io.BufferedWriter
import java.io.File
import java.io.OutputStreamWriter
import java.net.Inet4Address
import java.net.InetAddress
import java.util.concurrent.TimeUnit

class RelayController(
    private val nativeLibDir: String,
    private val onLog: ParamCallback<String>,
    private val onStatus: ParamCallback<VpnStatus>,
) {
    private var dcThread: Thread? = null
    private var pionThread: Thread? = null
    private var pionProcess: Process? = null

    @Volatile
    var isRunning = false
        private set

    @Synchronized
    fun start(mode: TunnelMode, platform: CallPlatform) {
        stop()
        isRunning = true
        if (mode.isPion) startPion(mode, platform) else startDc()
    }

    @Synchronized
    fun stop() {
        isRunning = false

        pionProcess?.let {
            it.destroy()
            try {
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                    if (!it.waitFor(1500, TimeUnit.MILLISECONDS)) {
                        it.destroyForcibly()
                        it.waitFor(500, TimeUnit.MILLISECONDS)
                    }
                } else {
                    it.waitFor()
                }
            } catch (_: Exception) {
            }
        }
        pionProcess = null
        pionThread?.interrupt()
        pionThread = null

        Androidbind.stopJoiner()
        dcThread?.interrupt()
        dcThread = null
    }

    private fun startDc() {
        val cb = LogCallback { msg ->
            onLog(msg)
            if (msg.contains("browser connected")) onStatus(VpnStatus.TUNNEL_ACTIVE)
            else if (msg.contains("ws read error")) onStatus(VpnStatus.TUNNEL_LOST)
        }
        dcThread = Thread {
            try {
                if (!checkPortOrAbort()) return@Thread
            } catch (e: Exception) {
                if (isRunning) onLog("Port check error: ${e.message}")
                return@Thread
            }
            try {
                Androidbind.startJoiner(Ports.DC_WS, Prefs.socksPort, Prefs.socksHost, SocksAuth.user, SocksAuth.pass, cb)
            } catch (e: Exception) {
                if (isRunning) onLog("Relay error: ${e.message}")
            }
        }.also { it.start() }
        onLog("Relay started DC mode (SOCKS5 ${SocksAuth.user}:${SocksAuth.pass}@${Prefs.socksHost}:${Prefs.socksPort}, WS :${Ports.DC_WS})")
    }

    private fun startPion(mode: TunnelMode, platform: CallPlatform) {
        val relayBin = File(nativeLibDir, "librelay.so")
        if (!relayBin.exists()) {
            onLog("Pion relay binary not found")
            return
        }
        val relayMode = mode.relayMode(platform)
        pionThread = Thread {
            try {
                if (!checkPortOrAbort()) return@Thread
            } catch (e: Exception) {
                if (isRunning) onLog("Port check error: ${e.message}")
                return@Thread
            }
            try {
                val pb = ProcessBuilder(
                    relayBin.absolutePath,
                    "--mode", relayMode,
                    "--ws-port", "${Ports.PION_SIGNALING}",
                    "--socks-host", Prefs.socksHost,
                    "--socks-port", "${Prefs.socksPort}",
                    "--socks-user", SocksAuth.user,
                    "--socks-pass", SocksAuth.pass
                )
                pb.redirectErrorStream(true)
                val proc = pb.start()
                synchronized(this) { pionProcess = proc }
                onLog("Pion relay started mode=$relayMode (signaling :${Ports.PION_SIGNALING}, SOCKS5 ${SocksAuth.user}:${SocksAuth.pass}@${Prefs.socksHost}:${Prefs.socksPort})")
                val stdinWriter = BufferedWriter(OutputStreamWriter(proc.outputStream))
                proc.inputStream.bufferedReader().forEachLine { line ->
                    if (line.startsWith("RESOLVE:")) {
                        val hostname = line.removePrefix("RESOLVE:")
                        try {
                            val all = InetAddress.getAllByName(hostname)
                            val address = all.firstOrNull { it is Inet4Address } ?: all.first()
                            val resolvedIP = address.hostAddress ?: ""
                            Log.d("RELAY", "Resolved $hostname -> $resolvedIP")
                            stdinWriter.write(resolvedIP)
                            stdinWriter.newLine()
                            stdinWriter.flush()
                        } catch (e: Exception) {
                            Log.e("RELAY", "DNS resolve failed for $hostname", e)
                            stdinWriter.write("")
                            stdinWriter.newLine()
                            stdinWriter.flush()
                        }
                    } else {
                        Log.d("RELAY", line)
                        onLog(line)
                        if (line.contains("CONNECTED")) onStatus(VpnStatus.TUNNEL_ACTIVE)
                        else if (line.contains("session cleaned up")) onStatus(VpnStatus.TUNNEL_LOST)
                    }
                }
                onLog("Pion relay exited: ${proc.exitValue()}")
            } catch (e: Exception) {
                if (isRunning) {
                    Log.e("RELAY", "Pion relay error", e)
                    onLog("Pion relay error: ${e.message}")
                }
            }
        }.also { it.start() }
    }

    private fun checkPortOrAbort(): Boolean {
        val socksPort = Prefs.socksPort
        if (PortGuard.ensurePortFree(socksPort)) return true
        onLog("SOCKS5 port $socksPort is busy and could not be freed")
        onStatus(VpnStatus.PORT_BUSY)
        isRunning = false
        return false
    }
}
