package bypass.whitelist

import android.annotation.SuppressLint
import android.app.Activity
import android.graphics.Color
import android.net.http.SslError
import android.os.Bundle
import android.view.ViewGroup
import android.webkit.CookieManager
import android.webkit.SslErrorHandler
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.LinearLayout
import android.widget.TextView
import bypass.whitelist.credentials.LocalUserCredentialStore
import bypass.whitelist.credentials.WBStreamRoomAuthPolicy
import java.util.concurrent.atomic.AtomicBoolean

/**
 * A deliberately narrow in-app WBStream login view. It blocks every origin
 * outside the provider allowlist and sends a successful session straight into
 * Android Keystore; it never exports browser cookies or profiles.
 */
class WBStreamRoomAuthActivity : Activity() {
    private lateinit var webView: WebView
    private val completed = AtomicBoolean(false)

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        title = "Вход WB Stream"

        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(Color.WHITE)
        }
        root.addView(TextView(this).apply {
            text = "Вход остаётся внутри WhiteTransport. После завершения сессия сохраняется только на устройстве."
            setPadding(32, 24, 32, 16)
        })
        webView = WebView(this).apply {
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true
            settings.allowFileAccess = false
            settings.allowContentAccess = false
            settings.mixedContentMode = android.webkit.WebSettings.MIXED_CONTENT_NEVER_ALLOW
            setBackgroundColor(Color.WHITE)
        }
        root.addView(webView, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))
        setContentView(root)

        CookieManager.getInstance().setAcceptCookie(true)
        webView.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                return !WBStreamRoomAuthPolicy.isAllowedNavigation(request.url.toString())
            }

            override fun onPageFinished(view: WebView, url: String) {
                if (WBStreamRoomAuthPolicy.isAllowedNavigation(url)) captureSessionIfReady()
            }

            override fun onReceivedSslError(view: WebView, handler: SslErrorHandler, error: SslError) {
                handler.cancel()
            }
        }
        webView.loadUrl(WBStreamRoomAuthPolicy.loginUrl)
    }

    private fun captureSessionIfReady() {
        if (completed.get()) return
        val cookieHeader = WBStreamRoomAuthPolicy.cookieHeader(
            CookieManager.getInstance().getCookie(WBStreamRoomAuthPolicy.loginUrl),
        ) ?: return
        webView.evaluateJavascript("(function(){try{return localStorage.getItem('wb_auth_auth_slice')||''}catch(_){return ''}})()") { result ->
            val accessToken = WBStreamRoomAuthPolicy.accessTokenFromJavascriptResult(result) ?: return@evaluateJavascript
            if (!completed.compareAndSet(false, true)) return@evaluateJavascript
            LocalUserCredentialStore.putRoomSession(this, "wbstream", accessToken, cookieHeader)
            setResult(RESULT_OK)
            finish()
        }
    }

    override fun onDestroy() {
        if (::webView.isInitialized) {
            webView.stopLoading()
            webView.destroy()
        }
        super.onDestroy()
    }
}
