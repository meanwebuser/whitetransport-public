package com.meanwebuser.whitetransport.planner

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.net.InetAddress
import java.net.ServerSocket

class GoRuntimeDynamicPortTest {
    @Test
    fun `managed launch requests OS allocation when configured port is occupied without mutating provisioned config`() {
        ServerSocket(0, 1, InetAddress.getByName("127.0.0.1")).use { occupied ->
            val config = JSONObject()
                .put("socks_listen", "127.0.0.1:${occupied.localPort}")
                .put("preferred_node_id", "node-a")
                .put("token_store", JSONObject().put("tokens", org.json.JSONArray()))
                .toString()

            val launch = JSONObject(managedRuntimeConfig(config))

            assertEquals("127.0.0.1:0", launch.getString("socks_listen"))
            assertEquals("node-a", launch.getString("preferred_node_id"))
            assertTrue(launch.has("token_store"))
            assertEquals("127.0.0.1:${occupied.localPort}", JSONObject(config).getString("socks_listen"))
        }
    }

    @Test
    fun `missing bound runtime address cannot select a configured foreign proxy`() {
        val error = runCatching {
            GoRuntimeController.runtimeSocksEndpoint(JSONObject(), """{"socks_listen":"127.0.0.1:1080"}""")
        }.exceptionOrNull()

        assertTrue("An unbound runtime must reject the configured endpoint", error is IllegalArgumentException)
    }

    @Test
    fun `runtime endpoint uses actual allocated address and rejects unbound port zero`() {
        ServerSocket(0, 1, InetAddress.getByName("127.0.0.1")).use { listener ->
            val status = JSONObject().put("socks_listen", "127.0.0.1:${listener.localPort}")
            assertEquals(listener.localPort, GoRuntimeController.runtimeSocksEndpoint(status).port)
        }
        assertTrue(runCatching {
            GoRuntimeController.runtimeSocksEndpoint(JSONObject().put("socks_listen", "127.0.0.1:0"))
        }.isFailure)
    }

    @Test
    fun `unstarted runtime SOCKS metadata reports not ready instead of localhost 1080`() {
        val error = runCatching { boundRuntimeSocksEndpoint("") }.exceptionOrNull()
        assertTrue(error is IllegalArgumentException)
        assertEquals("Go runtime SOCKS listener is not ready", error?.message)
    }
}
