package bypass.whitelist.internet

import android.content.Intent
import android.util.Log
import android.os.Process
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import bypass.whitelist.discovery.VkDiscoveryScanner
import bypass.whitelist.planner.PlannerApiClient
import bypass.whitelist.tunnel.CallConfig
import bypass.whitelist.tunnel.HeadlessSessionService
import bypass.whitelist.tunnel.SplitTunnelingMode
import bypass.whitelist.tunnel.TunnelMode
import bypass.whitelist.tunnel.TunnelServiceState
import bypass.whitelist.tunnel.VpnStatus
import bypass.whitelist.util.Prefs
import bypass.whitelist.util.SocksAuthMode
import org.junit.After
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import java.io.File
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.Proxy
import java.net.URL
import java.util.UUID
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import javax.net.ssl.HttpsURLConnection

@RunWith(AndroidJUnit4::class)
class TelegramThroughTunnelE2EInstrumentedTest {
    private val context = InstrumentationRegistry.getInstrumentation().targetContext
    private val vpnStatusOwner = Any()

    @After
    fun tearDown() {
        runCatching {
            context.startService(Intent(context, HeadlessSessionService::class.java).setAction(HeadlessSessionService.ACTION_STOP))
        }
        TunnelServiceState.detachVpnStatusCallback(vpnStatusOwner)
        TunnelServiceState.logCallback = null
    }

    @Test
    fun telegramIsReachableThroughAppTunnelAndNotDirectly() {
        val args = InstrumentationRegistry.getArguments()
        System.setProperty("java.net.preferIPv4Stack", "true")
        System.setProperty("java.net.preferIPv6Addresses", "false")
        val target = args.getString("targetUrl") ?: "https://t.me/Kuplinov_Telegram/1032"
        val destinationOverride = args.getString("destinationUrl") ?: ""
        val requireDirectBlocked = args.getString("requireDirectBlocked")?.toBooleanStrictOrNull() ?: true
        val discoveryTimeoutSec = args.getString("discoveryTimeoutSec")?.toLongOrNull() ?: 120L
        val tunnelModeOverride = args.getString("tunnelMode")
            ?.takeIf { it.isNotBlank() }
            ?.let { raw -> runCatching { TunnelMode.valueOf(raw.uppercase()) }.getOrNull() }
        val dualTrackOverride = args.getString("dualTrack")?.toBooleanStrictOrNull()
        val vp8FpsOverride = args.getString("vp8Fps")?.toIntOrNull()
        val vp8BatchOverride = args.getString("vp8Batch")?.toIntOrNull()
        val plannerApiUrl = args.getString("plannerApiUrl")?.takeIf { it.isNotBlank() }

        Prefs.init(context)
        val socksPort = args.getString("socksPort")?.toLongOrNull() ?: 1080L
        val instrumentationPackage = InstrumentationRegistry.getInstrumentation().context.packageName
        val targetPackage = context.packageName
        val uidPackages = context.packageManager.getPackagesForUid(Process.myUid())?.joinToString(",") ?: ""
        Prefs.proxyOnly = true
        Prefs.headless = true
        Prefs.splitTunnelingMode = SplitTunnelingMode.NONE
        Prefs.splitTunnelingPackages = emptySet()
        Prefs.socksPort = socksPort
        // Test transport reachability, not SOCKS auth. Use no-auth so Android's JDK SOCKS
        // client cannot make the test fail before exercising the tunnel itself.
        Prefs.socksAuthMode = SocksAuthMode.MANUAL
        Prefs.socksUser = ""
        Prefs.socksPass = ""
        val config = if (destinationOverride.isNotBlank()) {
            CallConfig(
                id = "e2e-${UUID.randomUUID()}",
                name = "E2E tunnel override",
                url = destinationOverride,
                tunnelMode = tunnelModeOverride ?: TunnelMode.DC,
                vp8Fps = vp8FpsOverride ?: 24,
                vp8Batch = vp8BatchOverride ?: 4,
                dualTrack = dualTrackOverride ?: false,
                autoDiscovered = true,
            )
        } else {
            discoverFreeRoom(discoveryTimeoutSec)
        }
        val effectiveConfig = config.copy(
            tunnelMode = tunnelModeOverride ?: config.tunnelMode,
            dualTrack = dualTrackOverride ?: config.dualTrack,
            vp8Fps = vp8FpsOverride ?: config.vp8Fps,
            vp8Batch = vp8BatchOverride ?: config.vp8Batch,
        )
        Prefs.autoDestination = effectiveConfig
        Prefs.activeDestinationId = effectiveConfig.id
        val plannerReport = fetchPlannerReport(plannerApiUrl)

        val direct = fetch(target, proxy = null)
        val directBlocked = direct.error != null || direct.status !in 200..399 || direct.bodyBytes == 0
        if (requireDirectBlocked) {
            assertTrue("Direct Telegram should be blocked in this environment before tunnel: $direct", directBlocked)
        }

        val statusLog = mutableListOf<String>()
        val activeLatch = CountDownLatch(1)
        TunnelServiceState.logCallback = { line ->
            synchronized(statusLog) { statusLog.add("LOG $line") }
            Log.i(TAG, "tunnel_log $line")
        }
        TunnelServiceState.attachVpnStatusCallback(vpnStatusOwner) { status ->
            synchronized(statusLog) { statusLog.add("STATUS $status") }
            Log.i(TAG, "tunnel_status $status")
            if (status == VpnStatus.TUNNEL_ACTIVE) activeLatch.countDown()
        }

        context.startService(Intent(context, HeadlessSessionService::class.java))
        val active = activeLatch.await(120, TimeUnit.SECONDS)
        assertTrue("Tunnel did not become TUNNEL_ACTIVE. Logs: ${statusLog.joinToString(" | ")}", active)

        val proxy = Proxy(Proxy.Type.SOCKS, InetSocketAddress("127.0.0.1", socksPort.toInt()))
        val tunneled = fetch(target, proxy)
        val report = """
            mode=tunnel-e2e
            destinationUrl=${effectiveConfig.url}
            destinationTunnelMode=${effectiveConfig.tunnelMode}
            tunnelModeOverride=$tunnelModeOverride
            dualTrack=${effectiveConfig.dualTrack}
            vp8Fps=${effectiveConfig.vp8Fps}
            vp8Batch=${effectiveConfig.vp8Batch}
            destinationOverride=$destinationOverride
            plannerApiUrl=${plannerApiUrl ?: ""}
            planner=$plannerReport
            target=$target
            packageName=${context.packageName}
            proxyOnly=${Prefs.proxyOnly}
            splitTunnelingMode=${Prefs.splitTunnelingMode}
            splitTunnelingPackages=${Prefs.splitTunnelingPackages}
            socksPort=$socksPort
            socksTarget=127.0.0.1:$socksPort
            direct=$direct
            tunneled=$tunneled
            statuses=${statusLog.joinToString(" | ")}
        """.trimIndent()
        File(context.filesDir, "telegram-through-tunnel-e2e.txt").writeText(report)
        Log.i(TAG, "telegram_tunnel_e2e_result ${report.replace('\n', ' ')}")

        assertTrue("Telegram over app SOCKS tunnel failed: $report", tunneled.error == null)
        assertTrue("Unexpected HTTP status over tunnel: $report", tunneled.status in 200..399)
        assertTrue("Empty Telegram body over tunnel: $report", tunneled.bodyBytes > 0)
    }

    private fun fetchPlannerReport(plannerApiUrl: String?): String {
        if (plannerApiUrl.isNullOrBlank()) return "not-configured"
        return runCatching {
            val control = PlannerApiClient.fetchPlan(plannerApiUrl, "control", 4096).summary()
            val egress = PlannerApiClient.fetchPlan(plannerApiUrl, "egress", 5000).summary()
            val bulk = PlannerApiClient.fetchPlan(plannerApiUrl, "bulk", 1048576).summary()
            "$control | $egress | $bulk"
        }.getOrElse { "error:${it.javaClass.simpleName}:${it.message}" }
    }

    private fun discoverFreeRoom(timeoutSec: Long): CallConfig {
        val deadline = System.currentTimeMillis() + TimeUnit.SECONDS.toMillis(timeoutSec)
        var lastResult: VkDiscoveryScanner.Result? = null
        var requested = false
        while (System.currentTimeMillis() < deadline) {
            val result = VkDiscoveryScanner.scan()
            lastResult = result
            val picked = result.configs.firstOrNull()
            if (picked != null) {
                Log.i(TAG, "discovery_picked method=${result.method} source=${result.source} url=${picked.url} slot=${picked.slotId} lease=${picked.leaseId}")
                return picked
            }
            if (!requested) {
                requested = VkDiscoveryScanner.sendClientEvent(
                    type = "request_room",
                    clientId = "android-e2e-${UUID.randomUUID()}",
                    room = null,
                    reason = "e2e_no_free_rooms",
                )
                Log.i(TAG, "discovery_request_room sent=$requested method=${result.method} source=${result.source}")
            }
            Thread.sleep(5_000)
        }
        error("Discovery did not find free room in ${timeoutSec}s. Last result: method=${lastResult?.method} source=${lastResult?.source} count=${lastResult?.configs?.size ?: -1}")
    }

    private fun fetch(target: String, proxy: Proxy?): FetchResult {
        val started = System.currentTimeMillis()
        val host = URL(target).host
        val resolved = runCatching { InetAddress.getAllByName(host).joinToString(",") { it.hostAddress ?: it.hostName } }
            .getOrElse { "DNS_ERROR:${it.javaClass.simpleName}:${it.message}" }
        return try {
            val conn = ((if (proxy == null) URL(target).openConnection() else URL(target).openConnection(proxy)) as HttpsURLConnection).apply {
                instanceFollowRedirects = true
                connectTimeout = 15_000
                readTimeout = 20_000
                requestMethod = "GET"
                setRequestProperty("User-Agent", "BEZabotny-NET Android TunnelE2E")
                setRequestProperty("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
            }
            val status = conn.responseCode
            val bytes = (if (status >= 400) conn.errorStream else conn.inputStream)?.use { it.readBytes() } ?: ByteArray(0)
            FetchResult(status, bytes.size, conn.url?.toString(), resolved, System.currentTimeMillis() - started, null)
        } catch (t: Throwable) {
            FetchResult(-1, 0, null, resolved, System.currentTimeMillis() - started, "${t.javaClass.name}: ${t.message}")
        }
    }

    private data class FetchResult(
        val status: Int,
        val bodyBytes: Int,
        val finalUrl: String?,
        val resolved: String,
        val durationMs: Long,
        val error: String?,
    )

    companion object { private const val TAG = "TelegramTunnelE2E" }
}
