//go:build !android && !darwin

package mobile

import "fmt"

// StartTun2Socks is a stub for platforms without the Android/macOS production
// TUN-to-SOCKS5 engine binding.
func StartTun2Socks(fd int64, mtu int64, socksPort int64, socksUser string, socksPass string) error {
	return fmt.Errorf("tun2socks: not supported on this platform")
}

// StopTun2Socks is a no-op on non-Android platforms.
func StopTun2Socks() error { return nil }
