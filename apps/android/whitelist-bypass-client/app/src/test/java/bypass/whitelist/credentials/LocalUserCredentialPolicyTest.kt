package bypass.whitelist.credentials

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class LocalUserCredentialPolicyTest {
    @Test
    fun `only VK and OK may resolve a local user credential`() {
        assertEquals("vk", LocalUserCredentialPolicy.requireUserProvider("VK"))
        assertEquals("ok", LocalUserCredentialPolicy.requireUserProvider("ok"))
        assertThrows(IllegalArgumentException::class.java) {
            LocalUserCredentialPolicy.requireUserProvider("wbstream")
        }
    }

    @Test
    fun `non VK or OK request cannot carry a user token`() {
        assertThrows(IllegalArgumentException::class.java) {
            LocalUserCredentialPolicy.rejectUserTokenOnRequest("wbstream", "user-secret")
        }
        LocalUserCredentialPolicy.rejectUserTokenOnRequest("vk", "user-secret")
        LocalUserCredentialPolicy.rejectUserTokenOnRequest("ok", "user-secret")
    }

    @Test
    fun `only WBStream may resolve a local room session`() {
        assertEquals("wbstream", LocalUserCredentialPolicy.requireRoomSessionProvider("WBSTREAM"))
        assertThrows(IllegalArgumentException::class.java) {
            LocalUserCredentialPolicy.requireRoomSessionProvider("vk")
        }
        assertThrows(IllegalArgumentException::class.java) {
            LocalUserCredentialPolicy.requireRoomSessionProvider("dion")
        }
    }

    @Test
    fun `user credential metadata is not exportable`() {
        assertThrows(IllegalArgumentException::class.java) {
            LocalUserCredentialPolicy.rejectUserCredentialSerialization(
                mapOf("credential_scope" to "user", "access_token" to "user-secret"),
            )
        }
        LocalUserCredentialPolicy.rejectUserCredentialSerialization(
            mapOf("credential_scope" to "bootstrap", "token_id" to "vk-bootstrap"),
        )
    }
}
