package bypass.whitelist

import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

class AndroidVpnManifestContractTest {
    @Test
    fun `Android 14 special-use VPN service declares its subtype property`() {
        val manifest = File("src/main/AndroidManifest.xml").readText()
        val tunnelService = Regex(
            """<service\s+[^>]*android:name="\.tunnel\.TunnelVpnService"[\s\S]*?</service>""",
        ).find(manifest)?.value.orEmpty()

        assertTrue(tunnelService.contains("android:foregroundServiceType=\"specialUse\""))
        assertTrue(tunnelService.contains("android.app.PROPERTY_SPECIAL_USE_FGS_SUBTYPE"))
        assertTrue(tunnelService.contains("android:value=\"vpn_tunnel_packet_forwarding\""))
    }
}
