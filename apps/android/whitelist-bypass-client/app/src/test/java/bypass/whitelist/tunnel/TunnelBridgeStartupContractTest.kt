package bypass.whitelist.tunnel

import java.util.Collections
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.TimeoutException
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference
import kotlin.concurrent.thread
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test

class TunnelBridgeStartupContractTest {
    @Test
    fun `direct DNS bypass uses exact host routes and removes duplicates`() {
        assertEquals(
            listOf(
                DirectDnsBypassRoute("192.168.2.1", 32),
                DirectDnsBypassRoute("2001:4860:4860::8888", 128),
            ),
            buildDirectDnsBypassRoutes(
                listOf("192.168.2.1", " 192.168.2.1 ", "2001:4860:4860::8888"),
            ),
        )
    }

    @Test
    fun `Go runtime bridge receives configured port with empty authentication`() {
        val endpoint = LoopbackSocksEndpoint.runtime("127.0.0.1", 1085)
        val payload = endpoint.toServicePayload()
        val restoredEndpoint = LoopbackSocksEndpoint.runtimeFromServicePayload(payload)
        var captured: Tun2SocksBridgeArgs? = null

        val error = invokeTun2SocksBridge(fd = 7, mtu = 1500, endpoint = restoredEndpoint) { args ->
            captured = args
            null
        }

        assertEquals(null, error)
        assertEquals(Tun2SocksBridgeArgs(7, 1500, 1085, "", ""), captured)
    }

    @Test
    fun `explicit legacy authenticated bridge keeps configured credentials`() {
        val endpoint = LoopbackSocksEndpoint.legacy("127.0.0.1", 1080, "legacy-user", "legacy-pass")
        var captured: Tun2SocksBridgeArgs? = null

        invokeTun2SocksBridge(fd = 9, mtu = 1400, endpoint = endpoint) { args ->
            captured = args
            null
        }

        assertEquals(Tun2SocksBridgeArgs(9, 1400, 1080, "legacy-user", "legacy-pass"), captured)
    }

    @Test
    fun `descriptor wrapper is released after successful raw fd detach`() {
        var wrapperCloses = 0

        val fd = detachOwnedTunDescriptor(detach = { 13 }, closeOwned = { wrapperCloses += 1 })

        assertEquals(13, fd)
        assertEquals(1, wrapperCloses)
    }

    @Test
    fun `owned descriptor is closed in finally when detach fails`() {
        var closes = 0

        val error = runCatching {
            detachOwnedTunDescriptor(
                detach = { throw IllegalStateException("detach failed") },
                closeOwned = { closes += 1 },
            )
        }.exceptionOrNull()

        assertTrue(error is IllegalStateException)
        assertEquals(1, closes)
    }

    @Test
    fun `product routing always excludes its own UID from capture`() {
        val none = buildTunnelAppRoutingPolicy(SplitTunnelingMode.NONE, emptySet(), "bypass.whitelist")
        val bypass = buildTunnelAppRoutingPolicy(
            SplitTunnelingMode.BYPASS,
            setOf("org.telegram.messenger"),
            "bypass.whitelist",
        )

        assertEquals(setOf("bypass.whitelist"), none.disallowedPackages)
        assertEquals(setOf("bypass.whitelist", "org.telegram.messenger"), bypass.disallowedPackages)
        assertTrue(none.allowedPackages.isEmpty())
    }

    @Test
    fun `only routing refuses to allow-list the VPN app UID`() {
        val error = runCatching {
            buildTunnelAppRoutingPolicy(
                SplitTunnelingMode.ONLY,
                setOf("bypass.whitelist"),
                "bypass.whitelist",
            )
        }.exceptionOrNull()

        assertTrue(error is IllegalArgumentException)
    }

    @Test
    fun `nonblocking native start return publishes active without closing native-owned fd`() {
        var rawFdCloses = 0
        val ownership = DetachedTunFdOwnership(fd = 17) { rawFdCloses += 1 }
        val contract = TunnelBridgeStartupContract()

        val error = runTun2SocksHandoff(ownership) { null }
        val ready = contract.nativeStartReturned(error)
        ownership.closeBeforeNativeHandoff()

        assertEquals(null, error)
        assertTrue(ready.publishActive)
        assertEquals(VpnStatus.TUNNEL_ACTIVE, ready.status)
        assertTrue(ownership.isNativeOwned())
        assertEquals(0, rawFdCloses)
    }

    @Test
    fun `failed native handoff closes raw fd exactly once and fails closed`() {
        var rawFdCloses = 0
        val ownership = DetachedTunFdOwnership(fd = 19) { rawFdCloses += 1 }
        val contract = TunnelBridgeStartupContract()
        val expected = IllegalStateException("native init failed")

        val error = runTun2SocksHandoff(ownership) { expected }
        val failed = contract.nativeStartReturned(error)
        ownership.closeBeforeNativeHandoff()

        assertEquals(expected, error)
        assertEquals(VpnStatus.CALL_FAILED, failed.status)
        assertTrue(failed.stopService)
        assertFalse(failed.publishActive)
        assertEquals(1, rawFdCloses)
    }

    @Test
    fun `cancellation before native invocation closes raw fd exactly once`() {
        var rawFdCloses = 0
        val ownership = DetachedTunFdOwnership(fd = 23) { rawFdCloses += 1 }
        val contract = TunnelBridgeStartupContract()

        ownership.closeBeforeNativeHandoff()
        ownership.closeBeforeNativeHandoff()
        val cancelled = contract.cancelledBeforeNativeStart()

        assertEquals(1, rawFdCloses)
        assertTrue(cancelled.stopService)
        assertFalse(cancelled.publishActive)
    }

    @Test
    fun `stop during blocked native start performs exactly once deferred native stop`() {
        val events = Collections.synchronizedList(mutableListOf<String>())
        val startEntered = CountDownLatch(1)
        val releaseStart = CountDownLatch(1)
        val rawFdCloses = AtomicInteger(0)
        val nativeStops = AtomicInteger(0)
        val ownership = DetachedTunFdOwnership(fd = 29) { rawFdCloses.incrementAndGet() }
        val lifecycle = Tun2SocksHandoffLifecycle(ownership)
        val startResult = AtomicReference<Throwable?>()
        val nativeStop = {
            events += "native-stop"
            nativeStops.incrementAndGet()
            null
        }
        val startThread = thread(name = "blocked-native-start") {
            startResult.set(lifecycle.runStart(
                start = {
                    events += "native-start-entered"
                    startEntered.countDown()
                    releaseStart.await()
                    events += "native-start-returned"
                    null
                },
                stop = nativeStop,
            ))
        }

        assertTrue(startEntered.await(1, TimeUnit.SECONDS))
        val boundedStop = lifecycle.requestStopAndAwait(stop = nativeStop, timeoutMillis = 10)
        assertTrue(boundedStop is TimeoutException)
        assertEquals(0, nativeStops.get())

        releaseStart.countDown()
        startThread.join(1_000)

        assertFalse(startThread.isAlive)
        assertNull(startResult.get())
        assertEquals(listOf("native-start-entered", "native-start-returned", "native-stop"), events)
        assertEquals(1, nativeStops.get())
        assertEquals(0, rawFdCloses.get())
        assertTrue(ownership.isClosed())
        assertNull(lifecycle.requestStopAndAwait(stop = nativeStop, timeoutMillis = 10))
        assertEquals(1, nativeStops.get())
    }

    @Test
    fun `stop during blocked native start preserves start failure and closes Kotlin fd`() {
        val startEntered = CountDownLatch(1)
        val releaseStart = CountDownLatch(1)
        val rawFdCloses = AtomicInteger(0)
        val nativeStops = AtomicInteger(0)
        val expectedStartError = IllegalStateException("native start failed")
        val ownership = DetachedTunFdOwnership(fd = 31) { rawFdCloses.incrementAndGet() }
        val lifecycle = Tun2SocksHandoffLifecycle(ownership)
        val startResult = AtomicReference<Throwable?>()
        val nativeStop = { nativeStops.incrementAndGet(); null }
        val startThread = thread(name = "failing-native-start") {
            startResult.set(lifecycle.runStart(
                start = {
                    startEntered.countDown()
                    releaseStart.await()
                    expectedStartError
                },
                stop = nativeStop,
            ))
        }

        assertTrue(startEntered.await(1, TimeUnit.SECONDS))
        assertTrue(lifecycle.requestStopAndAwait(stop = nativeStop, timeoutMillis = 10) is TimeoutException)
        releaseStart.countDown()
        startThread.join(1_000)

        assertSame(expectedStartError, startResult.get())
        assertSame(expectedStartError, lifecycle.requestStopAndAwait(stop = nativeStop, timeoutMillis = 10))
        assertEquals(0, nativeStops.get())
        assertEquals(1, rawFdCloses.get())
        assertTrue(ownership.isClosed())
    }

    @Test
    fun `deferred native stop failure remains authoritative and is never retried`() {
        val startEntered = CountDownLatch(1)
        val releaseStart = CountDownLatch(1)
        val nativeStops = AtomicInteger(0)
        val expectedStopError = IllegalStateException("native stop failed")
        val ownership = DetachedTunFdOwnership(fd = 37) { }
        val lifecycle = Tun2SocksHandoffLifecycle(ownership)
        val nativeStop = { nativeStops.incrementAndGet(); expectedStopError }
        val startThread = thread(name = "stop-failure-native-start") {
            lifecycle.runStart(
                start = {
                    startEntered.countDown()
                    releaseStart.await()
                    null
                },
                stop = nativeStop,
            )
        }

        assertTrue(startEntered.await(1, TimeUnit.SECONDS))
        assertTrue(lifecycle.requestStopAndAwait(stop = nativeStop, timeoutMillis = 10) is TimeoutException)
        releaseStart.countDown()
        startThread.join(1_000)

        assertSame(expectedStopError, lifecycle.requestStopAndAwait(stop = nativeStop, timeoutMillis = 10))
        assertEquals(1, nativeStops.get())
        assertTrue(ownership.isNativeOwned())
    }

    @Test
    fun `native stop timeout keeps lifecycle stopping until late completion closes fd`() {
        val ownership = DetachedTunFdOwnership(fd = 41) { }
        val lifecycle = Tun2SocksHandoffLifecycle(ownership)
        var pendingReference: Tun2SocksStopPending? = null
        val pendingStop = {
            Tun2SocksStopPending("native stop still running").also { pending ->
                pendingReference = pending
            }
        }

        assertNull(lifecycle.runStart(start = { null }, stop = pendingStop))
        assertTrue(lifecycle.requestStopAndAwait(pendingStop, timeoutMillis = 1) is TimeoutException)
        assertTrue(lifecycle.isStopping())
        assertTrue(ownership.isNativeOwned())

        checkNotNull(pendingReference).complete(null)

        assertTrue(lifecycle.isStopped())
        assertTrue(ownership.isClosed())
    }

    @Test
    fun `process barrier retains lifecycle and rejects start across service destruction until late stop success`() {
        Tun2SocksProcessBarrier.resetForTests()
        try {
            val reservation = checkNotNull(Tun2SocksProcessBarrier.reserveStart())
            val ownership = DetachedTunFdOwnership(fd = 43) { }
            val lifecycle = Tun2SocksHandoffLifecycle(ownership) { observed, error ->
                Tun2SocksProcessBarrier.observeStop(observed, error)
            }
            Tun2SocksProcessBarrier.attach(reservation, lifecycle, ownership)
            assertNull(lifecycle.runStart(start = { null }, stop = { null }))

            assertNull(Tun2SocksProcessBarrier.reserveStart())
            var pendingReference: Tun2SocksStopPending? = null
            val pendingStop = {
                Tun2SocksStopPending("native stop still running").also { pendingReference = it }
            }
            assertTrue(lifecycle.requestStopAndAwait(pendingStop, timeoutMillis = 1) is TimeoutException)
            assertNull(Tun2SocksProcessBarrier.reserveStart())

            checkNotNull(pendingReference).complete(null)

            assertNotNull(Tun2SocksProcessBarrier.reserveStart())
        } finally {
            Tun2SocksProcessBarrier.resetForTests()
        }
    }

    @Test
    fun `null restart is rejected while typed runtime and explicit legacy starts are distinct`() {
        val restart = resolveTunnelStartRequest(
            runtimeHostPresent = false,
            runtimeHost = null,
            runtimePortPresent = false,
            runtimePort = 0,
            explicitLegacy = false,
        )
        val runtime = resolveTunnelStartRequest(
            runtimeHostPresent = true,
            runtimeHost = "127.0.0.1",
            runtimePortPresent = true,
            runtimePort = 1085,
            explicitLegacy = false,
        )
        val legacy = resolveTunnelStartRequest(
            runtimeHostPresent = false,
            runtimeHost = null,
            runtimePortPresent = false,
            runtimePort = 0,
            explicitLegacy = true,
        )

        assertTrue(restart is TunnelStartRequest.Rejected)
        assertEquals(LoopbackSocksEndpoint.runtime("127.0.0.1", 1085), (runtime as TunnelStartRequest.Runtime).endpoint)
        assertTrue(legacy is TunnelStartRequest.Legacy)
    }

    @Test
    fun `native stop error becomes authoritative service failure`() {
        assertEquals(VpnStatus.CALL_DISCONNECTED, vpnStatusAfterBridgeStop(null))
        assertEquals(VpnStatus.CALL_FAILED, vpnStatusAfterBridgeStop(IllegalStateException("stop failed")))
        assertEquals(
            VpnStatus.STOPPING,
            vpnStatusAfterBridgeStop(Tun2SocksStopPending("native stop still running")),
        )
    }
}
