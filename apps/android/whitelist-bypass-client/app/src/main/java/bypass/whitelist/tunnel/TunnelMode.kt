package bypass.whitelist.tunnel

enum class TunnelMode(val label: String, val relayArg: String, val isPion: Boolean) {
    DC("DC", "dc", false),
    VIDEO("Video", "video", true);

    fun relayMode(platform: CallPlatform): String {
        if (!isPion) return "dc-joiner"
        return "${platform.id}-$relayArg-joiner"
    }

    fun forPlatform(platform: CallPlatform): TunnelMode {
        if (this == DC && (platform == CallPlatform.TELEMOST || platform == CallPlatform.DION)) {
            return VIDEO
        }
        return this
    }
}
