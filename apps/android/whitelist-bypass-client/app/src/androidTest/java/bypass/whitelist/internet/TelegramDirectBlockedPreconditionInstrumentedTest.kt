package bypass.whitelist.internet

import android.util.Log
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import java.io.File
import java.net.InetAddress
import java.net.URL
import javax.net.ssl.HttpsURLConnection

@RunWith(AndroidJUnit4::class)
class TelegramDirectBlockedPreconditionInstrumentedTest {
    @Test
    fun telegramIsNotReachableDirectlyFromEmulator() {
        val target = InstrumentationRegistry.getArguments().getString("targetUrl")
            ?: "https://t.me/Kuplinov_Telegram/1032"
        val started = System.currentTimeMillis()
        val host = URL(target).host
        val resolved = runCatching { InetAddress.getAllByName(host).joinToString(",") { it.hostAddress ?: it.hostName } }
            .getOrElse { "DNS_ERROR:${it.javaClass.simpleName}:${it.message}" }
        val result = fetchDirect(target)
        val durationMs = System.currentTimeMillis() - started
        val report = """
            mode=direct-negative-precondition
            target=$target
            host=$host
            resolved=$resolved
            status=${result.status}
            bodyBytes=${result.bodyBytes}
            durationMs=$durationMs
            error=${result.error ?: ""}
        """.trimIndent()
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        File(context.filesDir, "telegram-direct-blocked-precondition.txt").writeText(report)
        Log.i(TAG, "telegram_direct_precondition_result ${report.replace('\n', ' ')}")

        assertTrue("DNS should still work; only Telegram TCP/HTTPS should be blocked: $report", !resolved.startsWith("DNS_ERROR"))
        assertTrue(
            "Direct Telegram unexpectedly reachable; this invalidates tunnel test environment: $report",
            result.error != null || result.status !in 200..399 || result.bodyBytes == 0,
        )
    }

    private fun fetchDirect(target: String): FetchResult = try {
        val conn = (URL(target).openConnection() as HttpsURLConnection).apply {
            instanceFollowRedirects = true
            connectTimeout = 7_000
            readTimeout = 10_000
            requestMethod = "GET"
            setRequestProperty("User-Agent", "BEZabotny-NET Android DirectBlockedPrecondition")
        }
        val status = conn.responseCode
        val bytes = (if (status >= 400) conn.errorStream else conn.inputStream)?.use { it.readBytes() } ?: ByteArray(0)
        FetchResult(status, bytes.size, null)
    } catch (t: Throwable) {
        FetchResult(-1, 0, "${t.javaClass.name}: ${t.message}")
    }

    private data class FetchResult(val status: Int, val bodyBytes: Int, val error: String?)

    companion object { private const val TAG = "TelegramDirectPrecondition" }
}
