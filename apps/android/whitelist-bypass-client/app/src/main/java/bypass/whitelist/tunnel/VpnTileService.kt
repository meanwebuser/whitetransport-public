package bypass.whitelist.tunnel

import android.content.Intent
import android.graphics.drawable.Icon
import android.os.Build
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService
import android.util.Log
import androidx.annotation.RequiresApi
import androidx.core.content.ContextCompat
import bypass.whitelist.R

@RequiresApi(Build.VERSION_CODES.N)
class VpnTileService : TileService() {

    companion object {
        private const val TAG = "VpnTileService"
    }

    override fun onStartListening() {
        super.onStartListening()
        updateTile()
    }

    override fun onClick() {
        super.onClick()
        if (TunnelServiceState.isAnyTunnelComponentRunning(this)) {
            stopAll()
            return
        }

        if (isLocked) {
            updateTile()
            return
        }

        unlockAndRun {
            startSession()
            qsTile?.let {
                renderIconOnly(it, Tile.STATE_ACTIVE)
            }
        }
    }

    private fun startSession() {
        try {
            val intent = Intent(this, HeadlessSessionService::class.java)
            ContextCompat.startForegroundService(this, intent)
            Log.d(TAG, "HeadlessSessionService start requested")
        } catch (e: Exception) {
            Log.e(TAG, "Failed to start HeadlessSessionService: ${e.message}", e)
        }
    }

    private fun stopAll() {
        HeadlessSessionService.requestStop(this)
        TunnelVpnService.requestStop(this)
        ProxyService.requestStop(this)
        qsTile?.let {
            renderIconOnly(it, Tile.STATE_INACTIVE)
        }
    }

    private fun renderIconOnly(tile: Tile, state: Int) {
        tile.state = state
        tile.label = ""
        tile.icon = Icon.createWithResource(this, R.drawable.ic_launcher_foreground)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) tile.subtitle = ""
        tile.updateTile()
    }

    private fun updateTile() {
        val tile = qsTile ?: return
        when {
            TunnelServiceState.isTunnelActive(this) -> {
                renderIconOnly(tile, Tile.STATE_ACTIVE)
            }
            TunnelServiceState.isHeadlessSessionRunning(this) -> {
                renderIconOnly(tile, Tile.STATE_ACTIVE)
            }
            else -> {
                renderIconOnly(tile, Tile.STATE_INACTIVE)
            }
        }
        tile.updateTile()
    }
}
