package bypass.whitelist.longrun

import android.content.Intent
import android.os.SystemClock
import androidx.test.filters.LargeTest
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assert.assertTrue
import org.junit.Test

@LargeTest
class NetworkFlapLongInstrumentedTest {
    @Test
    fun appSurvivesNetworkFlaps() {
        val args = InstrumentationRegistry.getArguments()
        val durationSec = args.getString("durationSec")?.toLongOrNull() ?: 600L
        val flapEverySec = args.getString("flapEverySec")?.toLongOrNull() ?: 60L
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val context = instrumentation.targetContext
        val intent = context.packageManager.getLaunchIntentForPackage(context.packageName)!!
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        val activity = instrumentation.startActivitySync(intent)
        instrumentation.waitForIdleSync()
        val started = SystemClock.elapsedRealtime()
        var flaps = 0
        try {
            while (SystemClock.elapsedRealtime() - started < durationSec * 1000L) {
                instrumentation.uiAutomation.executeShellCommand("svc wifi disable; svc data disable").close()
                SystemClock.sleep(1500)
                instrumentation.uiAutomation.executeShellCommand("svc wifi enable; svc data enable").close()
                flaps++
                SystemClock.sleep((flapEverySec * 1000L).coerceAtLeast(3000L))
                assertTrue("activity should stay alive after flap $flaps", !activity.isFinishing)
            }
        } finally {
            instrumentation.uiAutomation.executeShellCommand("svc wifi enable; svc data enable").close()
            activity.finish()
        }
        assertTrue("expected at least one network flap", flaps > 0)
    }
}
