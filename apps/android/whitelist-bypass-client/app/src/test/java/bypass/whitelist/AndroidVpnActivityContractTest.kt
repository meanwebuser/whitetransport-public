package bypass.whitelist

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

class AndroidVpnActivityContractTest {
    @Test
    fun `legacy activity uses owner-scoped VPN status callbacks`() {
        val source = File("src/main/java/bypass/whitelist/MainActivity.kt").readText()

        assertTrue(source.contains("TunnelServiceState.attachVpnStatusCallback(vpnStatusOwner)"))
        assertTrue(source.contains("TunnelServiceState.detachVpnStatusCallback(vpnStatusOwner)"))
        assertFalse(source.contains("TunnelServiceState.vpnStatusCallback = null"))
    }

    @Test
    fun `VPN notification returns to the Capacitor launcher`() {
        val source = File("src/main/java/bypass/whitelist/tunnel/TunnelVpnService.kt").readText()

        assertTrue(source.contains("Intent(this, CapacitorMainActivity::class.java)"))
    }

    @Test
    fun `VPN service is non-sticky and has no implicit legacy restart fallback`() {
        val source = File("src/main/java/bypass/whitelist/tunnel/TunnelVpnService.kt").readText()

        assertFalse(source.contains("return START_STICKY"))
        assertTrue(source.contains("EXTRA_EXPLICIT_LEGACY_ENDPOINT"))
        assertTrue(source.contains("resolveTunnelStartRequest("))
    }
}
