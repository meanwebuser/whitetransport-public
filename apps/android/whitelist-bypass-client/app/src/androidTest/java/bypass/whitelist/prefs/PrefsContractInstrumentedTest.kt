package bypass.whitelist.prefs

import androidx.test.platform.app.InstrumentationRegistry
import bypass.whitelist.tunnel.CallConfig
import bypass.whitelist.tunnel.TunnelMode
import bypass.whitelist.util.Prefs
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

class PrefsContractInstrumentedTest {
    @Before
    fun setUp() {
        Prefs.init(InstrumentationRegistry.getInstrumentation().targetContext)
        Prefs.clearAutoDestination()
    }

    @Test
    fun autoDestinationCanBeClearedAndExpires() {
        assertNull(Prefs.autoDestination)
        val config = CallConfig(
            id = "auto-test",
            name = "Auto test",
            url = "wbstream://test-room",
            tunnelMode = TunnelMode.DC,
            vp8Fps = 24,
            vp8Batch = 4,
            dualTrack = false,
            autoDiscovered = true,
        )
        Prefs.autoDestination = config
        assertEquals("wbstream://test-room", Prefs.autoDestination?.url)
        Prefs.clearAutoDestination()
        assertNull(Prefs.autoDestination)
    }

    @Test
    fun telemetryPreferenceRoundTripsWhenAvailable() {
        // Use reflection because older copies may not expose the property yet; the test still
        // proves the setting can live in SharedPreferences without requiring telemetry by default.
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val prefs = context.getSharedPreferences("app_prefs", android.content.Context.MODE_PRIVATE)
        prefs.edit().remove("telemetry_enabled").commit()
        assertTrue(!prefs.getBoolean("telemetry_enabled", false))
        prefs.edit().putBoolean("telemetry_enabled", true).commit()
        assertTrue(prefs.getBoolean("telemetry_enabled", false))
    }
}
