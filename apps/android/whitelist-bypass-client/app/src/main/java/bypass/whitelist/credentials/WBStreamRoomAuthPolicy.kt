package bypass.whitelist.credentials

import java.net.URI

/** Strict, provider-owned policy for the Android WBStream MiniAuth WebView. */
object WBStreamRoomAuthPolicy {
    const val loginUrl = "https://stream.wb.ru/login"

    private val allowedHosts = setOf("stream.wb.ru", "wb.ru", "wildberries.ru")
    private val capturedCookies = setOf("x_wbaas_token", "wbx-validation-key", "_wbauid")
    private val accessTokenPattern = Regex("""\\?"accessToken\\?"\s*:\s*\\?"([^"\\]+)""")

    fun isAllowedNavigation(rawUrl: String): Boolean {
        val uri = runCatching { URI(rawUrl) }.getOrNull() ?: return false
        return uri.scheme.equals("https", ignoreCase = true) &&
            uri.host?.lowercase() in allowedHosts
    }

    /** Return only the WBStream cookies required by the local runtime. */
    fun cookieHeader(rawHeader: String?): String? {
        val selected = rawHeader.orEmpty()
            .split(';')
            .map(String::trim)
            .filter { entry -> entry.substringBefore('=').trim() in capturedCookies }
        return selected.takeIf { it.isNotEmpty() }?.joinToString("; ")
    }

    /** Decode Android WebView's JSON-quoted JavaScript return value safely. */
    fun accessTokenFromJavascriptResult(result: String?): String? {
        return accessTokenPattern.find(result.orEmpty())?.groupValues?.getOrNull(1)?.trim()?.takeIf(String::isNotEmpty)
    }
}
