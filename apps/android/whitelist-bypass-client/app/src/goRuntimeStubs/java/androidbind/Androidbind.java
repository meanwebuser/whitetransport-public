package androidbind;

/**
 * Legacy androidbind stubs. The Go-runtime build replaces the old native
 * androidbind AAR with these no-op/delegate stubs. tun2socks is now handled
 * by the Go mobile bindings (mobile.Mobile.startTun2Socks / stopTun2Socks)
 * via GoRuntimeController, so these methods are kept only for backward
 * compatibility with code that still references androidbind.Androidbind.
 */
public final class Androidbind {
    private Androidbind() {}

    public static int activeWsPort() {
        return 0;
    }

    public static void stopJoiner() {}

    public static void startJoiner(long wsPort, long socksPort, String socksHost, String socksUser, String socksPass, LogCallback cb) {
        if (cb != null) cb.onLog("legacy androidbind startJoiner unavailable in Go runtime build");
    }

    /** @deprecated Use GoRuntimeController.stopTun2Socks() instead. */
    @Deprecated
    public static void stopTun2Socks() {}

    /** @deprecated Use GoRuntimeController.startTun2Socks() instead. */
    @Deprecated
    public static void startTun2Socks(long fd, long mtu, long socksPort, String socksUser, String socksPass) {
        // No-op: tun2socks is now handled by the Go mobile bindings.
        // This stub is kept for backward compatibility. Callers should
        // migrate to GoRuntimeController.startTun2Socks().
    }
}
