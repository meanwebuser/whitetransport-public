package bypass.whitelist.discovery

import bypass.whitelist.BuildConfig
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Test

class WtBusCryptoContractInstrumentedTest {
    @Test
    fun telemetryEnvelopeRoundTripsWithVersionedPayload() {
        requireWtBusKey()
        val payload = JSONObject()
            .put("v", 1)
            .put("kind", "telemetry")
            .put("level", "error")
            .put("event", "instrumented_test")
            .put("platform", "android")
            .put("message", "encrypted telemetry smoke")
        val envelope = WtBusCrypto.encryptEnvelope("wtlog1", payload)
        assertNotNull(envelope)
        val parts = envelope!!.split('.', limit = 3)
        assertEquals(listOf("wtlog1", BuildConfig.WTBUS_KEY_ID.ifBlank { "k1" }), parts.take(2))
        assertFalse("ciphertext should not contain plaintext", envelope.contains("instrumented_test"))
        val decrypted = WtBusCrypto.decryptEnvelope(parts[0], parts[1], parts[2])
        assertNotNull(decrypted)
        assertEquals(1, decrypted!!.getInt("v"))
        assertEquals("telemetry", decrypted.getString("kind"))
        assertEquals("android", decrypted.getString("platform"))
    }

    @Test
    fun wrongPrefixOrKidCannotDecrypt() {
        requireWtBusKey()
        val envelope = WtBusCrypto.encryptEnvelope("wtlog1", JSONObject().put("v", 1).put("kind", "telemetry"))!!
        val parts = envelope.split('.', limit = 3)
        assertNull(WtBusCrypto.decryptEnvelope("wtroom2", parts[1], parts[2]))
        assertNull(WtBusCrypto.decryptEnvelope(parts[0], "wrong-kid", parts[2]))
        assertNotNull(WtBusCrypto.decryptEnvelope(parts[0], parts[1], parts[2]))
    }

    @Test
    fun customAlphabetPayloadHasNoAsciiBase64AlphabetLeak() {
        requireWtBusKey()
        val a = WtBusCrypto.encryptEnvelope("wtlog1", JSONObject().put("v", 1).put("seq", 1))!!
        val b = WtBusCrypto.encryptEnvelope("wtlog1", JSONObject().put("v", 1).put("seq", 1))!!
        assertNotEquals("nonce should make envelopes unique", a, b)
        val payload = a.split('.', limit = 3)[2]
        assertTrue(payload.all { it in "АБВГДЕЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯабвгдежзийклмнопрстуфхцчшщъыьэюя" })
    }

    private fun requireWtBusKey() {
        assumeTrue("WTBUS key is required for crypto tests", BuildConfig.WTBUS_KEY_B64.isNotBlank())
    }
}
