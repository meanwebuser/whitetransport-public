package bypass.whitelist.config

import bypass.whitelist.BuildConfig
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class BuildConfigGuardInstrumentedTest {
    @Test
    fun requiredSecretsAreEmbedded() {
        assertFalse("WTBUS_KEY_B64 must be embedded", BuildConfig.WTBUS_KEY_B64.isBlank())
        assertTrue("WTBUS key should look like base64url", BuildConfig.WTBUS_KEY_B64.matches(Regex("^[A-Za-z0-9_-]+$")))
        assertFalse("VK_BOT_TOKEN must be embedded", BuildConfig.VK_BOT_TOKEN.isBlank())
        assertFalse("VK_BOT_PEER_ID must be embedded", BuildConfig.VK_BOT_PEER_ID.isBlank())
    }

    @Test
    fun androidUpdateUrlIsEmbeddedAndHttps() {
        assertFalse("ANDROID_UPDATE_URL is required unless ALLOW_NO_AUTOUPDATE=1", BuildConfig.ANDROID_UPDATE_URL.isBlank())
        assertTrue(BuildConfig.ANDROID_UPDATE_URL.startsWith("https://"))
        assertTrue(BuildConfig.ANDROID_UPDATE_URL.endsWith("update.json"))
    }

}
