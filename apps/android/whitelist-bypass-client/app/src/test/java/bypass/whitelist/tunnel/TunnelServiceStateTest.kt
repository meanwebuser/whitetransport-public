package bypass.whitelist.tunnel

import org.junit.Assert.assertEquals
import org.junit.Test

class TunnelServiceStateTest {
    @Test
    fun `Capacitor legacy Capacitor handover keeps every live subscriber`() {
        val firstCapacitor = Any()
        val legacy = Any()
        val secondCapacitor = Any()
        val firstEvents = mutableListOf<VpnStatus>()
        val legacyEvents = mutableListOf<VpnStatus>()
        val secondEvents = mutableListOf<VpnStatus>()

        try {
            TunnelServiceState.attachVpnStatusCallback(firstCapacitor, firstEvents::add)
            TunnelServiceState.publishVpnStatus(VpnStatus.STARTING)
            TunnelServiceState.attachVpnStatusCallback(legacy, legacyEvents::add)
            TunnelServiceState.publishVpnStatus(VpnStatus.TUNNEL_ACTIVE)
            TunnelServiceState.detachVpnStatusCallback(firstCapacitor)
            TunnelServiceState.attachVpnStatusCallback(secondCapacitor, secondEvents::add)
            TunnelServiceState.detachVpnStatusCallback(legacy)
            TunnelServiceState.publishVpnStatus(VpnStatus.STOPPING)

            assertEquals(listOf(VpnStatus.STARTING, VpnStatus.TUNNEL_ACTIVE), firstEvents)
            assertEquals(listOf(VpnStatus.TUNNEL_ACTIVE), legacyEvents)
            assertEquals(listOf(VpnStatus.STOPPING), secondEvents)
            assertEquals(1, TunnelServiceState.vpnStatusSubscriberCount())
        } finally {
            TunnelServiceState.detachVpnStatusCallback(firstCapacitor)
            TunnelServiceState.detachVpnStatusCallback(legacy)
            TunnelServiceState.detachVpnStatusCallback(secondCapacitor)
        }
    }

    @Test
    fun `throwing subscriber does not suppress delivery to other owners`() {
        val received = mutableListOf<VpnStatus>()
        val failures = mutableListOf<Throwable>()
        val callbacks: List<(VpnStatus) -> Unit> = listOf(
            { _: VpnStatus -> error("callback failed") },
            { status: VpnStatus -> received.add(status); Unit },
        )

        publishVpnStatusTo(
            callbacks = callbacks,
            status = VpnStatus.CALL_FAILED,
            onFailure = failures::add,
        )

        assertEquals(1, failures.size)
        assertEquals(listOf(VpnStatus.CALL_FAILED), received)
    }
}
