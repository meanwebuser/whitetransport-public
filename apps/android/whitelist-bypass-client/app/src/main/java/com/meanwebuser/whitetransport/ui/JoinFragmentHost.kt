package com.meanwebuser.whitetransport.ui

import com.meanwebuser.whitetransport.tunnel.VpnStatus

interface JoinFragmentHost {
    fun appendLog(message: String)
    fun onJoinStatus(status: VpnStatus)
    fun onJoinStatusText(text: String)
    fun requestVpn()
    fun setJoinUiVisible(visible: Boolean)
    fun onJoinCancel()
}
