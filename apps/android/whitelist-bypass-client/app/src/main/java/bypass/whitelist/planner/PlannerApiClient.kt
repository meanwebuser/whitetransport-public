package bypass.whitelist.planner

import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.net.URLEncoder

data class CarrierPlan(
    val trafficClass: String,
    val strategy: String,
    val primary: String,
    val parallel: List<String>,
    val repair: List<String>,
    val mirrorCount: Int,
    val hedgeTimeoutMs: Long,
    val maxInFlightChunks: Int,
    val placements: List<ChunkPlacement>,
) {
    fun summary(): String {
        val lanes = listOf(primary).plus(parallel).filter { it.isNotBlank() }.joinToString("+")
        return "$trafficClass:$strategy:$lanes"
    }
}

data class ChunkPlacement(
    val index: Int,
    val offset: Int,
    val size: Int,
    val carrierId: String,
    val mirrorCarrierIds: List<String>,
    val hedgeCarrierIds: List<String>,
    val hedgeAfterMs: Long,
)

object PlannerApiClient {
    fun fetchPlan(
        baseUrl: String,
        trafficClass: String,
        payloadBytes: Int,
        connectTimeoutMs: Int = 3000,
        readTimeoutMs: Int = 5000,
    ): CarrierPlan {
        val normalizedBase = baseUrl.trim().trimEnd('/')
        require(normalizedBase.isNotBlank()) { "planner baseUrl is required" }
        require(payloadBytes >= 0) { "payloadBytes must be non-negative" }
        val url = "$normalizedBase/v1/plan?traffic=${encode(trafficClass)}&payload_bytes=$payloadBytes"
        val conn = URL(url).openConnection() as HttpURLConnection
        conn.instanceFollowRedirects = true
        conn.connectTimeout = connectTimeoutMs
        conn.readTimeout = readTimeoutMs
        conn.requestMethod = "GET"
        conn.setRequestProperty("User-Agent", "WhiteTransport Android planner")
        return try {
            val status = conn.responseCode
            val stream = if (status >= 400) conn.errorStream else conn.inputStream
            val body = stream?.bufferedReader(Charsets.UTF_8)?.use { it.readText() }.orEmpty()
            if (status !in 200..299) {
                error("planner HTTP $status: $body")
            }
            parsePlan(JSONObject(body))
        } finally {
            conn.disconnect()
        }
    }

    fun parsePlan(obj: JSONObject): CarrierPlan = CarrierPlan(
        trafficClass = obj.getString("traffic_class"),
        strategy = obj.getString("strategy"),
        primary = obj.getJSONObject("primary").getString("id"),
        parallel = carrierIds(obj.optJSONArray("parallel")),
        repair = carrierIds(obj.optJSONArray("repair")),
        mirrorCount = obj.optInt("mirror_count", 0),
        hedgeTimeoutMs = obj.optLong("hedge_timeout_ms", 0L),
        maxInFlightChunks = obj.optInt("max_in_flight_chunks", 1),
        placements = buildList {
            val arr = obj.optJSONArray("placements") ?: return@buildList
            for (i in 0 until arr.length()) {
                val item = arr.getJSONObject(i)
                add(
                    ChunkPlacement(
                        index = item.getInt("index"),
                        offset = item.getInt("offset"),
                        size = item.getInt("size"),
                        carrierId = item.getString("carrier_id"),
                        mirrorCarrierIds = strings(item.optJSONArray("mirror_carrier_ids")),
                        hedgeCarrierIds = strings(item.optJSONArray("hedge_carrier_ids")),
                        hedgeAfterMs = item.optLong("hedge_after_ms", 0L),
                    )
                )
            }
        },
    )

    private fun carrierIds(arr: org.json.JSONArray?): List<String> = buildList {
        if (arr == null) return@buildList
        for (i in 0 until arr.length()) {
            add(arr.getJSONObject(i).getString("id"))
        }
    }

    private fun strings(arr: org.json.JSONArray?): List<String> = buildList {
        if (arr == null) return@buildList
        for (i in 0 until arr.length()) {
            add(arr.getString(i))
        }
    }

    private fun encode(value: String): String = URLEncoder.encode(value, "UTF-8")
}
