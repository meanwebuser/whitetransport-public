package bypass.whitelist.credentials

import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class LocalUserCredentialStoreInstrumentedTest {
    @Test
    fun userCredentialsRoundTripOnlyThroughEncryptedLocalStore() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        LocalUserCredentialStore.clearAll(context)
        LocalUserCredentialStore.put(context, "vk", "local-vk-user-token")
        assertTrue(LocalUserCredentialStore.hasCredential(context, "vk"))
        assertEquals("local-vk-user-token", LocalUserCredentialStore.withAccessToken(context, "vk") { it })
        assertFalse(context.getSharedPreferences("local_user_credentials", 0)
            .getString("credential.vk", "")!!.contains("local-vk-user-token"))
        LocalUserCredentialStore.clearAll(context)
        assertFalse(LocalUserCredentialStore.hasCredential(context, "vk"))
    }
}
