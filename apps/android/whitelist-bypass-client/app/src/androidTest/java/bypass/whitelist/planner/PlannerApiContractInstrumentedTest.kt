package bypass.whitelist.planner

import androidx.test.ext.junit.runners.AndroidJUnit4
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class PlannerApiContractInstrumentedTest {
    @Test
    fun parsesStripedBulkPlanFromGoPlannerJson() {
        val plan = PlannerApiClient.parsePlan(JSONObject(BULK_PLAN_JSON))

        assertEquals("bulk", plan.trafficClass)
        assertEquals("striped", plan.strategy)
        assertEquals("vk.docs.1024", plan.primary)
        assertEquals(listOf("vk.docs.256", "ok.docs.256", "ok.photos"), plan.parallel)
        assertEquals(8, plan.maxInFlightChunks)
        assertTrue(plan.placements.map { it.carrierId }.containsAll(listOf("vk.docs.1024", "vk.docs.256", "ok.docs.256")))
    }

    @Test
    fun parsesMirroredControlPlanFromGoPlannerJson() {
        val plan = PlannerApiClient.parsePlan(JSONObject(CONTROL_PLAN_JSON))

        assertEquals("control", plan.trafficClass)
        assertEquals("mirrored", plan.strategy)
        assertEquals("vk.messages", plan.primary)
        assertEquals(listOf("ok.messages"), plan.parallel)
        assertEquals(2, plan.mirrorCount)
        assertEquals(listOf("ok.messages"), plan.placements.first().mirrorCarrierIds)
    }

    @Test
    fun parsesSinglePrimaryEgressPlanFromGoPlannerJson() {
        val plan = PlannerApiClient.parsePlan(JSONObject(EGRESS_PLAN_JSON))

        assertEquals("egress", plan.trafficClass)
        assertEquals("single", plan.strategy)
        assertEquals("wbstream.vp8", plan.primary)
        assertEquals(emptyList<String>(), plan.parallel)
        assertEquals(emptyList<String>(), plan.repair)
        assertEquals(1, plan.maxInFlightChunks)
        assertEquals("wbstream.vp8", plan.placements.first().carrierId)
        assertEquals(emptyList<String>(), plan.placements.first().mirrorCarrierIds)
        assertEquals(emptyList<String>(), plan.placements.first().hedgeCarrierIds)
    }

    companion object {
        private const val BULK_PLAN_JSON = """
            {
              "traffic_class": "bulk",
              "strategy": "striped",
              "primary": {"id": "vk.docs.1024"},
              "parallel": [{"id": "vk.docs.256"}, {"id": "ok.docs.256"}, {"id": "ok.photos"}],
              "repair": [{"id": "vk.docs.256"}, {"id": "ok.docs.256"}],
              "mirror_count": 0,
              "hedge_timeout_ms": 0,
              "max_in_flight_chunks": 8,
              "placements": [
                {"index": 0, "offset": 0, "size": 196608, "carrier_id": "vk.docs.1024"},
                {"index": 1, "offset": 196608, "size": 196608, "carrier_id": "vk.docs.256"},
                {"index": 2, "offset": 393216, "size": 196608, "carrier_id": "ok.docs.256"}
              ]
            }
        """

        private const val CONTROL_PLAN_JSON = """
            {
              "traffic_class": "control",
              "strategy": "mirrored",
              "primary": {"id": "vk.messages"},
              "parallel": [{"id": "ok.messages"}],
              "repair": [],
              "mirror_count": 2,
              "hedge_timeout_ms": 750,
              "max_in_flight_chunks": 2,
              "placements": [
                {"index": 0, "offset": 0, "size": 3072, "carrier_id": "vk.messages", "mirror_carrier_ids": ["ok.messages"]}
              ]
            }
        """

        private const val EGRESS_PLAN_JSON = """
            {
              "traffic_class": "egress",
              "strategy": "single",
              "primary": {"id": "wbstream.vp8"},
              "parallel": [],
              "repair": [],
              "mirror_count": 1,
              "hedge_timeout_ms": 0,
              "max_in_flight_chunks": 1,
              "placements": [
                {"index": 0, "offset": 0, "size": 1200, "carrier_id": "wbstream.vp8"}
              ]
            }
        """
    }
}
