package bypass.whitelist.tunnel

/** Action returned by the tun2socks startup state machine for the Android service. */
internal data class TunnelBridgeAction(
    val status: VpnStatus? = null,
    val publishActive: Boolean = false,
    val stopService: Boolean = false,
)

/**
 * Models the pinned tun2socks v2.6.0 contract: native Start returns after
 * initialization, so a successful return means the bridge is active.
 */
internal class TunnelBridgeStartupContract {
    private var phase = Phase.STARTING

    @Synchronized
    fun nativeStartReturned(error: Throwable?): TunnelBridgeAction {
        if (phase != Phase.STARTING) return TunnelBridgeAction()
        return if (error == null) {
            phase = Phase.ACTIVE
            TunnelBridgeAction(status = VpnStatus.TUNNEL_ACTIVE, publishActive = true)
        } else {
            phase = Phase.FAILED
            TunnelBridgeAction(status = VpnStatus.CALL_FAILED, stopService = true)
        }
    }

    @Synchronized
    fun cancelledBeforeNativeStart(): TunnelBridgeAction {
        if (phase == Phase.FAILED || phase == Phase.STOPPED) return TunnelBridgeAction()
        phase = Phase.STOPPED
        return TunnelBridgeAction(stopService = true)
    }

    private enum class Phase {
        STARTING,
        ACTIVE,
        FAILED,
        STOPPED,
    }
}

/** Convert native teardown completion into the authoritative service status. */
internal fun vpnStatusAfterBridgeStop(error: Throwable?): VpnStatus =
    when {
        error == null -> VpnStatus.CALL_DISCONNECTED
        error is Tun2SocksStopPending -> VpnStatus.STOPPING
        else -> VpnStatus.CALL_FAILED
    }
