package bypass.whitelist

import android.content.Intent
import android.net.VpnService
import androidx.core.content.ContextCompat
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import bypass.whitelist.tunnel.SplitTunnelingMode
import bypass.whitelist.tunnel.TunnelServiceState
import bypass.whitelist.tunnel.TunnelVpnService
import bypass.whitelist.tunnel.VpnStatus
import bypass.whitelist.util.Prefs
import org.junit.Assert.assertFalse
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Test
import org.junit.runner.RunWith
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference

/** Launcher wiring proof; transport/VPN readiness remains a separate installed smoke. */
@RunWith(AndroidJUnit4::class)
class RuntimeLaunchInstrumentedTest {
    @Test
    fun launcherActivityOwnsCoordinatorAndVpnConsentEntryPoint() {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val context = instrumentation.targetContext
        val initialSubscriberCount = TunnelServiceState.vpnStatusSubscriberCount()
        val launchIntent = context.packageManager.getLaunchIntentForPackage(context.packageName)
        assertNotNull("launcher intent must exist", launchIntent)
        val activity = instrumentation.startActivitySync(
            launchIntent!!.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        )
        try {
            assertTrue("launcher must be CapacitorMainActivity", activity is CapacitorMainActivity)
            val capacitorActivity = activity as CapacitorMainActivity
            assertNotNull("launcher coordinator must be initialized", capacitorActivity.vpnCoordinator)
            assertEquals(
                "launcher must add one owned service status callback",
                initialSubscriberCount + 1,
                TunnelServiceState.vpnStatusSubscriberCount(),
            )

            val permissionOutcome = AtomicReference<Result<Boolean>?>(null)
            instrumentation.runOnMainSync {
                capacitorActivity.vpnCoordinator.requestVpnPermission { permissionOutcome.set(it) }
            }
            instrumentation.waitForIdleSync()
            val lifecycle = capacitorActivity.vpnCoordinator.status().lifecycle
            assertTrue(
                "consent entry point must resolve granted consent or publish permission_required",
                permissionOutcome.get()?.isSuccess == true || lifecycle == CapacitorVpnLifecycle.PERMISSION_REQUIRED,
            )
        } finally {
            instrumentation.runOnMainSync { activity.finish() }
            val destroyDeadline = System.currentTimeMillis() + 5_000L
            while (!activity.isDestroyed && System.currentTimeMillis() < destroyDeadline) {
                instrumentation.waitForIdleSync()
                Thread.sleep(50)
            }
            assertTrue("finished launcher must reach onDestroy", activity.isDestroyed)
            assertEquals(
                "destroyed launcher must detach only its owned status callback",
                initialSubscriberCount,
                TunnelServiceState.vpnStatusSubscriberCount(),
            )
        }
    }

    @Test
    fun invalidOnlyPackageFailsServiceAndCleansForegroundState() {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val context = instrumentation.targetContext
        Prefs.init(context)
        val previousMode = Prefs.splitTunnelingMode
        val previousPackages = Prefs.splitTunnelingPackages
        val owner = Any()
        val failed = CountDownLatch(1)
        TunnelServiceState.attachVpnStatusCallback(owner) { status ->
            if (status == VpnStatus.CALL_FAILED) failed.countDown()
        }
        try {
            Prefs.splitTunnelingMode = SplitTunnelingMode.ONLY
            Prefs.splitTunnelingPackages = setOf("com.whitetransport.missing.fixture")

            val serviceIntent = Intent(context, TunnelVpnService::class.java).apply {
                putExtra(TunnelVpnService.EXTRA_RUNTIME_SOCKS_HOST, "127.0.0.1")
                putExtra(TunnelVpnService.EXTRA_RUNTIME_SOCKS_PORT, 1085)
            }
            ContextCompat.startForegroundService(context, serviceIntent)

            assertTrue("invalid app-only rule must publish CALL_FAILED", failed.await(10, TimeUnit.SECONDS))
            val cleanupDeadline = System.currentTimeMillis() + 5_000L
            while (TunnelVpnService.instance != null && System.currentTimeMillis() < cleanupDeadline) Thread.sleep(50)
            assertFalse("failed split rule must not report active TUN", TunnelVpnService.instance?.isAuthoritativelyActive() == true)
        } finally {
            TunnelVpnService.requestStop(context)
            TunnelServiceState.detachVpnStatusCallback(owner)
            Prefs.splitTunnelingMode = previousMode
            Prefs.splitTunnelingPackages = previousPackages
        }
    }

    @Test
    fun foregroundTunnelPublishesActiveAndCleansUpWithInstalledSplitPackage() {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        assumeTrue(
            "set instrumentation argument requireTun=true for the explicit installed TUN proof",
            InstrumentationRegistry.getArguments().getString("requireTun") == "true",
        )
        val context = instrumentation.targetContext
        Prefs.init(context)
        assertNull("device smoke requires VPN consent to be granted before instrumentation", VpnService.prepare(context))
        val routedPackage = listOf("com.android.chrome", "com.google.android.youtube", "com.android.settings")
            .firstOrNull { runCatching { context.packageManager.getPackageInfo(it, 0) }.isSuccess }
        assertNotNull("device smoke needs one installed package for ONLY routing", routedPackage)
        val previousMode = Prefs.splitTunnelingMode
        val previousPackages = Prefs.splitTunnelingPackages
        val owner = Any()
        val active = CountDownLatch(1)
        val disconnected = CountDownLatch(1)
        TunnelServiceState.attachVpnStatusCallback(owner) { status ->
            if (status == VpnStatus.TUNNEL_ACTIVE) active.countDown()
            if (status == VpnStatus.CALL_DISCONNECTED || status == VpnStatus.CALL_FAILED || status == VpnStatus.TUNNEL_LOST) {
                disconnected.countDown()
            }
        }
        try {
            Prefs.splitTunnelingMode = SplitTunnelingMode.ONLY
            Prefs.splitTunnelingPackages = setOf(routedPackage!!)

            val serviceIntent = Intent(context, TunnelVpnService::class.java).apply {
                putExtra(TunnelVpnService.EXTRA_RUNTIME_SOCKS_HOST, "127.0.0.1")
                putExtra(TunnelVpnService.EXTRA_RUNTIME_SOCKS_PORT, 1085)
            }
            ContextCompat.startForegroundService(context, serviceIntent)

            assertTrue("foreground VPN service must publish TUNNEL_ACTIVE after bridge startup", active.await(15, TimeUnit.SECONDS))
            assertTrue("service must expose authoritative active state", TunnelVpnService.instance?.isAuthoritativelyActive() == true)
            val externalProbeHoldSeconds = InstrumentationRegistry.getArguments().getString("holdTunSeconds")?.toLongOrNull() ?: 0L
            assertTrue("external TUN probe hold must be between 0 and 300 seconds", externalProbeHoldSeconds in 0L..300L)
            if (externalProbeHoldSeconds > 0L) {
                // Keep the real VPN alive so a host runner can inspect UID rules and send payloads from other app UIDs.
                Thread.sleep(TimeUnit.SECONDS.toMillis(externalProbeHoldSeconds))
            }
            TunnelVpnService.requestStop(context)
            assertTrue("foreground VPN service must publish cleanup state", disconnected.await(10, TimeUnit.SECONDS))
            val cleanupDeadline = System.currentTimeMillis() + 5_000L
            while (TunnelVpnService.instance != null && System.currentTimeMillis() < cleanupDeadline) Thread.sleep(50)
            assertFalse("stopped VPN service must clear authoritative active state", TunnelVpnService.instance?.isAuthoritativelyActive() == true)
        } finally {
            TunnelVpnService.requestStop(context)
            TunnelServiceState.detachVpnStatusCallback(owner)
            Prefs.splitTunnelingMode = previousMode
            Prefs.splitTunnelingPackages = previousPackages
        }
    }
}
