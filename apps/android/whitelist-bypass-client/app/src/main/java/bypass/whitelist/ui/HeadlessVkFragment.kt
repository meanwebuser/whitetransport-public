package bypass.whitelist.ui

import android.graphics.Color
import android.os.Bundle
import android.util.Log
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.ImageButton
import androidx.core.view.isVisible
import androidx.fragment.app.Fragment
import bypass.whitelist.R
import bypass.whitelist.tunnel.HeadlessRelayController
import bypass.whitelist.tunnel.VpnStatus
import bypass.whitelist.util.BLANK_URL
import bypass.whitelist.util.Prefs
import java.util.concurrent.atomic.AtomicBoolean

class HeadlessVkFragment : Fragment(), JoinSessionShutdown {

    private lateinit var relay: HeadlessRelayController
    private lateinit var webView: WebView
    private val shutdownOnce = AtomicBoolean(false)

    private val host: JoinFragmentHost?
        get() = activity as? JoinFragmentHost

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?,
    ): View = inflater.inflate(R.layout.fragment_headless_vk, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        webView = view.findViewById(R.id.captchaWebView)
        view.findViewById<ImageButton>(R.id.captchaBackButton).setOnClickListener {
            host?.onJoinCancel()
        }
        val url = requireArguments().getString(ARG_URL, "")
        val displayName = Prefs.autofillName

        webView.settings.javaScriptEnabled = true
        webView.settings.domStorageEnabled = true
        webView.webViewClient = WebViewClient()
        webView.setBackgroundColor(Color.WHITE)
        webView.isVisible = false

        relay = HeadlessRelayController(
            requireContext().applicationInfo.nativeLibraryDir,
            onLog = { message ->
                if (!isSessionAlive()) return@HeadlessRelayController
                if (message.contains("ERROR:") && !message.contains("ortc ERROR")) {
                    host?.onJoinStatusText(message)
                }
                host?.appendLog(message)
            },
            onStatus = { status ->
                if (!isSessionAlive()) return@HeadlessRelayController
                Log.d("HEADLESS-VK", "status: $status")
                if (status == VpnStatus.TUNNEL_ACTIVE) {
                    activity?.runOnUiThread {
                        if (!isSessionAlive()) return@runOnUiThread
                        host?.onJoinStatusText("Relay ready, starting local VPN")
                        webView.stopLoading()
                        webView.loadUrl(BLANK_URL)
                        webView.isVisible = false
                        host?.setJoinUiVisible(false)
                        host?.requestVpn()
                    }
                } else {
                    host?.onJoinStatus(status)
                }
            },
            onCaptchaUrl = { captchaUrl ->
                if (!isSessionAlive()) return@HeadlessRelayController
                Log.d("HEADLESS-VK", "captcha URL: $captchaUrl")
                activity?.runOnUiThread {
                    if (!isSessionAlive()) return@runOnUiThread
                    host?.setJoinUiVisible(true)
                    webView.isVisible = true
                    webView.loadUrl(captchaUrl)
                }
            },
        )
        relay.start()
        relay.sendAuth(url, displayName, Prefs.activeTunnelMode.relayArg)
    }

    override fun onDestroyView() {
        shutdownSession()
        if (::webView.isInitialized) {
            runCatching {
                webView.stopLoading()
                webView.loadUrl(BLANK_URL)
                webView.destroy()
            }
        }
        super.onDestroyView()
    }

    override fun shutdownSession() {
        if (!shutdownOnce.compareAndSet(false, true)) return
        if (::webView.isInitialized) {
            runCatching {
                webView.stopLoading()
                webView.loadUrl(BLANK_URL)
            }
        }
        if (::relay.isInitialized) {
            runCatching { relay.stop() }
        }
    }

    private fun isSessionAlive(): Boolean = !shutdownOnce.get()

    companion object {
        const val ARG_URL = "url"

        fun newInstance(url: String): HeadlessVkFragment {
            return HeadlessVkFragment().apply {
                arguments = Bundle().apply {
                    putString(ARG_URL, url)
                }
            }
        }
    }
}
