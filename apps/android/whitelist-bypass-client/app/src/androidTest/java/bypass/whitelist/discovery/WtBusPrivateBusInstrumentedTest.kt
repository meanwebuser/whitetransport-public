package bypass.whitelist.discovery

import bypass.whitelist.BuildConfig
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Test
import org.junit.runner.RunWith
import androidx.test.ext.junit.runners.AndroidJUnit4

@RunWith(AndroidJUnit4::class)
class WtBusPrivateBusInstrumentedTest {
    @Test
    fun wtroomEnvelopeRoundTripAndDiscoveryParser() {
        requireWtBusKey()
        val now = nowSec()
        val room = "wbstream://019e-test-free-0001"
        val payload = JSONObject().apply {
            put("v", 2)
            put("status", "free")
            put("room", room)
            put("creator", "android-test")
            put("slot_id", "android-test-slot-1")
            put("lease_id", "android-test-lease-1")
            put("created_at", now)
            put("expires_at", now + 600)
            put("seq", now)
        }

        val envelope = encrypt("wtroom2", payload)
        assertTrue(envelope.startsWith("wtroom2."))
        val decoded = decrypt(envelope)
        assertEquals("free", decoded.getString("status"))
        assertEquals(room, decoded.getString("room"))

        val configs = VkDiscoveryScanner.parseConfigs("noise $envelope tail")
        assertEquals(1, configs.size)
        assertEquals(room, configs.single().url)
        assertEquals("android-test-slot-1", configs.single().slotId)
        assertEquals("android-test-lease-1", configs.single().leaseId)
    }

    @Test
    fun newestBusyEventAndExpiredFreeRoomsAreFiltered() {
        requireWtBusKey()
        val now = nowSec()
        val oldFree = encrypt("wtroom2", roomEvent(
            slot = "android-test-slot-busy",
            lease = "lease-free-old",
            status = "free",
            room = "wbstream://019e-test-old-free",
            seq = now,
            expiresAt = now + 600,
        ))
        val newerBusy = encrypt("wtroom2", roomEvent(
            slot = "android-test-slot-busy",
            lease = "lease-busy-new",
            status = "busy",
            room = "wbstream://019e-test-busy-new",
            seq = now + 1,
            expiresAt = now + 600,
        ))
        val expiredFree = encrypt("wtroom2", roomEvent(
            slot = "android-test-slot-expired",
            lease = "lease-expired",
            status = "free",
            room = "wbstream://019e-test-expired",
            seq = now + 2,
            expiresAt = now - 1,
        ))
        val goodFree = encrypt("wtroom2", roomEvent(
            slot = "android-test-slot-good",
            lease = "lease-good",
            status = "free",
            room = "wbstream://019e-test-good",
            seq = now + 3,
            expiresAt = now + 600,
        ))

        val configs = VkDiscoveryScanner.parseConfigs("$oldFree\n$newerBusy\n$expiredFree\n$goodFree")
        assertEquals(1, configs.size)
        assertEquals("wbstream://019e-test-good", configs.single().url)
    }

    @Test
    fun wtclient2BadRoomRequestRoundTrip() {
        requireWtBusKey()
        val now = nowSec()
        val room = "wbstream://019e-test-bad-room"
        val payload = JSONObject().apply {
            put("v", 2)
            put("type", "bad_room")
            put("client_id", "android-test-client")
            put("platform", "android")
            put("app_version", BuildConfig.VERSION_NAME)
            put("app_build", BuildConfig.VERSION_CODE)
            put("room", room)
            put("bad_rooms", JSONArray().put(room))
            put("reason", "guest_cannot_create_room")
            put("created_at", now)
            put("seq", now)
            put("nonce", "instrumented-test")
        }

        val envelope = encrypt("wtclient2", payload)
        assertTrue(envelope.startsWith("wtclient2."))
        val decoded = decrypt(envelope)
        assertEquals("bad_room", decoded.getString("type"))
        assertEquals(room, decoded.getString("room"))
        assertEquals(room, decoded.getJSONArray("bad_rooms").getString(0))
    }

    @Test
    fun envelopeAuthenticationBindsPrefixAndKeyId() {
        requireWtBusKey()
        val envelope = encrypt("wtclient2", JSONObject().apply {
            put("v", 2)
            put("type", "request_room")
            put("client_id", "android-test-client")
            put("created_at", nowSec())
        })
        val parts = envelope.split('.', limit = 3)
        assertEquals(3, parts.size)
        assertNull(WtBusCrypto.decryptEnvelope("wtroom2", parts[1], parts[2]))
        assertNull(WtBusCrypto.decryptEnvelope(parts[0], "wrong-kid", parts[2]))
        assertNotNull(WtBusCrypto.decryptEnvelope(parts[0], parts[1], parts[2]))
    }

    private fun requireWtBusKey() {
        assumeTrue("WTBUS_KEY_B64 must be embedded for private-bus tests", BuildConfig.WTBUS_KEY_B64.isNotBlank())
    }

    private fun encrypt(prefix: String, payload: JSONObject): String =
        WtBusCrypto.encryptEnvelope(prefix, payload) ?: error("encryptEnvelope returned null")

    private fun decrypt(envelope: String): JSONObject {
        val parts = envelope.split('.', limit = 3)
        assertEquals(3, parts.size)
        return WtBusCrypto.decryptEnvelope(parts[0], parts[1], parts[2]) ?: error("decryptEnvelope returned null")
    }

    private fun roomEvent(slot: String, lease: String, status: String, room: String, seq: Long, expiresAt: Long): JSONObject =
        JSONObject().apply {
            put("v", 2)
            put("status", status)
            put("room", room)
            put("creator", "android-test")
            put("slot_id", slot)
            put("lease_id", lease)
            put("created_at", seq)
            put("expires_at", expiresAt)
            put("seq", seq)
        }

    private fun nowSec(): Long = System.currentTimeMillis() / 1000L
}
