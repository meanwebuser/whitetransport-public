package bypass.whitelist

import android.content.Intent
import android.net.VpnService
import android.os.Bundle
import androidx.activity.result.ActivityResultLauncher
import androidx.activity.result.contract.ActivityResultContracts
import com.getcapacitor.BridgeActivity
import bypass.whitelist.util.Prefs
import bypass.whitelist.util.LogWriter
import bypass.whitelist.tunnel.TunnelServiceState

/**
 * Capacitor WebView host for the unified React client UI (apps/client-web).
 *
 * PoC (Step B): this is the new launcher. The legacy Fragment-based
 * [MainActivity] is kept registered (non-launcher) for rollback until Step D.
 * The WtTransport plugin (Step B3) is registered here and bridges the WebView
 * to the existing TunnelVpnService / ProxyService.
 */
class CapacitorMainActivity : BridgeActivity() {
    lateinit var vpnCoordinator: CapacitorVpnCoordinator
        private set

    private lateinit var vpnDependencies: AndroidCapacitorVpnDependencies
    private lateinit var vpnPermissionLauncher: ActivityResultLauncher<Intent>
    private val vpnStatusOwner = Any()
    private var activeVpnConsentToken: Long? = null
    private lateinit var capacitorLogWriter: LogWriter
    private val capacitorLogCallback: (String) -> Unit = { message ->
        capacitorLogWriter.append(message)
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        Prefs.init(this)
        capacitorLogWriter = LogWriter(cacheDir, maxDisplayLines = 300)
        capacitorLogWriter.reset()
        TunnelServiceState.logCallback = capacitorLogCallback
        vpnDependencies = AndroidCapacitorVpnDependencies(applicationContext)
        vpnCoordinator = CapacitorVpnCoordinator(vpnDependencies)
        vpnPermissionLauncher = registerForActivityResult(
            ActivityResultContracts.StartActivityForResult()
        ) { result ->
            val operationToken = activeVpnConsentToken ?: return@registerForActivityResult
            activeVpnConsentToken = null
            vpnCoordinator.onVpnPermissionResult(operationToken, result.resultCode == RESULT_OK)
        }
        vpnDependencies.launchVpnConsent = { operationToken ->
            val intent = VpnService.prepare(this)
            if (intent == null) {
                vpnCoordinator.onVpnPermissionResult(operationToken, true)
            } else {
                check(activeVpnConsentToken == null) { "a VPN consent result is already pending" }
                activeVpnConsentToken = operationToken
                try {
                    vpnPermissionLauncher.launch(intent)
                } catch (error: Throwable) {
                    activeVpnConsentToken = null
                    throw error
                }
            }
        }
        TunnelServiceState.attachVpnStatusCallback(vpnStatusOwner) { status -> vpnCoordinator.onTunnelStatus(status) }
        registerPlugin(WtTransportPlugin::class.java)
        super.onCreate(savedInstanceState)
        vpnCoordinator.reconcileAfterRestart()
        GuiTestLaunchRunner.maybeRun(this, intent, vpnCoordinator)
    }

    override fun onDestroy() {
        TunnelServiceState.detachVpnStatusCallback(vpnStatusOwner)
        vpnDependencies.launchVpnConsent = null
        activeVpnConsentToken = null
        if (TunnelServiceState.logCallback === capacitorLogCallback) {
            TunnelServiceState.logCallback = null
        }
        capacitorLogWriter.close()
        vpnCoordinator.detachUi()
        super.onDestroy()
    }

    override fun onNewIntent(intent: android.content.Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        GuiTestLaunchRunner.maybeRun(this, intent, vpnCoordinator)
    }
}
