package bypass.whitelist

import android.app.AlertDialog
import android.content.Intent
import android.net.VpnService
import android.os.Bundle
import android.text.InputType
import android.util.Log
import android.view.Gravity
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.RadioButton
import android.widget.RadioGroup
import android.widget.ScrollView
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.FileProvider
import bypass.whitelist.planner.GoRuntimeController
import bypass.whitelist.planner.RuntimeConfigStore
import bypass.whitelist.tunnel.SplitTunnelingMode
import bypass.whitelist.util.LogWriter
import bypass.whitelist.util.Prefs
import org.json.JSONArray
import java.io.IOException
import java.net.InetSocketAddress
import java.net.Socket
import kotlin.concurrent.thread

class NativeRuntimeActivity : AppCompatActivity() {
    private val tag = "NativeRuntimeActivity"
    private lateinit var statusText: TextView
    private lateinit var detailText: TextView
    private lateinit var latencyText: TextView
    private lateinit var externalIpText: TextView
    private lateinit var vpnStatusText: TextView
    private lateinit var nodesGroup: RadioGroup
    private lateinit var portInput: EditText
    private lateinit var packagesInput: EditText
    private lateinit var primaryButton: Button
    private lateinit var refreshButton: Button
    private lateinit var probeButton: Button
    private lateinit var shareLogsButton: Button
    private lateinit var modeGroup: RadioGroup
    private lateinit var splitGroup: RadioGroup
    private lateinit var logWriter: LogWriter
    private var selectedNodeId: String? = null
    private var active = false
    private var currentSocksPort = 1085

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        Prefs.init(this)
        logWriter = LogWriter(cacheDir, maxDisplayLines = 300)
        logWriter.reset()
        appendRuntimeLog("Native runtime screen opened log_file=${logWriter.file.absolutePath}")
        currentSocksPort = RuntimeConfigStore.configuredSocksPort(this)
        setContentView(buildView())
        refreshState(startRuntime = false)
        primaryButton.setOnClickListener { toggleConnection() }
        refreshButton.setOnClickListener { refreshState(startRuntime = true) }
        probeButton.setOnClickListener { runPayloadProbe() }
        shareLogsButton.setOnClickListener { shareLogs() }
        GuiTestLaunchRunner.maybeRun(this, intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        GuiTestLaunchRunner.maybeRun(this, intent)
    }

    private fun buildView(): View {
        val density = resources.displayMetrics.density
        fun dp(value: Int): Int = (value * density).toInt()
        fun label(text: String, size: Float = 13f): TextView = TextView(this).apply {
            this.text = text
            textSize = size
            setTextColor(0xff8ac7ad.toInt())
            setPadding(0, dp(16), 0, dp(6))
        }

        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(22), dp(28), dp(22), dp(18))
            setBackgroundColor(0xff07100d.toInt())
        }
        val content = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }

        content.addView(TextView(this).apply {
            text = "WhiteTransport Android"
            textSize = 28f
            setTextColor(0xffeafff6.toInt())
            gravity = Gravity.CENTER_HORIZONTAL
        })
        statusText = TextView(this).apply {
            text = "Checking runtime..."
            textSize = 21f
            setTextColor(0xfff6fff9.toInt())
            gravity = Gravity.CENTER_HORIZONTAL
            setPadding(0, dp(12), 0, dp(4))
        }
        content.addView(statusText)
        detailText = TextView(this).apply {
            textSize = 13f
            setTextColor(0xffb6d8c5.toInt())
            gravity = Gravity.CENTER_HORIZONTAL
        }
        content.addView(detailText)

        val stats = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
        latencyText = statBox("Latency", "-")
        externalIpText = statBox("External IP", "-")
        stats.addView(latencyText, LinearLayout.LayoutParams(0, -2, 1f).apply { setMargins(0, 0, dp(6), 0) })
        stats.addView(externalIpText, LinearLayout.LayoutParams(0, -2, 1f).apply { setMargins(dp(6), 0, 0, 0) })
        content.addView(stats)

        primaryButton = Button(this).apply {
            text = "Connect"
            contentDescription = "Connect WhiteTransport"
            textSize = 18f
            minHeight = dp(62)
        }
        content.addView(primaryButton, LinearLayout.LayoutParams(-1, dp(66)).apply { setMargins(0, dp(18), 0, dp(8)) })
        val row = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
        refreshButton = Button(this).apply {
            text = "Refresh nodes"
            contentDescription = "Refresh WhiteTransport nodes"
        }
        probeButton = Button(this).apply {
            text = "Test IP"
            contentDescription = "Test WhiteTransport SOCKS payload"
        }
        row.addView(refreshButton, LinearLayout.LayoutParams(0, dp(52), 1f).apply { setMargins(0, 0, dp(6), 0) })
        row.addView(probeButton, LinearLayout.LayoutParams(0, dp(52), 1f).apply { setMargins(dp(6), 0, 0, 0) })
        content.addView(row)
        shareLogsButton = Button(this).apply {
            text = getString(R.string.share_logs)
            contentDescription = "Share WhiteTransport logs"
        }
        content.addView(shareLogsButton, LinearLayout.LayoutParams(-1, dp(48)).apply { setMargins(0, 0, 0, dp(10)) })

        content.addView(label("Node"))
        nodesGroup = RadioGroup(this).apply { orientation = RadioGroup.VERTICAL }
        content.addView(nodesGroup)

        content.addView(label("SOCKS5 on this phone"))
        portInput = EditText(this).apply {
            setText(currentSocksPort.toString())
            inputType = InputType.TYPE_CLASS_NUMBER
            setTextColor(0xffeafff6.toInt())
            setHintTextColor(0xff6f9f86.toInt())
            hint = "1085"
        }
        content.addView(portInput)
        content.addView(TextView(this).apply {
            text = "PC cannot reach phone 127.0.0.1 directly. Use apps on the phone, Android VPN mode, or adb forward."
            setTextColor(0xffb6d8c5.toInt())
            textSize = 12f
            setPadding(0, dp(4), 0, 0)
        })

        content.addView(label("Mode"))
        modeGroup = RadioGroup(this).apply {
            orientation = RadioGroup.HORIZONTAL
            addView(radio(1, "SOCKS"))
            addView(radio(2, "VPN"))
            check(if (Prefs.proxyOnly) 1 else 2)
        }
        content.addView(modeGroup)
        vpnStatusText = TextView(this).apply {
            setTextColor(0xffffd28a.toInt())
            textSize = 12f
            text = "VPN mode routes all device traffic through WhiteTransport via tun2socks."
        }
        content.addView(vpnStatusText)

        content.addView(label("Split tunneling"))
        splitGroup = RadioGroup(this).apply {
            orientation = RadioGroup.VERTICAL
            addView(radio(10, "All apps through VPN"))
            addView(radio(11, "Only listed apps through VPN"))
            addView(radio(12, "Listed apps bypass VPN"))
            check(when (Prefs.splitTunnelingMode) {
                SplitTunnelingMode.ONLY -> 11
                SplitTunnelingMode.BYPASS -> 12
                SplitTunnelingMode.NONE -> 10
            })
        }
        content.addView(splitGroup)
        packagesInput = EditText(this).apply {
            setText(Prefs.splitTunnelingPackages.joinToString("\n"))
            minLines = 3
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE
            setTextColor(0xffeafff6.toInt())
            setHintTextColor(0xff6f9f86.toInt())
            hint = "com.android.chrome\norg.telegram.messenger"
        }
        content.addView(packagesInput)
        content.addView(Button(this).apply {
            text = "Pick installed apps"
            setOnClickListener { showAppPicker() }
        })

        root.addView(ScrollView(this).apply { addView(content) }, LinearLayout.LayoutParams(-1, 0, 1f))
        return root
    }

    private fun statBox(title: String, value: String): TextView = TextView(this).apply {
        text = "$title\n$value"
        textSize = 13f
        setTextColor(0xffeafff6.toInt())
        setBackgroundColor(0xff0d221a.toInt())
        setPadding(18, 14, 18, 14)
        gravity = Gravity.CENTER
    }

    private fun radio(id: Int, title: String): RadioButton = RadioButton(this).apply {
        this.id = id
        text = title
        setTextColor(0xffeafff6.toInt())
    }

    private fun setBusy(busy: Boolean, message: String? = null) {
        primaryButton.isEnabled = !busy
        refreshButton.isEnabled = !busy
        probeButton.isEnabled = !busy && active
        message?.let { detailText.text = it }
    }

    private fun appendRuntimeLog(message: String, error: Throwable? = null) {
        val fullMessage = if (error == null) message else "$message: ${error.message ?: error.javaClass.simpleName}"
        logWriter.append(fullMessage)
        if (error == null) {
            Log.i(tag, fullMessage)
        } else {
            Log.e(tag, fullMessage, error)
        }
    }

    private fun shareLogs() {
        appendRuntimeLog("Sharing native runtime logs")
        val uri = FileProvider.getUriForFile(this, "${packageName}.fileprovider", logWriter.file)
        val share = Intent(Intent.ACTION_SEND).apply {
            type = "text/plain"
            putExtra(Intent.EXTRA_STREAM, uri)
            putExtra(Intent.EXTRA_TEXT, "WhiteTransport logs: ${logWriter.file.name}")
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        startActivity(Intent.createChooser(share, getString(R.string.share_logs)))
    }

    private fun refreshState(startRuntime: Boolean = false) {
        setBusy(startRuntime, if (startRuntime) "Starting discovery..." else null)
        appendRuntimeLog("Refresh state start_runtime=$startRuntime")
        thread(name = "wt-refresh") {
            val result = runCatching {
                if (startRuntime && GoRuntimeController.isAvailable()) GoRuntimeController.ensureStarted(this)
                GoRuntimeController.status(this)
            }
            runOnUiThread {
                setBusy(false)
                result.onSuccess { status ->
                    active = status.optBoolean("active")
                    statusText.text = if (active) "Connected" else "Ready"
                    primaryButton.text = if (active) "Disconnect" else "Connect"
                    val cfg = RuntimeConfigStore.status(this)
                    val socks = status.optString("socks_listen", "127.0.0.1:$currentSocksPort")
                    detailText.text = "backend=${status.optString("backend", "gomobile")} config=${cfg.optBoolean("provisioned")} socks=$socks"
                    renderNodes(if (GoRuntimeController.isAvailable()) GoRuntimeController.listNodes() else JSONArray())
                }.onFailure { error ->
                    statusText.text = "Runtime unavailable"
                    detailText.text = error.message ?: "Runtime error"
                    appendRuntimeLog("Runtime refresh failed", error)
                    renderNodes(JSONArray())
                }
            }
        }
    }

    private fun toggleConnection() {
        if (active) {
            setBusy(true, "Disconnecting...")
            thread(name = "wt-disconnect") {
                runCatching { GoRuntimeController.disconnect(this) }
                runOnUiThread {
                    active = false
                    statusText.text = "Ready"
                    primaryButton.text = "Connect"
                    detailText.text = "Disconnected"
                    latencyText.text = "Latency\n-"
                    externalIpText.text = "External IP\n-"
                    setBusy(false)
                }
            }
            return
        }
        if (!saveSettingsBeforeConnect()) return
        if (!Prefs.proxyOnly && VpnService.prepare(this) != null) {
            vpnStatusText.text = "Android VPN permission is required for whole-device VPN mode. Falling back to SOCKS-only proxy."
            Prefs.proxyOnly = true
            modeGroup.check(1)
        }
        setBusy(true, "Connecting through WhiteTransport...")
        thread(name = "wt-connect") {
            val startedAt = System.currentTimeMillis()
            val result = runCatching {
                appendRuntimeLog("WT_RUNTIME_UI connect start backend=native node=${selectedNodeId ?: "auto"}")
                GoRuntimeController.connect(this, selectedNodeId)
            }
            result.onSuccess { status ->
                val probe = runCatching { probeExternalIp(currentSocksPort) }
                runOnUiThread {
                    setBusy(false)
                    if (probe.isSuccess) {
                        val elapsed = System.currentTimeMillis() - startedAt
                        active = true
                        statusText.text = "Connected"
                        primaryButton.text = "Disconnect"
                        val socks = status.optString("socks_listen", "127.0.0.1:$currentSocksPort")
                        detailText.text = "Connected via $socks"
                        latencyText.text = "Latency\n${elapsed}ms"
                        externalIpText.text = "External IP\n${probe.getOrThrow()}"
                        appendRuntimeLog("WT_RUNTIME_UI connected backend=native socks=$socks")
                        appendRuntimeLog("WT_RUNTIME_UI probe ok external_ip=${probe.getOrThrow()} latency_ms=$elapsed")
                    } else {
                        active = false
                        statusText.text = "SOCKS probe failed"
                        primaryButton.text = "Connect"
                        detailText.text = probe.exceptionOrNull()?.message ?: "SOCKS payload timeout"
                        appendRuntimeLog("WT_RUNTIME_UI probe failed backend=native", probe.exceptionOrNull())
                    }
                    renderNodes(if (GoRuntimeController.isAvailable()) GoRuntimeController.listNodes() else JSONArray())
                }
            }.onFailure { error ->
                runOnUiThread {
                    setBusy(false)
                    statusText.text = "Connection failed"
                    detailText.text = error.message ?: "connect failed"
                    appendRuntimeLog("WT_RUNTIME_UI failed backend=native", error)
                }
            }
        }
    }

    private fun saveSettingsBeforeConnect(): Boolean {
        val port = portInput.text.toString().toIntOrNull()
        if (port == null || port !in 1..65535) {
            detailText.text = "Invalid SOCKS port"
            return false
        }
        currentSocksPort = port
        Prefs.proxyOnly = modeGroup.checkedRadioButtonId != 2
        Prefs.splitTunnelingMode = when (splitGroup.checkedRadioButtonId) {
            11 -> SplitTunnelingMode.ONLY
            12 -> SplitTunnelingMode.BYPASS
            else -> SplitTunnelingMode.NONE
        }
        Prefs.splitTunnelingPackages = packagesInput.text.toString()
            .lines()
            .map { it.trim() }
            .filter { it.isNotEmpty() }
            .toSet()
        val wasStarted = GoRuntimeController.status(this).optString("state") != "stopped"
        RuntimeConfigStore.setSocksPort(this, port)
        if (wasStarted && !active) {
            GoRuntimeController.stop()
        }
        return true
    }

    private fun renderNodes(nodes: JSONArray) {
        nodesGroup.removeAllViews()
        if (nodes.length() == 0) {
            nodesGroup.addView(radio(1000, "No nodes yet. Tap Refresh nodes."))
            return
        }
        for (i in 0 until nodes.length()) {
            val node = nodes.optJSONObject(i) ?: continue
            val id = node.optString("node_id", "node")
            val available = node.optBoolean("available", false)
            nodesGroup.addView(radio(2000 + i, "$id  available=$available").apply {
                isEnabled = available
                setOnClickListener { selectedNodeId = id }
            })
            if (selectedNodeId == null && available) selectedNodeId = id
        }
        for (i in 0 until nodesGroup.childCount) {
            val rb = nodesGroup.getChildAt(i) as? RadioButton ?: continue
            if (rb.text.toString().startsWith(selectedNodeId ?: "\u0000")) nodesGroup.check(rb.id)
        }
    }

    private fun runPayloadProbe() {
        setBusy(true, "Testing SOCKS payload...")
        thread(name = "wt-probe") {
            val startedAt = System.currentTimeMillis()
            val result = runCatching { probeExternalIp(currentSocksPort) }
            runOnUiThread {
                setBusy(false)
                result.onSuccess { ip ->
                    externalIpText.text = "External IP\n$ip"
                    latencyText.text = "Latency\n${System.currentTimeMillis() - startedAt}ms"
                    detailText.text = "SOCKS payload OK on 127.0.0.1:$currentSocksPort"
                    appendRuntimeLog("WT_RUNTIME_UI probe ok external_ip=$ip latency_ms=${System.currentTimeMillis() - startedAt}")
                }.onFailure { error ->
                    detailText.text = error.message ?: "SOCKS payload failed"
                    appendRuntimeLog("WT_RUNTIME_UI probe failed backend=native", error)
                }
            }
        }
    }

    private fun probeExternalIp(port: Int): String {
        openSocksNoAuth("api.ipify.org", 80, port).use { socket ->
            val request = "GET /?format=text HTTP/1.1\r\nHost: api.ipify.org\r\nConnection: close\r\n\r\n"
            socket.getOutputStream().write(request.toByteArray(Charsets.US_ASCII))
            socket.getOutputStream().flush()
            val response = socket.getInputStream().bufferedReader(Charsets.US_ASCII).readText()
            val body = response.substringAfter("\r\n\r\n", "").trim()
            if (!Regex("^[0-9a-fA-F:.]{3,64}$").matches(body)) throw IOException("unexpected IP probe response")
            return body
        }
    }

    private fun openSocksNoAuth(host: String, targetPort: Int, socksPort: Int): Socket {
        val socket = Socket()
        socket.connect(InetSocketAddress("127.0.0.1", socksPort), 5_000)
        socket.soTimeout = 10_000
        val output = socket.getOutputStream()
        val input = socket.getInputStream()
        output.write(byteArrayOf(0x05, 0x01, 0x00))
        output.flush()
        if (input.read() != 0x05 || input.read() != 0x00) throw IOException("SOCKS no-auth rejected")
        val hostBytes = host.toByteArray(Charsets.US_ASCII)
        val request = ByteArray(4 + 1 + hostBytes.size + 2)
        request[0] = 0x05
        request[1] = 0x01
        request[2] = 0x00
        request[3] = 0x03
        request[4] = hostBytes.size.toByte()
        System.arraycopy(hostBytes, 0, request, 5, hostBytes.size)
        request[5 + hostBytes.size] = ((targetPort ushr 8) and 0xff).toByte()
        request[6 + hostBytes.size] = (targetPort and 0xff).toByte()
        output.write(request)
        output.flush()
        val head = ByteArray(4)
        if (input.read(head) != 4) throw IOException("SOCKS short reply")
        if (head[0].toInt() != 0x05 || head[1].toInt() != 0x00) throw IOException("SOCKS connect failed code=${head[1].toInt() and 0xff}")
        when (head[3].toInt()) {
            0x01 -> input.skipFully(4)
            0x03 -> input.skipFully(input.read().toLong())
            0x04 -> input.skipFully(16)
            else -> throw IOException("SOCKS bad address type")
        }
        input.skipFully(2)
        return socket
    }

    private fun java.io.InputStream.skipFully(bytes: Long) {
        var remaining = bytes
        while (remaining > 0) {
            val skipped = skip(remaining)
            if (skipped <= 0) {
                if (read() == -1) throw IOException("SOCKS reply truncated")
                remaining--
            } else {
                remaining -= skipped
            }
        }
    }

    private fun showAppPicker() {
        val apps = packageManager.getInstalledApplications(0)
            .map { it.packageName }
            .filter { it != packageName }
            .sorted()
            .take(200)
            .toTypedArray()
        val selected = Prefs.splitTunnelingPackages.toMutableSet()
        val checked = apps.map { selected.contains(it) }.toBooleanArray()
        AlertDialog.Builder(this)
            .setTitle("Split tunneling apps")
            .setMultiChoiceItems(apps, checked) { _, which, isChecked ->
                if (isChecked) selected += apps[which] else selected -= apps[which]
            }
            .setPositiveButton("Save") { _, _ ->
                Prefs.splitTunnelingPackages = selected
                packagesInput.setText(selected.sorted().joinToString("\n"))
            }
            .setNegativeButton("Cancel", null)
            .show()
    }
}
