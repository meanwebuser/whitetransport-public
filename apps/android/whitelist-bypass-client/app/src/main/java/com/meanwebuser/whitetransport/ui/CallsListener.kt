package com.meanwebuser.whitetransport.ui

import com.meanwebuser.whitetransport.tunnel.CallConfig

interface CallsListener {
    fun onDestinationSelected(config: CallConfig)
    fun onDestinationsChanged()
}
