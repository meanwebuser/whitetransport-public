package bypass.whitelist.smoke

import android.content.Intent
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test

class AppSmokeInstrumentedTest {
    @Test
    fun launcherActivityStartsAndPackageIsCorrect() {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val context = instrumentation.targetContext
        assertTrue(context.packageName == "bypass.whitelist")
        val intent = context.packageManager.getLaunchIntentForPackage(context.packageName)
        assertNotNull("launcher intent must exist", intent)
        intent!!.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        val activity = instrumentation.startActivitySync(intent)
        try {
            instrumentation.waitForIdleSync()
            assertTrue("activity should not be finishing", !activity.isFinishing)
        } finally {
            activity.finish()
        }
    }
}
