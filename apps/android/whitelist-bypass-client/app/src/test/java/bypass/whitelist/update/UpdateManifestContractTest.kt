package bypass.whitelist.update

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class UpdateManifestContractTest {
    @Test
    fun updateManifestAcceptsUrlAndApkUrlAliases() {
        val manifest = mapOf(
            "version" to "0.3.16",
            "versionName" to "0.3.16",
            "versionCode" to "3016",
            "url" to "https://downloads.example.invalid/white-transport/client.apk",
            "apkUrl" to "https://downloads.example.invalid/white-transport/client.apk",
        )
        val versionName = manifest["versionName"] ?: manifest["version"].orEmpty()
        val apkUrl = manifest["apkUrl"] ?: manifest["url"].orEmpty()
        assertEquals("0.3.16", versionName)
        assertTrue(apkUrl.startsWith("https://downloads.example.invalid/white-transport/"))
        assertTrue(apkUrl.endsWith(".apk"))
        assertEquals("3016", manifest["versionCode"])
    }

    @Test
    fun legacyUpdateManifestWithOnlyUrlStillParses() {
        val manifest = mapOf(
            "version" to "0.3.16",
            "versionCode" to "3016",
            "url" to "https://example.test/app.apk",
        )
        val versionName = manifest["versionName"] ?: manifest["version"].orEmpty()
        val apkUrl = manifest["apkUrl"] ?: manifest["url"].orEmpty()
        assertEquals("0.3.16", versionName)
        assertEquals("https://example.test/app.apk", apkUrl)
    }
}
