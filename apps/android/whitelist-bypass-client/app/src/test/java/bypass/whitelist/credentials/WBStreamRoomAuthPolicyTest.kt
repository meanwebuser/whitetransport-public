package bypass.whitelist.credentials

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class WBStreamRoomAuthPolicyTest {
    @Test
    fun `only declared HTTPS WBStream hosts may load in mini auth webview`() {
        assertTrue(WBStreamRoomAuthPolicy.isAllowedNavigation(WBStreamRoomAuthPolicy.loginUrl))
        assertTrue(WBStreamRoomAuthPolicy.isAllowedNavigation("https://wildberries.ru/profile"))
        assertFalse(WBStreamRoomAuthPolicy.isAllowedNavigation("http://stream.wb.ru/login"))
        assertFalse(WBStreamRoomAuthPolicy.isAllowedNavigation("https://example.invalid/redirect"))
    }

    @Test
    fun `completion extracts only required WBStream local session fields`() {
        assertEquals(
            "x_wbaas_token=room-cookie; _wbauid=identifier",
            WBStreamRoomAuthPolicy.cookieHeader("other=ignored; x_wbaas_token=room-cookie; _wbauid=identifier"),
        )
        assertEquals(
            "room-access",
            WBStreamRoomAuthPolicy.accessTokenFromJavascriptResult("\"{\\\"accessToken\\\":\\\"room-access\\\"}\""),
        )
        assertNull(WBStreamRoomAuthPolicy.accessTokenFromJavascriptResult("\"{}\""))
    }
}
