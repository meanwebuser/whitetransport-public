package bypass.whitelist.planner

import android.content.Context
import bypass.whitelist.credentials.LocalUserCredentialPolicy
import org.json.JSONObject
import java.io.File

object RuntimeConfigStore {
    private const val DIR_NAME = "wt-runtime"
    private const val FILE_NAME = "config.json"
    private const val ASSET_NAME = "wt-runtime-config.json"

    fun resolveConfigJson(context: Context, explicitConfigJson: String? = null): String {
        explicitConfigJson?.trim()?.takeIf { it.isNotEmpty() }?.let {
            val normalized = normalizeForApp(context, it)
            validateConfig(normalized)
            saveConfigJson(context, normalized)
            return normalized
        }

        configFile(context).takeIf { it.isFile }?.readText()?.trim()?.takeIf { it.isNotEmpty() }?.let {
            val normalized = normalizeForApp(context, it)
            validateConfig(normalized)
            return normalized
        }

        runCatching { context.assets.open(ASSET_NAME).bufferedReader().use { it.readText() } }
            .getOrNull()
            ?.trim()
            ?.takeIf { it.isNotEmpty() }
            ?.let {
                val normalized = normalizeForApp(context, it)
                validateConfig(normalized)
                saveConfigJson(context, normalized)
                return normalized
            }

        error("Go runtime config is not provisioned. Install a TokenStore config into app-private storage before connecting.")
    }

    fun saveConfigJson(context: Context, configJson: String) {
        val normalized = normalizeForApp(context, configJson)
        validateConfig(normalized)
        val file = configFile(context)
        file.parentFile?.mkdirs()
        file.writeText(normalized)
    }

    fun importFromDeviceFile(context: Context, path: String): JSONObject {
        val source = File(path)
        require(source.isFile) { "runtime config file not found: $path" }
        val configJson = source.readText().trim()
        saveConfigJson(context, configJson)
        return status(context).put("imported_from", path)
    }

    fun clear(context: Context) {
        configFile(context).delete()
    }

    fun status(context: Context): JSONObject {
        val file = configFile(context)
        val hasAsset = runCatching { context.assets.open(ASSET_NAME).close() }.isSuccess
        return JSONObject()
            .put("provisioned", file.isFile || hasAsset)
            .put("stored", file.isFile)
            .put("asset", hasAsset)
            .put("path", file.absolutePath)
    }

    fun preferredNodeId(context: Context): String? = runCatching {
        JSONObject(resolveConfigJson(context)).optString("preferred_node_id").takeIf { it.isNotBlank() }
    }.getOrNull()

    fun configuredSocksPort(context: Context): Int = runCatching {
        val listen = JSONObject(resolveConfigJson(context)).optString("socks_listen", "127.0.0.1:1085")
        listen.substringAfterLast(":", "1085").toInt().takeIf { it in 1..65535 } ?: 1085
    }.getOrDefault(1085)

    fun setSocksPort(context: Context, port: Int) {
        require(port in 1..65535) { "SOCKS port must be between 1 and 65535" }
        val cfg = JSONObject(resolveConfigJson(context))
        cfg.put("socks_listen", "127.0.0.1:$port")
        saveConfigJson(context, cfg.toString())
    }

    private fun configFile(context: Context): File = File(File(context.filesDir, DIR_NAME), FILE_NAME)

    private fun validateConfig(configJson: String) {
        val cfg = JSONObject(configJson)
        LocalUserCredentialPolicy.rejectUserCredentialSerialization(cfg)
        require(cfg.optJSONObject("token_store") != null) { "runtime config must include token_store" }
        require(cfg.optJSONArray("carrier_configs") != null) { "runtime config must include carrier_configs" }
    }

    fun validateConfigJson(configJson: String): JSONObject {
        validateConfig(configJson)
        return JSONObject(configJson)
    }

    private fun normalizeForApp(context: Context, configJson: String): String {
        val runtimeDir = File(context.filesDir, DIR_NAME)
        val singBoxBinary = normalizedSingBoxBinaryPath(File(context.applicationInfo.nativeLibraryDir))
        return normalizeRuntimeConfigPaths(configJson, runtimeDir, singBoxBinary)
    }
}

internal fun normalizedSingBoxBinaryPath(nativeLibraryDir: File): File =
    File(nativeLibraryDir, "libsingbox.so")

internal fun normalizeRuntimeConfigPaths(configJson: String, runtimeDir: File, singBoxBinary: File? = null): String {
        val cfg = JSONObject(configJson)
        val stateFile = cfg.optString("state_file", "")
        if (stateFile.isBlank() || stateFile.startsWith("/tmp/")) {
            cfg.put("state_file", File(runtimeDir, "state.json").absolutePath)
        }
        val sessionEgress = cfg.optJSONObject("session_egress") ?: JSONObject().also {
            cfg.put("session_egress", it)
        }
        val sessionSingBox = sessionEgress.optJSONObject("sing_box") ?: JSONObject().also {
            sessionEgress.put("sing_box", it)
        }
        singBoxBinary?.let { sessionSingBox.put("binary_path", it.absolutePath) }
        sessionSingBox.put(
            "config_dir",
            normalizedSingBoxConfigDir(sessionSingBox.optString("config_dir", ""), runtimeDir),
        )
        val carriers = cfg.optJSONArray("carrier_configs")
        if (carriers != null) {
            for (index in 0 until carriers.length()) {
                val singBox = carriers.optJSONObject(index)?.optJSONObject("sing_box") ?: continue
                val configDir = singBox.optString("config_dir", "")
                singBox.put("config_dir", normalizedSingBoxConfigDir(configDir, runtimeDir))
            }
        }
        return cfg.toString()
}

internal fun normalizedSingBoxConfigDir(configDir: String, runtimeDir: File): String =
    if (configDir.isBlank() || configDir.startsWith("/tmp/") || configDir.startsWith("/data/local/tmp/")) {
        File(runtimeDir, "sing-box").path
    } else {
        configDir
    }
