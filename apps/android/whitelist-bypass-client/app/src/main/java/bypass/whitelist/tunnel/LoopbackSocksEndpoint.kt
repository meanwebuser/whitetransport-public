package bypass.whitelist.tunnel

import java.util.concurrent.CancellationException
import java.util.concurrent.TimeUnit
import java.util.concurrent.TimeoutException

/** Validated local SOCKS listener passed between the Go session, probe, and bridge. */
@ConsistentCopyVisibility
data class LoopbackSocksEndpoint private constructor(
    val host: String,
    val port: Int,
    val username: String,
    val password: String,
) {
    init {
        require(host == IPV4_LOOPBACK) { "runtime SOCKS host must be $IPV4_LOOPBACK" }
        require(port in 1..65_535) { "runtime SOCKS port must be between 1 and 65535" }
    }

    companion object {
        const val IPV4_LOOPBACK = "127.0.0.1"

        /** Go's embedded SOCKS server has no username/password authentication. */
        fun runtime(host: String, port: Int): LoopbackSocksEndpoint =
            LoopbackSocksEndpoint(host.trim(), port, "", "")

        /** Preserve explicit authentication for the deprecated legacy proxy path only. */
        fun legacy(host: String, port: Int, username: String, password: String): LoopbackSocksEndpoint =
            LoopbackSocksEndpoint(host.trim(), port, username, password)

        fun runtimeFromListenAddress(listenAddress: String): LoopbackSocksEndpoint {
            val normalized = listenAddress.trim()
            val separator = normalized.lastIndexOf(':')
            require(separator > 0 && separator < normalized.lastIndex) {
                "runtime SOCKS listen address must be host:port"
            }
            val host = normalized.substring(0, separator)
            val port = normalized.substring(separator + 1).toIntOrNull()
                ?: throw IllegalArgumentException("runtime SOCKS port must be numeric")
            return runtime(host, port)
        }

        fun runtimeFromServicePayload(payload: RuntimeSocksEndpointPayload): LoopbackSocksEndpoint =
            runtime(payload.host, payload.port)
    }
}

/** Credential-free value serialized across the coordinator/service Intent boundary. */
data class RuntimeSocksEndpointPayload(val host: String, val port: Int)

fun LoopbackSocksEndpoint.toServicePayload(): RuntimeSocksEndpointPayload =
    RuntimeSocksEndpointPayload(host = host, port = port)

/** Explicit service start modes; a null or untyped restart is rejected. */
internal sealed interface TunnelStartRequest {
    data class Runtime(val endpoint: LoopbackSocksEndpoint) : TunnelStartRequest
    data object Legacy : TunnelStartRequest
    data class Rejected(val reason: String) : TunnelStartRequest
}

/** Resolve an Intent-like payload without ever falling back from runtime to legacy auth. */
internal fun resolveTunnelStartRequest(
    runtimeHostPresent: Boolean,
    runtimeHost: String?,
    runtimePortPresent: Boolean,
    runtimePort: Int,
    explicitLegacy: Boolean,
): TunnelStartRequest {
    if (runtimeHostPresent || runtimePortPresent) {
        if (!runtimeHostPresent || !runtimePortPresent) {
            return TunnelStartRequest.Rejected("runtime SOCKS host and port must be provided together")
        }
        return runCatching {
            TunnelStartRequest.Runtime(
                LoopbackSocksEndpoint.runtimeFromServicePayload(
                    RuntimeSocksEndpointPayload(runtimeHost.orEmpty(), runtimePort),
                ),
            )
        }.getOrElse { TunnelStartRequest.Rejected(it.message ?: "invalid runtime SOCKS endpoint") }
    }
    return if (explicitLegacy) TunnelStartRequest.Legacy else {
        TunnelStartRequest.Rejected("missing typed runtime SOCKS endpoint")
    }
}

/** Exact arguments forwarded to the Go tun2socks bridge. */
data class Tun2SocksBridgeArgs(
    val fd: Long,
    val mtu: Long,
    val socksPort: Long,
    val username: String,
    val password: String,
)

/** Invoke the bridge through a capturable seam so JVM tests protect real argument propagation. */
internal fun invokeTun2SocksBridge(
    fd: Int,
    mtu: Int,
    endpoint: LoopbackSocksEndpoint,
    start: (Tun2SocksBridgeArgs) -> Throwable?,
): Throwable? {
    val args = Tun2SocksBridgeArgs(
        fd = fd.toLong(),
        mtu = mtu.toLong(),
        socksPort = endpoint.port.toLong(),
        username = endpoint.username,
        password = endpoint.password,
    )
    return start(args)
}

/** Transfer descriptor ownership while always closing the original wrapper. */
internal fun detachOwnedTunDescriptor(detach: () -> Int, closeOwned: () -> Unit): Int = try {
    detach()
} finally {
    closeOwned()
}

/**
 * Tracks the raw descriptor transferred out of ParcelFileDescriptor. Kotlin
 * closes it only before a successful native handoff; native Stop owns it after.
 */
internal class DetachedTunFdOwnership(
    val fd: Int,
    private val closeRawFd: (Int) -> Unit,
) {
    private var state = State.KOTLIN_OWNED

    @Synchronized
    fun beginNativeHandoff() {
        check(state == State.KOTLIN_OWNED) { "TUN fd is no longer available for native handoff" }
        state = State.HANDOFF_IN_PROGRESS
    }

    fun completeNativeHandoff(error: Throwable?) {
        val close = synchronized(this) {
            check(state == State.HANDOFF_IN_PROGRESS) { "TUN fd handoff is not in progress" }
            if (error == null) {
                state = State.NATIVE_OWNED
                false
            } else {
                state = State.CLOSED
                true
            }
        }
        if (close) closeRawFd(fd)
    }

    fun closeBeforeNativeHandoff() {
        val close = synchronized(this) {
            if (state != State.KOTLIN_OWNED) return@synchronized false
            state = State.CLOSED
            true
        }
        if (close) closeRawFd(fd)
    }

    @Synchronized
    fun isNativeOwned(): Boolean = state == State.NATIVE_OWNED

    /** Record that native Stop released its descriptor after a successful handoff. */
    fun completeNativeStop(error: Throwable?) {
        synchronized(this) {
            check(state == State.NATIVE_OWNED) { "TUN fd is not owned by native runtime" }
            if (error == null) state = State.CLOSED
        }
    }

    @Synchronized
    fun isClosed(): Boolean = state == State.CLOSED

    private enum class State {
        KOTLIN_OWNED,
        HANDOFF_IN_PROGRESS,
        NATIVE_OWNED,
        CLOSED,
    }
}

/** Execute the synchronous native initialization boundary and settle FD ownership. */
internal fun runTun2SocksHandoff(
    ownership: DetachedTunFdOwnership,
    start: () -> Throwable?,
): Throwable? {
    val beginError = runCatching(ownership::beginNativeHandoff).exceptionOrNull()
    if (beginError != null) return beginError
    val error = runCatching(start).fold(onSuccess = { it }, onFailure = { it })
    ownership.completeNativeHandoff(error)
    return error
}

/**
 * Signals that native Stop is still running after the bounded handoff wait.
 * The completion callback is invoked by the native worker exactly once when
 * the underlying Stop call really returns.
 */
internal class Tun2SocksStopPending(
    message: String,
) : TimeoutException(message) {
    private var completed = false
    private var completionError: Throwable? = null
    private var completion: ((Throwable?) -> Unit)? = null

    fun onComplete(callback: (Throwable?) -> Unit) {
        val error = synchronized(this) {
            if (!completed) {
                completion = callback
                return
            }
            completionError
        }
        callback(error)
    }

    fun complete(error: Throwable?) {
        val callback = synchronized(this) {
            if (completed) return
            completed = true
            completionError = error
            completion.also { completion = null }
        }
        callback?.invoke(error)
    }
}

/** Reservation held before establishing a new Android VPN interface. */
internal class Tun2SocksStartReservation internal constructor()

/**
 * Process-global ownership barrier for the native tun2socks descriptor.
 * Android may destroy and recreate the service while native Stop is still
 * unwinding, so the lifecycle and descriptor must outlive any service object.
 */
internal object Tun2SocksProcessBarrier {
    private data class Held(
        val lifecycle: Tun2SocksHandoffLifecycle,
        val ownership: DetachedTunFdOwnership,
    )

    private var reservation: Tun2SocksStartReservation? = null
    private var held: Held? = null

    @Synchronized
    fun reserveStart(): Tun2SocksStartReservation? {
        if (reservation != null || held != null) return null
        return Tun2SocksStartReservation().also { reservation = it }
    }

    @Synchronized
    fun attach(
        startReservation: Tun2SocksStartReservation,
        lifecycle: Tun2SocksHandoffLifecycle,
        ownership: DetachedTunFdOwnership,
    ) {
        check(reservation === startReservation) { "tun2socks start reservation is not active" }
        held = Held(lifecycle, ownership)
        reservation = null
    }

    @Synchronized
    fun releaseReservation(startReservation: Tun2SocksStartReservation) {
        if (reservation === startReservation) reservation = null
    }

    /** Keep a failed stop fail-closed; only observed native success clears it. */
    @Synchronized
    fun observeStop(lifecycle: Tun2SocksHandoffLifecycle, error: Throwable?) {
        if (held?.lifecycle !== lifecycle) return
        if (error == null) held = null
    }

    @Synchronized
    fun abort(lifecycle: Tun2SocksHandoffLifecycle) {
        if (held?.lifecycle === lifecycle) held = null
    }

    @Synchronized
    fun isHeld(): Boolean = reservation != null || held != null

    /** Test-only isolation; production code never clears a held native stop. */
    @Synchronized
    fun resetForTests() {
        reservation = null
        held = null
    }
}

/**
 * Serializes native Start with service Stop without blocking Android's main
 * thread. A timed-out stop request remains registered, so a late successful
 * Start must still execute exactly one native Stop before returning.
 */
internal class Tun2SocksHandoffLifecycle(
    private val ownership: DetachedTunFdOwnership,
    private val onStopObserved: (Tun2SocksHandoffLifecycle, Throwable?) -> Unit = { _, _ -> },
) {
    private val monitor = Object()
    private var phase = Phase.PENDING
    private var stopRequested = false
    private var stopClaimed = false
    private var stopCompleted = false
    private var startError: Throwable? = null
    private var stopError: Throwable? = null
    private var pendingStop: Tun2SocksStopPending? = null

    fun runStart(start: () -> Throwable?, stop: () -> Throwable?): Throwable? {
        val mayStart = synchronized(monitor) {
            if (phase != Phase.PENDING) {
                false
            } else {
                phase = Phase.STARTING
                true
            }
        }
        if (!mayStart) return CancellationException("tun2socks start cancelled before native handoff")

        val error = runTun2SocksHandoff(ownership, start)
        val invokeDeferredStop = synchronized(monitor) {
            startError = error
            when {
                error != null -> {
                    phase = Phase.START_FAILED
                    stopCompleted = true
                    monitor.notifyAll()
                    false
                }
                stopRequested -> {
                    phase = Phase.STOPPING
                    claimStopLocked()
                }
                else -> {
                    phase = Phase.ACTIVE
                    monitor.notifyAll()
                    false
                }
            }
        }
        if (invokeDeferredStop) completeStop(stop)
        return error
    }

    fun cancelBeforeNativeStart() {
        val closeKotlinOwnedFd = synchronized(monitor) {
            if (phase != Phase.PENDING) {
                false
            } else {
                stopRequested = true
                stopCompleted = true
                phase = Phase.STOPPED
                monitor.notifyAll()
                true
            }
        }
        if (closeKotlinOwnedFd) ownership.closeBeforeNativeHandoff()
    }

    fun requestStopAndAwait(stop: () -> Throwable?, timeoutMillis: Long): Throwable? {
        require(timeoutMillis >= 0) { "tun2socks stop timeout must not be negative" }
        var closeKotlinOwnedFd = false
        val invokeStop = synchronized(monitor) {
            stopRequested = true
            when (phase) {
                Phase.PENDING -> {
                    phase = Phase.STOPPED
                    stopCompleted = true
                    closeKotlinOwnedFd = true
                    monitor.notifyAll()
                    false
                }
                Phase.ACTIVE -> {
                    phase = Phase.STOPPING
                    claimStopLocked()
                }
                Phase.STARTING, Phase.START_FAILED, Phase.STOPPING, Phase.STOPPED -> false
            }
        }
        if (closeKotlinOwnedFd) ownership.closeBeforeNativeHandoff()
        if (invokeStop) completeStop(stop)
        return awaitStopCompletion(timeoutMillis)
    }

    fun isStopping(): Boolean = synchronized(monitor) { phase == Phase.STOPPING }

    fun isStopped(): Boolean = synchronized(monitor) { phase == Phase.STOPPED }

    private fun claimStopLocked(): Boolean {
        if (stopClaimed) return false
        stopClaimed = true
        return true
    }

    private fun completeStop(stop: () -> Throwable?) {
        val error = runCatching(stop).fold(onSuccess = { it }, onFailure = { it })
        if (error is Tun2SocksStopPending) {
            synchronized(monitor) {
                pendingStop = error
                stopError = null
                stopCompleted = false
                phase = Phase.STOPPING
                monitor.notifyAll()
            }
            error.onComplete(::finishStop)
            return
        }
        finishStop(error)
    }

    private fun finishStop(error: Throwable?) {
        ownership.completeNativeStop(error)
        synchronized(monitor) {
            pendingStop = null
            stopError = error
            stopCompleted = true
            phase = Phase.STOPPED
            monitor.notifyAll()
        }
        onStopObserved(this, error)
    }

    private fun awaitStopCompletion(timeoutMillis: Long): Throwable? {
        val deadlineNanos = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(timeoutMillis)
        synchronized(monitor) {
            while (!stopCompleted) {
                val remainingNanos = deadlineNanos - System.nanoTime()
                if (remainingNanos <= 0) {
                    return pendingStop
                        ?: TimeoutException("tun2socks handoff stop timed out after ${timeoutMillis}ms")
                }
                val waitMillis = maxOf(1, TimeUnit.NANOSECONDS.toMillis(remainingNanos))
                try {
                    monitor.wait(waitMillis)
                } catch (error: InterruptedException) {
                    Thread.currentThread().interrupt()
                    return error
                }
            }
            return startError ?: stopError
        }
    }

    private enum class Phase {
        PENDING,
        STARTING,
        ACTIVE,
        START_FAILED,
        STOPPING,
        STOPPED,
    }
}

/** Builder application rules that guarantee the VPN process cannot capture its own sockets. */
internal data class TunnelAppRoutingPolicy(
    val allowedPackages: Set<String> = emptySet(),
    val disallowedPackages: Set<String> = emptySet(),
)

/**
 * Product invariant: NONE/BYPASS disallow the app UID, while ONLY refuses to
 * allow-list it. This avoids recursive capture without claiming protect(fd).
 */
internal fun buildTunnelAppRoutingPolicy(
    mode: SplitTunnelingMode,
    packages: Set<String>,
    appPackageName: String,
): TunnelAppRoutingPolicy = when (mode) {
    SplitTunnelingMode.NONE -> TunnelAppRoutingPolicy(disallowedPackages = setOf(appPackageName))
    SplitTunnelingMode.BYPASS -> TunnelAppRoutingPolicy(disallowedPackages = packages + appPackageName)
    SplitTunnelingMode.ONLY -> {
        require(packages.isNotEmpty()) { "only split-routing mode requires at least one installed package" }
        require(appPackageName !in packages) { "only split-routing mode cannot include the VPN app package" }
        TunnelAppRoutingPolicy(allowedPackages = packages)
    }
}
