package bypass.whitelist

/** Shared client capability response emitted by the Capacitor host. */
internal data class CapacitorCapabilitiesContract(
    val host: String,
    val booleans: Map<String, Boolean>,
)

internal fun capacitorCapabilities(systemVpnAvailable: Boolean): CapacitorCapabilitiesContract =
    CapacitorCapabilitiesContract(
        host = "capacitor",
        booleans = linkedMapOf(
            "transport" to true,
            "endpoints" to true,
            "logs" to true,
            "splitRouting" to systemVpnAvailable,
            "proxyRouting" to false,
            "systemVpn" to systemVpnAvailable,
            "requestVpnPermission" to systemVpnAvailable,
            "startSystemVpn" to systemVpnAvailable,
            "stopSystemVpn" to systemVpnAvailable,
            "smokeTest" to false,
        ),
    )

internal fun capacitorSplitRoutingResponse(mode: String, packages: Set<String>): Map<String, Any> = linkedMapOf(
    "mode" to mode,
    "lan_access" to false,
    "packages" to packages.sorted(),
)

internal fun requireSupportedLanAccess(enabled: Boolean?) {
    if (enabled == true) {
        throw UnsupportedOperationException("Android LAN access bypass is unsupported")
    }
}
