package bypass.whitelist.planner

import org.junit.Assert.assertEquals
import org.junit.Test
import java.io.File

class RuntimeConfigStoreTest {
    @Test
    fun `android runtime assigns app private sing box config directory`() {
        val runtimeDir = File("/data/user/0/bypass.whitelist/files/wt-runtime")

        assertEquals(File(runtimeDir, "sing-box").path, normalizedSingBoxConfigDir("", runtimeDir))
        assertEquals(File(runtimeDir, "sing-box").path, normalizedSingBoxConfigDir("/data/local/tmp/wt", runtimeDir))
    }

    @Test
    fun `explicit non temporary sing box config directory is preserved`() {
        assertEquals(
            "/opt/white-transport/sing-box",
            normalizedSingBoxConfigDir("/opt/white-transport/sing-box", File("/app/files/wt-runtime")),
        )
    }

    @Test
    fun `android runtime selects packaged sing box native binary`() {
        val nativeLibraryDir = File("/data/app/bypass.whitelist/lib/arm64")

        assertEquals(
            File(nativeLibraryDir, "libsingbox.so"),
            normalizedSingBoxBinaryPath(nativeLibraryDir),
        )
    }
}
