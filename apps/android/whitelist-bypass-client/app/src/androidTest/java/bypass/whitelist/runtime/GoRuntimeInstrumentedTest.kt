package bypass.whitelist.runtime

import android.os.Bundle
import android.util.Log
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import bypass.whitelist.planner.GoRuntimeController
import bypass.whitelist.planner.RuntimeConfigStore
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.Timeout
import org.junit.runner.RunWith
import java.io.BufferedReader
import java.io.File
import java.io.InputStreamReader
import java.net.InetSocketAddress
import java.net.Socket
import java.util.concurrent.TimeUnit

@RunWith(AndroidJUnit4::class)
class GoRuntimeInstrumentedTest {
    private val instrumentation = InstrumentationRegistry.getInstrumentation()
    private val context = instrumentation.targetContext
    private val args = InstrumentationRegistry.getArguments()

    @get:Rule
    val testTimeout: Timeout = Timeout(
        args.getString("testTimeoutMs")?.toLongOrNull()?.takeIf { it > 0L } ?: DEFAULT_TEST_TIMEOUT_MS,
        TimeUnit.MILLISECONDS,
    )

    @Test
    fun expectedProbeMarkerMustBePresentInSocksResponse() {
        assertExpectedProbeBody("echo nonce=android-test-123", "nonce=android-test-123")
        try {
            assertExpectedProbeBody("echo nonce=other", "nonce=android-test-123")
            throw AssertionError("missing probe marker was accepted")
        } catch (_: IllegalStateException) {
            // Expected: a response without the marker is not payload proof.
        }
    }

    @Test
    fun startsRuntimeDiscoversConnectsAndTransfersSocksPayloadWhenConfigured() {
        reportStage("setup")
        val configJson = runtimeConfigJson()
        assertTrue("gomobile runtime AAR must be available", GoRuntimeController.isAvailable())
        assertTrue("runtimeConfigJson instrumentation argument is required", configJson.isNotBlank())

        RuntimeConfigStore.saveConfigJson(context, configJson)
        try {
            atStage("runtime.ensureStarted") { GoRuntimeController.ensureStarted(context) }
            val nodes = atStage("runtime.waitForNodes") { waitForNodes() }
            assertTrue("runtime should discover at least one node", nodes.length() > 0)

            val nodeId = args.getString("nodeId")?.takeIf { it.isNotBlank() }
                ?: nodes.optJSONObject(0)?.optString("node_id")?.takeIf { it.isNotBlank() }
            val connectedStatus = atStage("runtime.connect") {
                GoRuntimeController.connectRuntimeForPayload(context, nodeId)
            }
            atStage("runtime.selectEgressEndpoint") { selectExpectedEgressEndpoint(connectedStatus) }

            val socks = atStage("runtime.socksInfo") { GoRuntimeController.socksInfo() }
            val host = socks.optString("host", "127.0.0.1")
            val port = socks.optInt("port", 1080)
            val body = atStage("socks.payload") {
                socksHttpGet(
                    socksHost = host,
                    socksPort = port,
                    targetHost = args.getString("probeHost") ?: "api.ipify.org",
                    targetPort = args.getString("probePort")?.toIntOrNull() ?: 80,
                    path = args.getString("probePath") ?: "/",
                )
            }
            assertTrue("SOCKS HTTP probe should return response body", body.trim().isNotEmpty())
            args.getString("probeExpected")?.trim()?.takeIf { it.isNotEmpty() }?.let {
                assertExpectedProbeBody(body, it)
            }
        } finally {
            atStage("runtime.disconnect") { GoRuntimeController.disconnect(context) }
        }
        reportStage("complete")
    }

    private fun assertExpectedProbeBody(body: String, expectedMarker: String) {
        check(body.contains(expectedMarker)) {
            "SOCKS payload response did not contain expected marker: $expectedMarker"
        }
    }

    /** Select and verify the requested carrier boundary before probing SOCKS. */
    private fun selectExpectedEgressEndpoint(connectedStatus: org.json.JSONObject) {
        val expectedCarrier = args.getString("expectedEgressCarrier")?.trim().orEmpty()
        val exactEndpointId = args.getString("egressEndpointId")?.trim().orEmpty()
        if (expectedCarrier.isBlank() && exactEndpointId.isBlank()) return

        val endpoints = connectedStatus.getJSONArray("egress_endpoints")
        var selectedEndpoint: org.json.JSONObject? = null
        for (index in 0 until endpoints.length()) {
            val endpoint = endpoints.getJSONObject(index)
            val idMatches = exactEndpointId.isBlank() || endpoint.getString("ID") == exactEndpointId
            val carrierMatches = expectedCarrier.isBlank() || endpoint.getString("Carrier") == expectedCarrier
            if (idMatches && carrierMatches) {
                selectedEndpoint = endpoint
                break
            }
        }
        checkNotNull(selectedEndpoint) {
            "no negotiated egress endpoint matched id=$exactEndpointId carrier=$expectedCarrier"
        }

        val endpointId = selectedEndpoint.getString("ID")
        val endpointCarrier = selectedEndpoint.getString("Carrier")
        val status = GoRuntimeController.selectEgressEndpoint(endpointId)
        check(status.getString("selected_egress_endpoint_id") == endpointId) {
            "runtime selected ${status.optString("selected_egress_endpoint_id")} instead of $endpointId"
        }
        val selectedStatusEndpoint = status.getJSONArray("egress_endpoints")
            .let { selectedEndpoints ->
                (0 until selectedEndpoints.length())
                    .map { selectedEndpoints.getJSONObject(it) }
                    .firstOrNull { it.getString("ID") == endpointId }
            }
        checkNotNull(selectedStatusEndpoint) { "selected endpoint $endpointId disappeared from runtime status" }
        check(selectedStatusEndpoint.getString("Carrier") == endpointCarrier) {
            "selected endpoint carrier changed from $endpointCarrier"
        }
        if (expectedCarrier.isNotBlank()) {
            check(selectedStatusEndpoint.getString("Carrier") == expectedCarrier) {
                "selected carrier ${selectedStatusEndpoint.getString("Carrier")} != $expectedCarrier"
            }
        }
    }

    private fun reportStage(stage: String) {
        Log.i(LOG_TAG, "stage=$stage")
        instrumentation.sendStatus(1, Bundle().apply { putString("wt_stage", stage) })
    }

    private inline fun <T> atStage(stage: String, block: () -> T): T {
        reportStage(stage)
        return try {
            block()
        } catch (error: Throwable) {
            throw AssertionError("Go runtime instrumentation failed at stage=$stage: ${error.message}", error)
        }
    }

    private fun waitForNodes(): org.json.JSONArray {
        val timeoutMs = args.getString("nodeTimeoutMs")?.toLongOrNull() ?: 45_000L
        val deadline = System.currentTimeMillis() + timeoutMs
        var last = GoRuntimeController.listNodes()
        while (System.currentTimeMillis() < deadline) {
            if (last.length() > 0) return last
            Thread.sleep(1_000)
            last = GoRuntimeController.listNodes()
        }
        return last
    }

    private fun runtimeConfigJson(): String {
        val inline = args.getString("runtimeConfigJson")?.trim().orEmpty()
        if (inline.isNotBlank()) return inline
        val path = args.getString("runtimeConfigFile")?.trim().orEmpty()
        if (path.isBlank()) return ""
        return File(path).readText().trim()
    }

    private fun socksHttpGet(
        socksHost: String,
        socksPort: Int,
        targetHost: String,
        targetPort: Int,
        path: String,
    ): String {
        Socket().use { socket ->
            socket.soTimeout = 20_000
            socket.connect(InetSocketAddress(socksHost, socksPort), 10_000)
            val out = socket.getOutputStream()
            val input = socket.getInputStream()

            out.write(byteArrayOf(0x05, 0x01, 0x00))
            out.flush()
            val method = ByteArray(2)
            input.readFully(method)
            check(method[0].toInt() == 0x05 && method[1].toInt() == 0x00) { "SOCKS auth negotiation failed" }

            val hostBytes = targetHost.toByteArray(Charsets.UTF_8)
            out.write(byteArrayOf(0x05, 0x01, 0x00, 0x03, hostBytes.size.toByte()))
            out.write(hostBytes)
            out.write(byteArrayOf(((targetPort ushr 8) and 0xff).toByte(), (targetPort and 0xff).toByte()))
            out.flush()

            val header = ByteArray(4)
            input.readFully(header)
            check(header[1].toInt() == 0x00) { "SOCKS connect failed with code ${header[1].toInt()}" }
            val addressLength = when (header[3].toInt()) {
                0x01 -> 4
                0x03 -> input.read()
                0x04 -> 16
                else -> error("unsupported SOCKS address type ${header[3].toInt()}")
            }
            input.skipFully(addressLength + 2L)

            out.write("GET $path HTTP/1.1\r\nHost: $targetHost\r\nConnection: close\r\nUser-Agent: WhiteTransport-AndroidRuntimeTest\r\n\r\n".toByteArray())
            out.flush()
            val response = BufferedReader(InputStreamReader(input, Charsets.UTF_8)).readText()
            return response.substringAfter("\r\n\r\n", response)
        }
    }

    private fun java.io.InputStream.readFully(buffer: ByteArray) {
        var offset = 0
        while (offset < buffer.size) {
            val read = read(buffer, offset, buffer.size - offset)
            check(read >= 0) { "unexpected EOF" }
            offset += read
        }
    }

    private fun java.io.InputStream.skipFully(count: Long) {
        var remaining = count
        while (remaining > 0) {
            val skipped = skip(remaining)
            if (skipped <= 0) {
                check(read() >= 0) { "unexpected EOF while skipping" }
                remaining--
            } else {
                remaining -= skipped
            }
        }
    }

    private companion object {
        const val DEFAULT_TEST_TIMEOUT_MS: Long = 150_000L
        const val LOG_TAG: String = "WT-GoRuntimeE2E"
    }
}
