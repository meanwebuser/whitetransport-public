package bypass.whitelist.planner

import org.json.JSONArray
import org.json.JSONObject
import java.io.BufferedReader
import java.io.InputStreamReader
import java.net.HttpURLConnection
import java.net.URL

object RuntimeApiClient {
    fun fetchNodes(
        baseUrl: String,
        connectTimeoutMs: Int = 3000,
        readTimeoutMs: Int = 5000,
    ): JSONArray {
        val body = request(baseUrl, "/v1/nodes", "GET", null, connectTimeoutMs, readTimeoutMs)
        return JSONArray(body)
    }

    fun fetchStatus(
        baseUrl: String,
        connectTimeoutMs: Int = 3000,
        readTimeoutMs: Int = 5000,
    ): JSONObject {
        val body = request(baseUrl, "/v1/status", "GET", null, connectTimeoutMs, readTimeoutMs)
        return JSONObject(body)
    }

    fun connect(
        baseUrl: String,
        nodeId: String?,
        connectTimeoutMs: Int = 3000,
        readTimeoutMs: Int = 10000,
    ): JSONObject {
        val payload = if (nodeId.isNullOrBlank()) "{}" else JSONObject().put("node_id", nodeId).toString()
        val body = request(baseUrl, "/v1/session/connect", "POST", payload, connectTimeoutMs, readTimeoutMs)
        return JSONObject(body)
    }

    fun disconnect(
        baseUrl: String,
        connectTimeoutMs: Int = 3000,
        readTimeoutMs: Int = 5000,
    ): JSONObject {
        val body = request(baseUrl, "/v1/session/disconnect", "POST", "", connectTimeoutMs, readTimeoutMs)
        return JSONObject(body)
    }

    private fun request(
        baseUrl: String,
        path: String,
        method: String,
        payload: String?,
        connectTimeoutMs: Int,
        readTimeoutMs: Int,
    ): String {
        val normalizedBase = baseUrl.trim().trimEnd('/')
        require(normalizedBase.isNotBlank()) { "runtime baseUrl is required" }

        val conn = URL("$normalizedBase$path").openConnection() as HttpURLConnection
        conn.requestMethod = method
        conn.connectTimeout = connectTimeoutMs
        conn.readTimeout = readTimeoutMs
        conn.setRequestProperty("User-Agent", "WhiteTransport-Android/RuntimeApiClient")
        conn.setRequestProperty("Accept", "application/json")

        if (payload != null) {
            conn.doOutput = true
            conn.setRequestProperty("Content-Type", "application/json")
            conn.outputStream.use { it.write(payload.toByteArray(Charsets.UTF_8)) }
        }

        return try {
            val status = conn.responseCode
            val body = readBody(conn)
            if (status !in 200..299) {
                error("runtime HTTP $status: $body")
            }
            body
        } finally {
            conn.disconnect()
        }
    }

    private fun readBody(conn: HttpURLConnection): String {
        val source = conn.errorStream ?: conn.inputStream
        return source.bufferedReader(Charsets.UTF_8).use(BufferedReader::readText)
    }
}
