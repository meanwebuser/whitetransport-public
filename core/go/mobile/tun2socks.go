//go:build android || darwin

package mobile

// StartTun2Socks starts a tun2socks bridge that reads IP packets from the
// given TUN file descriptor and forwards TCP/UDP flows through the local
// SOCKS5 proxy. Android VpnService and macOS Network Extension hosts provide
// the descriptor; this bridge routes their packets through the Go runtime's
// SOCKS5 proxy and active WhiteTransport carrier session.
//
// engine.Start initializes the pinned tun2socks engine and returns. A successful
// call leaves the bridge running until StopTun2Socks explicitly owns teardown.
func StartTun2Socks(fd int64, mtu int64, socksPort int64, socksUser string, socksPass string) error {
	return startTun2SocksSession(engineRunner{}, fd, mtu, socksPort, socksUser, socksPass)
}

const tun2SocksProductionBinding = true

// StopTun2Socks stops the running tun2socks engine exactly once. It is safe to
// call when idle and returns any teardown error to the native host.
func StopTun2Socks() error {
	return stopTun2SocksSession()
}
