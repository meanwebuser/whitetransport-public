package bypass.whitelist.planner

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.lang.reflect.InvocationTargetException

class GoRuntimeControllerStopTest {
    @Test
    fun `confirmed node requires active status and hashed profile selection`() {
        assertEquals("node-a", requireConfirmedSelectedNode("node-a", "node-a", "node-a"))
        assertTrue(runCatching { requireConfirmedSelectedNode("node-a", "node-b", "node-b") }.isFailure)
        assertTrue(runCatching { requireConfirmedSelectedNode("node-a", "node-a", "node-b") }.isFailure)
        assertTrue(runCatching { requireConfirmedSelectedNode("node-a", "node-a", "") }.isFailure)
    }

    @Test
    fun `transport confirmation accepts active node before asynchronous VPN profile refresh`() {
        assertEquals("node-a", requireConfirmedTransportNode("node-a", "node-a"))
    }

    @Test
    fun `native stop reflection failure is propagated to service`() {
        val expected = IllegalStateException("native stop failed")

        val error = runCatching {
            invokeRequiredTun2SocksStop(available = true) { throw expected }
        }.exceptionOrNull()

        assertEquals(expected, error)
    }

    @Test
    fun `native reflection wrapper exposes the underlying runtime failure`() {
        val expected = IllegalStateException("runtime connect failed")

        assertEquals(expected, unwrapInvocationTarget(InvocationTargetException(expected)))
    }

    @Test
    fun `unavailable native runtime is a stop failure`() {
        val error = runCatching {
            invokeRequiredTun2SocksStop(available = false) {}
        }.exceptionOrNull()

        assertTrue(error is IllegalStateException)
    }
}
