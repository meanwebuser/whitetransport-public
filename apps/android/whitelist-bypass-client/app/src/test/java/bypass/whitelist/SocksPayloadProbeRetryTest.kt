package bypass.whitelist

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class SocksPayloadProbeRetryTest {
    @Test
    fun `external payload verifier uses the TLS IP endpoint`() {
        assertEquals(443, SOCKS_PAYLOAD_PROBE_PORT)
        assertEquals("api.ipify.org", SOCKS_PAYLOAD_PROBE_HOST)
        assertEquals("/?format=text", SOCKS_PAYLOAD_PROBE_PATH)
        assertTrue(SOCKS_PAYLOAD_PROBE_TLS)
    }

    @Test
    fun `transient socks probe failure is retried before lifecycle gives up`() {
        var attempts = 0
        val sleeps = mutableListOf<Long>()

        val result = retrySocksPayloadProbe(
            attempts = 3,
            retryDelayMs = 250L,
            sleep = { sleeps += it },
        ) {
            attempts += 1
            attempts == 3
        }

        assertTrue(result)
        assertEquals(3, attempts)
        assertEquals(listOf(250L, 250L), sleeps)
    }
}
