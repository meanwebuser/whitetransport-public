package bypass.whitelist

import bypass.whitelist.tunnel.SplitTunnelingMode
import bypass.whitelist.tunnel.LoopbackSocksEndpoint
import bypass.whitelist.tunnel.TunnelServiceState
import bypass.whitelist.tunnel.VpnStatus
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.concurrent.Executor
import java.util.concurrent.TimeoutException

class CapacitorVpnCoordinatorTest {
    @Test
    fun `selected server mismatch rolls back before SOCKS probe and TUN start`() {
        val dependencies = FakeDependencies(
            transportStatus = JSONObject("""{"active_node_id":"node-other","system_vpn_profile":{"selected_node_id":"node-other"}}"""),
            selectedNodeId = "node-other",
        )
        val coordinator = coordinator(dependencies)
        var outcome: Result<CapacitorVpnStatus>? = null

        coordinator.connect(nodeId = "node-requested") { outcome = it }

        assertTrue(outcome!!.isFailure)
        assertEquals(listOf("connect", "disconnect"), dependencies.events)
        assertFalse(dependencies.tunnelStartAttempted)
        assertNull(dependencies.probedEndpoint)
    }

    @Test
    fun `Go runtime endpoint reaches both payload probe and tunnel bridge unchanged`() {
        val endpoint = LoopbackSocksEndpoint.runtime("127.0.0.1", 1085)
        val dependencies = FakeDependencies(runtimeEndpoint = endpoint)
        val coordinator = coordinator(dependencies)
        var outcome: Result<CapacitorVpnStatus>? = null

        coordinator.connect(configJson = """{"socks_listen":"127.0.0.1:1085"}""") { outcome = it }

        assertTrue(outcome!!.isSuccess)
        assertSame(endpoint, dependencies.probedEndpoint)
        assertSame(endpoint, dependencies.tunnelEndpoint)
        assertEquals(1085, dependencies.probedEndpoint?.port)
        assertEquals("", dependencies.tunnelEndpoint?.username)
        assertEquals("", dependencies.tunnelEndpoint?.password)
    }

    @Test
    fun `connect reports consent required and returns denied outcome to pending call`() {
        val dependencies = FakeDependencies(hasConsent = false)
        val coordinator = coordinator(dependencies)
        var outcome: Result<CapacitorVpnStatus>? = null

        coordinator.connect { outcome = it }

        assertEquals(CapacitorVpnLifecycle.PERMISSION_REQUIRED, coordinator.status().lifecycle)
        assertTrue(dependencies.permissionRequested)
        assertNull(outcome)

        coordinator.onVpnPermissionResult(dependencies.permissionTokens.single(), granted = false)

        assertEquals(CapacitorVpnLifecycle.PERMISSION_REQUIRED, coordinator.status().lifecycle)
        assertNotNull(outcome)
        assertTrue(outcome!!.isFailure)
        assertEquals(listOf("permission"), dependencies.events)
    }

    @Test
    fun `late permission result cannot resolve a newer generation`() {
        val dependencies = FakeDependencies(hasConsent = false)
        val coordinator = coordinator(dependencies)
        val firstOutcomes = mutableListOf<Result<CapacitorVpnStatus>>()

        coordinator.connect { firstOutcomes += it }
        val staleToken = dependencies.permissionTokens.single()
        coordinator.disconnect {}
        dependencies.hasConsent = true
        val secondOutcomes = mutableListOf<Result<CapacitorVpnStatus>>()
        coordinator.connect { secondOutcomes += it }

        coordinator.onVpnPermissionResult(staleToken, granted = false)

        assertEquals(1, firstOutcomes.size)
        assertTrue(firstOutcomes.single().isFailure)
        assertEquals(1, secondOutcomes.size)
        assertTrue(secondOutcomes.single().isSuccess)
        assertEquals(CapacitorVpnLifecycle.CONNECTED, coordinator.status().lifecycle)
    }

    @Test
    fun `disconnect invalidates in-flight connect and resolves each call once`() {
        val dependencies = FakeDependencies()
        val coordinator = coordinator(dependencies)
        val connectOutcomes = mutableListOf<Result<CapacitorVpnStatus>>()
        val disconnectOutcomes = mutableListOf<Result<CapacitorVpnStatus>>()
        val published = mutableListOf<CapacitorVpnLifecycle>()
        coordinator.statusListener = { published += it.lifecycle }
        dependencies.onConnect = {
            coordinator.disconnect { disconnectOutcomes += it }
        }

        coordinator.connect { connectOutcomes += it }

        assertEquals(1, connectOutcomes.size)
        assertTrue(connectOutcomes.single().isFailure)
        assertEquals(1, disconnectOutcomes.size)
        assertTrue(disconnectOutcomes.single().isSuccess)
        assertEquals(CapacitorVpnLifecycle.DISCONNECTED, coordinator.status().lifecycle)
        assertFalse(published.dropWhile { it != CapacitorVpnLifecycle.DISCONNECTING }.contains(CapacitorVpnLifecycle.CONNECTED))
        assertFalse(dependencies.events.contains("startTunnel"))
    }

    @Test
    fun `consent launcher exception rejects and clears permission-only request`() {
        val dependencies = FakeDependencies(hasConsent = false, permissionFailure = IllegalStateException("launcher unavailable"))
        val coordinator = coordinator(dependencies)
        val outcomes = mutableListOf<Result<Boolean>>()

        coordinator.requestVpnPermission { outcomes += it }

        assertEquals(1, outcomes.size)
        assertTrue(outcomes.single().isFailure)
        assertEquals(CapacitorVpnLifecycle.ERROR, coordinator.status().lifecycle)

        dependencies.permissionFailure = null
        coordinator.requestVpnPermission { outcomes += it }

        assertEquals(listOf("permission", "permission"), dependencies.events)
        assertEquals(1, outcomes.size)
        assertEquals(CapacitorVpnLifecycle.PERMISSION_REQUIRED, coordinator.status().lifecycle)
    }

    @Test
    fun `granted permission-only request publishes disconnected before plugin status resolution`() {
        val dependencies = FakeDependencies(hasConsent = false, tunnelActive = false)
        val coordinator = coordinator(dependencies)
        val published = mutableListOf<CapacitorVpnLifecycle>()
        var callbackLifecycle: CapacitorVpnLifecycle? = null
        coordinator.statusListener = { published += it.lifecycle }

        coordinator.requestVpnPermission { result ->
            assertTrue(result.isSuccess)
            // WtTransportPlugin resolves statusJson synchronously from this
            // same coordinator state after the callback is invoked.
            callbackLifecycle = coordinator.status().lifecycle
        }
        dependencies.hasConsent = true
        coordinator.onVpnPermissionResult(dependencies.permissionTokens.single(), granted = true)

        assertEquals(CapacitorVpnLifecycle.DISCONNECTED, callbackLifecycle)
        assertEquals(
            listOf(CapacitorVpnLifecycle.PERMISSION_REQUIRED, CapacitorVpnLifecycle.DISCONNECTED),
            published,
        )
        assertEquals(CapacitorVpnLifecycle.DISCONNECTED, coordinator.status().lifecycle)
    }

    @Test
    fun `granted permission-only request restores a currently active tunnel status`() {
        val dependencies = FakeDependencies(hasConsent = false, tunnelActive = true, transportConnected = true)
        val coordinator = coordinator(dependencies)
        var callbackStatus: CapacitorVpnStatus? = null

        coordinator.requestVpnPermission { result ->
            assertTrue(result.isSuccess)
            callbackStatus = coordinator.status()
        }
        dependencies.hasConsent = true
        coordinator.onVpnPermissionResult(dependencies.permissionTokens.single(), granted = true)

        assertEquals(CapacitorVpnLifecycle.CONNECTED, callbackStatus?.lifecycle)
        assertTrue(callbackStatus?.transportConnected == true)
        assertTrue(callbackStatus?.systemVpnConnected == true)
    }

    @Test
    fun `connect is rejected while permission-only request is pending`() {
        val dependencies = FakeDependencies(hasConsent = false)
        val coordinator = coordinator(dependencies)
        var permissionOutcome: Result<Boolean>? = null
        var connectOutcome: Result<CapacitorVpnStatus>? = null

        coordinator.requestVpnPermission { permissionOutcome = it }
        coordinator.connect { connectOutcome = it }

        assertNull(permissionOutcome)
        assertNotNull(connectOutcome)
        assertTrue(connectOutcome!!.isFailure)
        assertEquals(listOf("permission"), dependencies.events)
    }

    @Test
    fun `connect consent launcher exception rejects and clears pending call`() {
        val dependencies = FakeDependencies(hasConsent = false, permissionFailure = IllegalStateException("launcher unavailable"))
        val coordinator = coordinator(dependencies)
        val outcomes = mutableListOf<Result<CapacitorVpnStatus>>()

        coordinator.connect { outcomes += it }

        assertEquals(1, outcomes.size)
        assertTrue(outcomes.single().isFailure)
        assertEquals(CapacitorVpnLifecycle.ERROR, coordinator.status().lifecycle)

        dependencies.permissionFailure = null
        coordinator.connect { outcomes += it }

        assertEquals(listOf("permission", "permission"), dependencies.events)
        assertEquals(1, outcomes.size)
        assertEquals(CapacitorVpnLifecycle.PERMISSION_REQUIRED, coordinator.status().lifecycle)
    }

    @Test
    fun `Go connect failure rolls back transport and never starts VPN`() {
        val dependencies = FakeDependencies(connectFailure = IllegalStateException("go connect failed"))
        val coordinator = coordinator(dependencies)
        var outcome: Result<CapacitorVpnStatus>? = null

        coordinator.connect { outcome = it }

        assertTrue(outcome!!.isFailure)
        assertEquals(CapacitorVpnLifecycle.ERROR, coordinator.status().lifecycle)
        assertEquals(listOf("connect", "disconnect"), dependencies.events)
        assertFalse(dependencies.tunnelStartAttempted)
    }

    @Test
    fun `execute connect rejects work invalidated by a newer operation generation`() {
        val dependencies = FakeDependencies(tunnelActive = false)
        val coordinator = coordinator(dependencies)
        dependencies.onConnect = { coordinator.reconcileAfterRestart() }
        var outcome: Result<CapacitorVpnStatus>? = null

        coordinator.connect { outcome = it }

        assertTrue(outcome!!.isFailure)
        assertFalse(dependencies.events.contains("startTunnel"))
        assertEquals(CapacitorVpnLifecycle.DISCONNECTED, coordinator.status().lifecycle)
    }

    @Test
    fun `VPN service failure rolls back TUN before transport`() {
        val dependencies = FakeDependencies(startTunnelResult = false)
        val coordinator = coordinator(dependencies)
        var outcome: Result<CapacitorVpnStatus>? = null

        coordinator.connect { outcome = it }

        assertTrue(outcome!!.isFailure)
        assertEquals(listOf("connect", "probe", "startTunnel", "stopTunnel", "disconnect"), dependencies.events)
        assertEquals(CapacitorVpnLifecycle.ERROR, coordinator.status().lifecycle)
    }

    @Test
    fun `connected is published only after authoritative TUNNEL_ACTIVE`() {
        val dependencies = FakeDependencies(tunnelActive = false)
        val coordinator = coordinator(dependencies)
        var outcome: Result<CapacitorVpnStatus>? = null

        coordinator.connect { outcome = it }

        assertTrue(outcome!!.isFailure)
        assertEquals(CapacitorVpnLifecycle.ERROR, coordinator.status().lifecycle)
        assertEquals(listOf("connect", "probe", "startTunnel", "awaitTunnelActive", "stopTunnel", "disconnect"), dependencies.events)
    }

    @Test
    fun `service startup failure rejects in-flight connect without later connected state`() {
        val dependencies = FakeDependencies()
        val coordinator = coordinator(dependencies)
        val outcomes = mutableListOf<Result<CapacitorVpnStatus>>()
        dependencies.onStartTunnel = { coordinator.onTunnelStatus(VpnStatus.CALL_FAILED) }

        coordinator.connect { outcomes += it }

        assertEquals(1, outcomes.size)
        assertTrue(outcomes.single().isFailure)
        assertEquals(CapacitorVpnLifecycle.ERROR, coordinator.status().lifecycle)
        assertFalse(coordinator.status().systemVpnConnected)
    }

    @Test
    fun `mode and split settings persist and are applied to tunnel start`() {
        val dependencies = FakeDependencies()
        val coordinator = coordinator(dependencies)

        coordinator.setMode(CapacitorVpnMode.TUNNEL)
        val settings = coordinator.setSplitRouting(
            SplitTunnelingMode.ONLY,
            setOf("org.telegram.messenger", "com.android.chrome"),
        )

        assertEquals(CapacitorVpnMode.TUNNEL, dependencies.settings.mode)
        assertEquals(SplitTunnelingMode.ONLY, settings.mode)
        assertEquals(setOf("org.telegram.messenger", "com.android.chrome"), dependencies.settings.packages)

        var outcome: Result<CapacitorVpnStatus>? = null
        coordinator.connect { outcome = it }

        assertTrue(outcome!!.isSuccess)
        assertEquals(settings, dependencies.startedWith)
    }

    @Test
    fun `invalid split package is rejected before settings are persisted`() {
        val dependencies = FakeDependencies()
        val coordinator = coordinator(dependencies)

        val failure = runCatching {
            coordinator.setSplitRouting(SplitTunnelingMode.ONLY, setOf("not a package"))
        }.exceptionOrNull()

        assertNotNull(failure)
        assertEquals(SplitTunnelingMode.NONE, dependencies.settings.splitMode)
        assertTrue(dependencies.settings.packages.isEmpty())
    }

    @Test
    fun `only split mode requires at least one package`() {
        val dependencies = FakeDependencies()
        val coordinator = coordinator(dependencies)

        val failure = runCatching {
            coordinator.setSplitRouting(SplitTunnelingMode.ONLY, emptySet())
        }.exceptionOrNull()

        assertNotNull(failure)
        assertEquals(SplitTunnelingMode.NONE, dependencies.settings.splitMode)
    }

    @Test
    fun `only split mode rejects the VPN app package to prevent route recursion`() {
        val dependencies = FakeDependencies()
        val coordinator = coordinator(dependencies)

        val failure = runCatching {
            coordinator.setSplitRouting(SplitTunnelingMode.ONLY, setOf(dependencies.appPackageName()))
        }.exceptionOrNull()

        assertNotNull(failure)
        assertEquals(SplitTunnelingMode.NONE, dependencies.settings.splitMode)
    }

    @Test
    fun `unknown split mode is rejected instead of becoming full tunnel`() {
        val failure = runCatching { parseCapacitorSplitMode("unexpected") }.exceptionOrNull()

        assertNotNull(failure)
    }

    @Test
    fun `proxy mode is rejected without changing persisted tunnel mode`() {
        val dependencies = FakeDependencies()
        val coordinator = coordinator(dependencies)

        val failure = runCatching { coordinator.setMode(CapacitorVpnMode.PROXY) }.exceptionOrNull()

        assertTrue(failure is UnsupportedOperationException)
        assertEquals(CapacitorVpnMode.TUNNEL, dependencies.settings.mode)
    }

    @Test
    fun `disconnect stops TUN before transport and publishes disconnected`() {
        val dependencies = FakeDependencies()
        val coordinator = coordinator(dependencies)
        var connectOutcome: Result<CapacitorVpnStatus>? = null
        coordinator.connect { connectOutcome = it }
        assertTrue(connectOutcome!!.isSuccess)
        dependencies.events.clear()

        var disconnectOutcome: Result<CapacitorVpnStatus>? = null
        coordinator.disconnect { disconnectOutcome = it }

        assertTrue(disconnectOutcome!!.isSuccess)
        assertEquals(listOf("stopTunnel", "disconnect"), dependencies.events)
        assertEquals(CapacitorVpnLifecycle.DISCONNECTED, coordinator.status().lifecycle)
    }

    @Test
    fun `disconnect attempts transport cleanup when tunnel stop fails`() {
        val dependencies = FakeDependencies(stopTunnelFailure = IllegalStateException("TUN stop failed"))
        val coordinator = coordinator(dependencies)
        var connectOutcome: Result<CapacitorVpnStatus>? = null
        coordinator.connect { connectOutcome = it }
        assertTrue(connectOutcome!!.isSuccess)
        dependencies.events.clear()

        var disconnectOutcome: Result<CapacitorVpnStatus>? = null
        coordinator.disconnect { disconnectOutcome = it }

        assertTrue(disconnectOutcome!!.isFailure)
        assertEquals(listOf("stopTunnel", "disconnect"), dependencies.events)
        assertFalse(dependencies.transportConnected)
        assertEquals(CapacitorVpnLifecycle.ERROR, coordinator.status().lifecycle)
    }

    @Test
    fun `restart reconciliation restores active service or cleans stale tunnel`() {
        val dependencies = FakeDependencies(transportConnected = true, tunnelActive = true)
        val coordinator = coordinator(dependencies)

        var activeOutcome: Result<CapacitorVpnStatus>? = null
        coordinator.reconcileAfterRestart { activeOutcome = it }
        val active = activeOutcome!!.getOrThrow()

        assertEquals(CapacitorVpnLifecycle.CONNECTED, active.lifecycle)
        assertTrue(active.systemVpnConnected)

        dependencies.transportConnected = false
        dependencies.events.clear()
        var staleOutcome: Result<CapacitorVpnStatus>? = null
        coordinator.reconcileAfterRestart { staleOutcome = it }
        val stale = staleOutcome!!.getOrThrow()

        assertEquals(CapacitorVpnLifecycle.DISCONNECTED, stale.lifecycle)
        assertEquals(listOf("stopTunnel", "disconnect"), dependencies.events)
    }

    @Test
    fun `restart reconciliation rejects stale persisted proxy transport`() {
        val dependencies = FakeDependencies(transportConnected = true, tunnelActive = false)
        dependencies.settings.mode = CapacitorVpnMode.PROXY
        val coordinator = coordinator(dependencies)
        var outcome: Result<CapacitorVpnStatus>? = null

        coordinator.reconcileAfterRestart { outcome = it }
        val status = outcome!!.getOrThrow()

        assertEquals(CapacitorVpnLifecycle.ERROR, status.lifecycle)
        assertFalse(status.transportConnected)
        assertFalse(status.systemVpnConnected)
        assertEquals(listOf("disconnect"), dependencies.events)
    }

    @Test
    fun `restart reconciliation stops a service that is still starting`() {
        val dependencies = FakeDependencies(
            transportConnected = true,
            tunnelActive = false,
            tunnelInProgress = true,
        )
        val coordinator = coordinator(dependencies)
        var outcome: Result<CapacitorVpnStatus>? = null

        coordinator.reconcileAfterRestart { outcome = it }

        assertTrue(outcome!!.isSuccess)
        assertEquals(listOf("stopTunnel", "disconnect"), dependencies.events)
        assertEquals(CapacitorVpnLifecycle.DISCONNECTED, coordinator.status().lifecycle)
    }

    @Test
    fun `tunnel stop timeout fails instead of reporting disconnected`() {
        val error = runCatching {
            awaitTunnelStopped(
                timeoutMs = 1,
                isStopped = { false },
                sleep = {},
            )
        }.exceptionOrNull()

        assertTrue(error is IllegalStateException)
        assertTrue(error?.message.orEmpty().contains("timed out"))
    }

    @Test
    fun `completed service cleanup still propagates bridge stop timeout`() {
        val error = runCatching {
            requireTunnelStopSucceeded(TimeoutException("tun2socks stop timed out"))
        }.exceptionOrNull()

        assertTrue(error is IllegalStateException)
        assertTrue(error?.cause is TimeoutException)
    }

    @Test
    fun `status listener runs outside coordinator lock and cannot break state publication`() {
        val dependencies = FakeDependencies()
        val coordinator = coordinator(dependencies)
        var callbacks = 0
        coordinator.statusListener = {
            callbacks += 1
            assertFalse(coordinator.isStateLockHeldByCurrentThread())
            throw IllegalStateException("listener failure")
        }

        coordinator.setMode(CapacitorVpnMode.TUNNEL)

        assertEquals(CapacitorVpnMode.TUNNEL, coordinator.status().mode)
        assertEquals(1, callbacks)
    }

    @Test
    fun `restart reconciliation is scheduled on the coordinator executor`() {
        val dependencies = FakeDependencies(transportConnected = true, tunnelActive = true)
        val executor = QueuedExecutor()
        val coordinator = CapacitorVpnCoordinator(dependencies, executor, 25)
        var outcome: Result<CapacitorVpnStatus>? = null

        coordinator.reconcileAfterRestart { outcome = it }

        assertNull(outcome)
        executor.runNext()
        assertTrue(outcome!!.isSuccess)
    }

    @Test
    fun `activity detach invalidates queued restart reconciliation`() {
        val dependencies = FakeDependencies(transportConnected = true, tunnelActive = true)
        val executor = QueuedExecutor()
        val coordinator = CapacitorVpnCoordinator(dependencies, executor, 25)
        var outcome: Result<CapacitorVpnStatus>? = null
        coordinator.reconcileAfterRestart { outcome = it }

        coordinator.detachUi()
        executor.runNext()

        assertTrue(outcome!!.isFailure)
        assertEquals(CapacitorVpnLifecycle.DISCONNECTED, coordinator.status().lifecycle)
    }

    @Test
    fun `status callback ownership survives old activity detach`() {
        val oldOwner = Any()
        val newOwner = Any()
        var received: VpnStatus? = null
        TunnelServiceState.attachVpnStatusCallback(oldOwner) {}
        TunnelServiceState.attachVpnStatusCallback(newOwner) { received = it }

        TunnelServiceState.detachVpnStatusCallback(oldOwner)
        TunnelServiceState.publishVpnStatus(VpnStatus.TUNNEL_ACTIVE)

        assertEquals(VpnStatus.TUNNEL_ACTIVE, received)
        TunnelServiceState.detachVpnStatusCallback(newOwner)
        assertEquals(0, TunnelServiceState.vpnStatusSubscriberCount())
    }

    private fun coordinator(dependencies: FakeDependencies): CapacitorVpnCoordinator =
        CapacitorVpnCoordinator(
            dependencies = dependencies,
            executor = Executor { it.run() },
            tunnelReadyTimeoutMs = 25,
        )

    private class FakeSettings(
        override var mode: CapacitorVpnMode = CapacitorVpnMode.TUNNEL,
        override var splitMode: SplitTunnelingMode = SplitTunnelingMode.NONE,
        override var packages: Set<String> = emptySet(),
    ) : CapacitorVpnSettings

    private class FakeDependencies(
        var hasConsent: Boolean = true,
        var permissionFailure: Throwable? = null,
        private val connectFailure: Throwable? = null,
        private val startTunnelResult: Boolean = true,
        private val tunnelActive: Boolean = true,
        private val tunnelInProgress: Boolean = false,
        var transportConnected: Boolean = false,
        private val stopTunnelFailure: Throwable? = null,
        private val runtimeEndpoint: LoopbackSocksEndpoint = LoopbackSocksEndpoint.runtime("127.0.0.1", 1085),
        private val transportStatus: JSONObject = JSONObject(
            """{"active_node_id":"node-a","system_vpn_profile":{"selected_node_id":"node-a"}}""",
        ),
        private val selectedNodeId: String = "node-a",
    ) : CapacitorVpnDependencies {
        val events = mutableListOf<String>()
        val settings = FakeSettings()
        var permissionRequested = false
        var tunnelStartAttempted = false
        var startedWith: CapacitorVpnSplitSettings? = null
        var onConnect: (() -> Unit)? = null
        var onStartTunnel: (() -> Unit)? = null
        val permissionTokens = mutableListOf<Long>()
        var probedEndpoint: LoopbackSocksEndpoint? = null
        var tunnelEndpoint: LoopbackSocksEndpoint? = null

        override fun hasVpnConsent(): Boolean = hasConsent

        override fun requestVpnConsent(operationToken: Long) {
            events += "permission"
            permissionRequested = true
            permissionTokens += operationToken
            permissionFailure?.let { throw it }
        }

        override fun connectTransport(nodeId: String?, configJson: String?): CapacitorTransportConnection {
            events += "connect"
            connectFailure?.let { throw it }
            transportConnected = true
            onConnect?.invoke()
            return CapacitorTransportConnection(transportStatus, runtimeEndpoint, selectedNodeId)
        }

        override fun verifySocksPayload(endpoint: LoopbackSocksEndpoint): Boolean {
            events += "probe"
            probedEndpoint = endpoint
            return true
        }

        override fun startTunnel(settings: CapacitorVpnSplitSettings, endpoint: LoopbackSocksEndpoint): Boolean {
            events += "startTunnel"
            tunnelStartAttempted = true
            startedWith = settings
            tunnelEndpoint = endpoint
            onStartTunnel?.invoke()
            return startTunnelResult
        }

        override fun awaitTunnelActive(timeoutMs: Long): Boolean {
            events += "awaitTunnelActive"
            return tunnelActive
        }

        override fun isTunnelActive(): Boolean = tunnelActive

        override fun isTunnelStartedOrInProgress(): Boolean = tunnelActive || tunnelInProgress

        override fun isTransportConnected(): Boolean = transportConnected

        override fun stopTunnel() {
            events += "stopTunnel"
            stopTunnelFailure?.let { throw it }
        }

        override fun disconnectTransport() {
            events += "disconnect"
            transportConnected = false
        }

        override fun appPackageName(): String = "bypass.whitelist"

        override fun settings(): CapacitorVpnSettings = settings
    }

    private class QueuedExecutor : Executor {
        private val tasks = ArrayDeque<Runnable>()

        override fun execute(command: Runnable) {
            tasks.addLast(command)
        }

        fun runNext() {
            tasks.removeFirst().run()
        }
    }
}
