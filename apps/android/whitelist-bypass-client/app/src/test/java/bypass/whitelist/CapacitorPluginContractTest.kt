package bypass.whitelist

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

class CapacitorPluginContractTest {
    @Test
    fun `capabilities emit every shared boolean contract field`() {
        val capabilities = capacitorCapabilities(systemVpnAvailable = true)

        assertEquals(
            setOf(
                "transport",
                "endpoints",
                "logs",
                "splitRouting",
                "proxyRouting",
                "systemVpn",
                "requestVpnPermission",
                "startSystemVpn",
                "stopSystemVpn",
                "smokeTest",
            ),
            capabilities.booleans.keys,
        )
        assertTrue(capabilities.booleans.getValue("transport"))
        assertTrue(capabilities.booleans.getValue("endpoints"))
        assertTrue(capabilities.booleans.getValue("logs"))
        assertFalse(capabilities.booleans.getValue("proxyRouting"))
        assertFalse(capabilities.booleans.getValue("smokeTest"))
    }

    @Test
    fun `split routing response explicitly reports LAN access disabled`() {
        val response = capacitorSplitRoutingResponse("bypass", setOf("org.telegram.messenger"))

        assertEquals(false, response["lan_access"])
        assertEquals("bypass", response["mode"])
    }

    @Test
    fun `LAN access true is rejected instead of silently ignored`() {
        val error = runCatching { requireSupportedLanAccess(true) }.exceptionOrNull()

        assertTrue(error is UnsupportedOperationException)
    }

    @Test
    fun `coordinator connect emits lifecycle markers for installed auto-debug`() {
        val source = File("src/main/java/bypass/whitelist/WtTransportPlugin.kt").readText()

        assertTrue(source.contains("WT_RUNTIME_UI connect start backend=capacitor"))
        assertTrue(source.contains("WT_RUNTIME_UI connected backend=capacitor"))
        assertTrue(source.contains("WT_RUNTIME_UI failed backend=capacitor"))
    }
}
