package bypass.whitelist.tunnel

/**
 * Transport negotiation policy.
 *
 * A room/node may advertise which tunnel language it wants to speak. Auto mode
 * follows that node preference first, because both sides must pick the same
 * transport before any user traffic can flow. If a node does not advertise a
 * preference yet, we fall back to the currently measured provider default. User
 * saved destinations may still override tunnelMode explicitly.
 */
object TransportPolicy {
    fun parseAdvertisedMode(raw: String?): TunnelMode? {
        val normalized = raw
            ?.trim()
            ?.replace('-', '_')
            ?.replace(' ', '_')
            ?.uppercase()
            ?: return null
        return when (normalized) {
            "DC", "DATA", "DATA_CHANNEL", "DATACHANNEL", "RELIABLE", "RELIABLE_DC", "WEBRTC_DC" -> TunnelMode.DC
            "VIDEO", "VP8", "VIDEO_VP8", "MEDIA", "TRACK" -> TunnelMode.VIDEO
            else -> runCatching { TunnelMode.valueOf(normalized) }.getOrNull()
        }
    }

    fun providerDefault(platform: CallPlatform): TunnelMode? = when (platform) {
        // Manual Android/WBStream benchmark: DC has lower small-request latency,
        // but VIDEO is the stable default until DC large-stream handling is fixed.
        CallPlatform.WBSTREAM -> TunnelMode.VIDEO
        // Telemost/Dion currently only support video transport in relayMode().
        CallPlatform.TELEMOST, CallPlatform.DION -> TunnelMode.VIDEO
        // VK video is the conservative historical default until per-provider
        // benchmark data says otherwise.
        CallPlatform.VK -> TunnelMode.VIDEO
    }

    fun autoModeFor(url: String, advertisedMode: TunnelMode?): TunnelMode? {
        val platform = CallPlatform.fromUrl(url)
        return (advertisedMode ?: providerDefault(platform))?.forPlatform(platform)
    }
}
