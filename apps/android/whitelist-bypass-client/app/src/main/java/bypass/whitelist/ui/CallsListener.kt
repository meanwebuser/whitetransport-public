package bypass.whitelist.ui

import bypass.whitelist.tunnel.CallConfig

interface CallsListener {
    fun onDestinationSelected(config: CallConfig)
    fun onDestinationsChanged()
}
